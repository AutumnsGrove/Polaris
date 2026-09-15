#!/usr/bin/env bash
#
# One-command bare-metal dev stack launcher — see issue #72. Starts/stops
# vite, the Go backend, the code_exec watcher loop, and the local SearXNG
# container, and can report whether all four are actually up.
#
# Usage: dev/stack.sh [start|stop|restart|status]   (default: restart)
#
# Everything is launched as a `set -m` background job, not a plain `&`
# without job control: `setsid` (the obvious tool for "detach into your
# own session") is Linux-only and doesn't exist on macOS, so this reuses
# the same trick compose/watcher/codeexec.sh already relies on — with
# job control enabled, each `&` job becomes its own process-group leader
# automatically, portable to both. That's what lets the stack survive
# this script itself exiting (or being run from a Claude Code tool call,
# whose backgrounded jobs die at the tool-call boundary — the exact pain
# issue #72 was filed over) and lets `stop` below kill `pnpm run dev`'s
# real Node child along with its shell wrapper via `kill -- -$pid`
# (negative PID = whole process group), not just the wrapper PID a bare
# `kill $pid` would leave orphaned.
set -euo pipefail
set -m

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$ROOT/dev/.stack"
mkdir -p "$STATE_DIR"

VITE_PORT=45173
BACKEND_PORT=8899
SEARXNG_PORT=18888
SEARXNG_CONTAINER=searxng-dev

pidfile() { echo "$STATE_DIR/$1.pid"; }
logfile() { echo "$STATE_DIR/$1.log"; }

is_alive() {
	local pf; pf="$(pidfile "$1")"
	[ -f "$pf" ] && kill -0 "$(cat "$pf")" 2>/dev/null
}

# Recursive parent->child kill via pgrep -P, not `kill -- -$pid` (negative
# PID / process-group kill) alone: confirmed live on macOS that nohup
# doesn't always exec-replace itself the way compose/watcher/codeexec.sh's
# own `set -m` comment assumes on Linux — it can leave an extra fork
# layer where $pid (from $!) and the job's actual pgid are different
# values, so a process-group kill misses real descendants (e.g. `pnpm run
# dev`'s child `vite` process survives, orphaned, PPID reparented to 1,
# with nothing left to kill it on any future run). Walking the actual
# parent/child tree by PID sidesteps that pgid ambiguity entirely.
kill_tree() {
	local pid="$1" child
	for child in $(pgrep -P "$pid" 2>/dev/null || true); do
		kill_tree "$child"
	done
	kill -9 "$pid" 2>/dev/null || true
}

stop_proc() {
	local name="$1" pf; pf="$(pidfile "$name")"
	if [ -f "$pf" ]; then
		local pid; pid="$(cat "$pf")"
		if kill -0 "$pid" 2>/dev/null; then
			kill -- -"$pid" 2>/dev/null || true
			for _ in $(seq 1 10); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.2
			done
			kill_tree "$pid"
		fi
		rm -f "$pf"
	fi
}

free_port() {
	local port="$1" pids
	pids="$(lsof -ti:"$port" 2>/dev/null || true)"
	[ -n "$pids" ] && kill -9 $pids 2>/dev/null || true
}

start_bg() {
	local name="$1"; shift
	nohup "$@" >"$(logfile "$name")" 2>&1 </dev/null &
	echo $! >"$(pidfile "$name")"
	disown
}

has_code_exec_config() {
	grep -q "^code_exec:" "$ROOT/config.yaml" 2>/dev/null
}

port_listening() {
	lsof -ti:"$1" >/dev/null 2>&1
}

do_stop() {
	echo "Stopping dev stack..."
	stop_proc vite
	stop_proc backend
	stop_proc codeexec
	free_port "$VITE_PORT"
	free_port "$BACKEND_PORT"
}

do_start() {
	echo "Starting dev stack..."

	echo "  vite (:$VITE_PORT)"
	( cd "$ROOT/web" && start_bg vite pnpm run dev )

	echo "  backend (:$BACKEND_PORT)"
	( cd "$ROOT" && start_bg backend go run . run --dev )

	if has_code_exec_config; then
		echo "  code_exec watcher loop"
		start_bg codeexec bash -c 'while true; do "'"$ROOT"'/compose/watcher/codeexec.sh"; sleep 1; done'
	else
		echo "  code_exec watcher skipped (no code_exec: block in config.yaml)"
	fi

	if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$SEARXNG_CONTAINER"; then
		if docker ps --format '{{.Names}}' | grep -qx "$SEARXNG_CONTAINER"; then
			echo "  searxng (:$SEARXNG_PORT) already running"
		else
			echo "  searxng (:$SEARXNG_PORT) starting existing container"
			docker start "$SEARXNG_CONTAINER" >/dev/null
		fi
	else
		echo "  searxng (:$SEARXNG_PORT) creating container"
		docker run -d --name "$SEARXNG_CONTAINER" -p "$SEARXNG_PORT:8080" \
			-v "$ROOT/dev/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
			searxng/searxng:latest >/dev/null
	fi
}

do_status() {
	printf "%-12s %-8s %-10s %s\n" "COMPONENT" "PID" "PORT" "STATUS"
	for entry in "vite:$VITE_PORT" "backend:$BACKEND_PORT" "codeexec:-"; do
		name="${entry%%:*}"; port="${entry##*:}"
		if is_alive "$name"; then
			pid="$(cat "$(pidfile "$name")")"
			if [ "$port" != "-" ]; then
				if port_listening "$port"; then st="up"; else st="pid alive, port not listening yet"; fi
			else
				st="up"
			fi
			printf "%-12s %-8s %-10s %s\n" "$name" "$pid" "$port" "$st"
		else
			printf "%-12s %-8s %-10s %s\n" "$name" "-" "$port" "down"
		fi
	done
	if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$SEARXNG_CONTAINER"; then
		printf "%-12s %-8s %-10s %s\n" "searxng" "-" "$SEARXNG_PORT" "up"
	else
		printf "%-12s %-8s %-10s %s\n" "searxng" "-" "$SEARXNG_PORT" "down"
	fi
	echo
	echo "Logs: $STATE_DIR/{vite,backend,codeexec}.log"
}

cmd="${1:-restart}"
case "$cmd" in
	start) do_start ;;
	stop) do_stop ;;
	restart) do_stop; do_start ;;
	status) do_status ;;
	*)
		echo "Usage: $0 [start|stop|restart|status]" >&2
		exit 1
		;;
esac

if [ "$cmd" != "status" ] && [ "$cmd" != "stop" ]; then
	echo
	sleep 2
	do_status
fi
