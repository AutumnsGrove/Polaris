#!/usr/bin/env bash
#
# One-command bare-metal dev stack launcher — see issue #72. Starts/stops
# vite, the Go backend, the code_exec watcher loop, and the local SearXNG
# container, and can report whether all four are actually up.
#
# Usage: dev/stack.sh [start|stop|restart|status] [--fake-llm] [--fake-llm-delay=DURATION]
#   (default command: restart)
#
# --fake-llm runs the backend against dev/fakeopenrouter instead of real
# OpenRouter (see its own package doc comment) — no OPENROUTER_API_KEY
# needed, and turns are scriptable/deterministic via its /_control/queue
# API, which is what makes this useful for actually watching (or
# Playwright-driving) a multi-tool-call turn stream in end to end.
# --fake-llm-delay=1500ms also implies --fake-llm and slows every model
# call down by that much — plenty for a screenshot or a manual page
# reload mid-turn; omit it (plain --fake-llm) for the default 0 delay,
# which answers instantly and suits an automated assertion instead.
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
FAKE_LLM_PORT=18901

# --fake-llm[-delay=DURATION] (start/restart only): runs the backend
# against dev/fakeopenrouter (see its own package doc comment) instead of
# real OpenRouter — no OPENROUTER_API_KEY needed, and turns are
# deterministic/scriptable via its /_control/queue API. Doesn't touch
# config.yaml itself: a derived copy with openrouter.base_url overridden
# is generated into STATE_DIR on every start and the backend is pointed
# at that instead (see fake_llm_config below). -delay defaults to 0
# (instant) unless --fake-llm-delay is also given — e.g. 1500ms, so a
# scripted multi-tool-call turn actually streams slowly enough to watch
# or to catch a thread genuinely mid-turn in the browser.
FAKE_LLM=0
FAKE_LLM_DELAY=0
for arg in "$@"; do
	case "$arg" in
		--fake-llm) FAKE_LLM=1 ;;
		--fake-llm-delay=*) FAKE_LLM=1; FAKE_LLM_DELAY="${arg#--fake-llm-delay=}" ;;
	esac
done

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

# Writes STATE_DIR/config.fake-llm.yaml: config.yaml's content with
# openrouter.base_url overridden to point at fakeopenrouter instead of
# real OpenRouter. Replaces an existing "  base_url:" line inside the
# openrouter: block in place; if that block has no such line (a config.yaml
# that relies on config.Load's real-OpenRouter default), one is inserted
# right before the block ends instead — either way exactly one base_url
# line survives under openrouter:. Everything else in config.yaml
# (api_key, every other section) passes through untouched.
write_fake_llm_config() {
	local url="http://127.0.0.1:$FAKE_LLM_PORT"
	awk -v url="$url" '
		/^openrouter:[[:space:]]*$/ { print; in_or=1; done=0; next }
		in_or && /^[^[:space:]]/ {
			if (!done) print "  base_url: \"" url "\""
			in_or=0
		}
		in_or && /^[[:space:]]+base_url:/ { print "  base_url: \"" url "\""; done=1; next }
		{ print }
		END { if (in_or && !done) print "  base_url: \"" url "\"" }
	' "$ROOT/config.yaml" >"$STATE_DIR/config.fake-llm.yaml"
}

port_listening() {
	lsof -ti:"$1" >/dev/null 2>&1
}

do_stop() {
	echo "Stopping dev stack..."
	stop_proc vite
	stop_proc backend
	stop_proc codeexec
	stop_proc fakellm
	free_port "$VITE_PORT"
	free_port "$BACKEND_PORT"
	free_port "$FAKE_LLM_PORT"
}

do_start() {
	echo "Starting dev stack..."

	echo "  vite (:$VITE_PORT)"
	( cd "$ROOT/web" && start_bg vite pnpm run dev )

	local backend_config="config.yaml"
	if [ "$FAKE_LLM" = "1" ]; then
		echo "  fake-llm (:$FAKE_LLM_PORT, delay=$FAKE_LLM_DELAY)"
		( cd "$ROOT" && start_bg fakellm go run ./dev/fakeopenrouter -addr "127.0.0.1:$FAKE_LLM_PORT" -delay "$FAKE_LLM_DELAY" )
		write_fake_llm_config
		backend_config="$STATE_DIR/config.fake-llm.yaml"
		echo "  backend (:$BACKEND_PORT, against fake-llm)"
	else
		echo "  backend (:$BACKEND_PORT)"
	fi
	( cd "$ROOT" && start_bg backend go run . run --dev --config "$backend_config" )

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
	for entry in "vite:$VITE_PORT" "backend:$BACKEND_PORT" "codeexec:-" "fakellm:$FAKE_LLM_PORT"; do
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
	echo "Logs: $STATE_DIR/{vite,backend,codeexec,fakellm}.log"
}

# $1 may be a flag (--fake-llm/--fake-llm-delay=...) rather than the
# command itself, e.g. `dev/stack.sh --fake-llm` meaning "restart, with
# fake-llm" — the flag loop above already scanned every "$@" for those, so
# here just skip past any leading flags to find the actual command word,
# defaulting to "restart" if there isn't one.
cmd="restart"
for arg in "$@"; do
	case "$arg" in
		--fake-llm | --fake-llm-delay=*) ;;
		*) cmd="$arg"; break ;;
	esac
done

case "$cmd" in
	start) do_start ;;
	stop) do_stop ;;
	restart) do_stop; do_start ;;
	status) do_status ;;
	*)
		echo "Usage: $0 [start|stop|restart|status] [--fake-llm] [--fake-llm-delay=DURATION]" >&2
		exit 1
		;;
esac

if [ "$cmd" != "status" ] && [ "$cmd" != "stop" ]; then
	echo
	sleep 2
	do_status
fi
