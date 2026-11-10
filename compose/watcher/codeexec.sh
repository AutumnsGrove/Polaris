#!/usr/bin/env bash
#
# Runs on the host (not inside any container) as a systemd oneshot
# service, triggered by polaris-codeexec.path the instant a request file
# appears in code-exec-signal/ — never invoked by the polaris container
# itself. This is the one place `docker run` for a code_exec sandbox
# actually happens; see docs/plans/code-execution.md's "How Polaris's
# own container reaches Docker" for why that boundary is deliberate,
# the same reasoning update.sh's own comment gives for update/restart.
#
# Requires python3 on the host — used only to parse/write the
# request/result JSON, never to touch its contents unsanitized:
# generated Python "code" is written straight from JSON into a file via
# Python's own file I/O, never interpolated into a shell command, so
# arbitrary code content (quotes, backslashes, newlines, shell
# metacharacters, anything) can't affect this script itself, only the
# sandboxed process that later executes that file.
set -euo pipefail

# set -m (job control / monitor mode): each `&` background job gets its
# own process group, which the watchdog kill below relies on. Without
# this, `kill "$watchdog_pid"` only signals the subshell wrapper bash
# process itself — confirmed live: a subshell running `sleep "$timeout_s"`
# followed by more statements (not bash's single-command tail-call exec
# optimization) left the `sleep` as an orphaned grandchild that kept
# running, and kept holding this script's own inherited copy of the
# flock lock fd, for the rest of its sleep duration even after the
# script that spawned it had already fully exited and reported a
# result. That silently blocked every OTHER pending code_exec call for
# up to a full timeout_seconds after a completely successful,
# already-finished run — a real availability bug against the "one
# execution at a time" concurrency design (docs/plans/code-execution.md),
# not just a cosmetic leak. `kill -- -"$pid"` (negative PID) below
# targets the whole process group instead of just the wrapper process.
set -m

INSTALL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SIGNAL_DIR="$INSTALL_DIR/code-exec-signal"
LOCK_FILE="$INSTALL_DIR/.codeexec-watcher.lock"
# CI-built and published, not built on this host — see
# docker/sandbox/Dockerfile's header comment and
# .github/workflows/docker-publish-sandbox.yml. install.sh's Docker path
# pulls this at install time, and update.sh's own "Update Polaris" flow
# re-pulls it on every update too (a plain `docker pull`, since it isn't
# a docker-compose.yml service) — see that script's comment for why a
# failed pull there is non-fatal to the Polaris update itself.
SANDBOX_IMAGE="ghcr.io/autumnsgrove/polaris-sandbox:latest"

cd "$INSTALL_DIR"

# flock -n: belt-and-suspenders alongside Polaris's own package-level
# mutex (tools/code_exec.go's codeExecGlobalLock), which already
# guarantees at most one real request file exists at a time — this only
# guards against this *script* somehow overlapping itself (e.g. a
# leftover request file from a container restart re-triggering the
# path unit while a previous run is still finishing).
exec 200>"$LOCK_FILE"
if ! flock -n 200; then
	echo "codeexec already in progress, skipping this run"
	exit 0
fi

# --- failure handling (issue #203) -----------------------------------------
#
# Everything below runs under `set -e`, so any unexpected failure between
# "a request file was picked up" and "its result file was written" used to
# end the script right there: the request file stayed behind, no result was
# ever written, and Polaris sat out its whole timeout_seconds before
# reporting "is the host-side watcher running?" — indistinguishable from the
# watcher not existing at all, and silent in the journal. The one real
# attempt since the Nova flip (2026-11-05) failed exactly that way.
#
# The EXIT trap makes that failure mode loud and bounded: if a request was
# in flight and no result exists yet, write an explicit error result (Polaris
# polls for it and hands the model the message immediately), remove the
# request file, and log the request's name to the journal. The message is
# built only from fixed strings and numbers, never from request content, so
# it is JSON-safe without python3 (which may itself be the thing that failed).
current_id=""
current_req=""
step=""
container_name=""
script_path=""
run_pid=""
watchdog_pid=""

log() { echo "codeexec: $*" >&2; }

write_error_result() {
	local id="$1" msg="$2"
	msg="${msg//[\"\\]/}"
	printf '{"stdout":"","stderr":"","exit_code":-1,"timed_out":false,"oom_killed":false,"error":"%s"}' \
		"$msg" >"$SIGNAL_DIR/result-${id}.json.tmp" &&
		mv "$SIGNAL_DIR/result-${id}.json.tmp" "$SIGNAL_DIR/result-${id}.json"
}

on_exit() {
	local rc=$?
	trap - EXIT
	if [ -n "$current_req" ]; then
		if [ ! -f "$SIGNAL_DIR/result-${current_id}.json" ]; then
			log "request $(basename "$current_req") abandoned at step '$step' (script exit $rc); writing an error result and removing it"
			if [ "$rc" -eq 143 ]; then
				# SIGTERM: systemd's TimeoutStartSec expiring (the unit's 120s sits right
				# on top of a 120s code_exec timeout), or a restart. Not a script bug, and
				# "failed unexpectedly" would send someone hunting for one.
				write_error_result "$current_id" "the sandbox runner was stopped before it finished (the service timeout TimeoutStartSec, or a restart) at step: $step - see the host journal: journalctl -u polaris-codeexec" || true
			else
				write_error_result "$current_id" "the sandbox runner failed unexpectedly (step: $step, exit $rc) - see the host journal: journalctl -u polaris-codeexec" || true
			fi
		fi
		rm -f "$current_req"
	fi
	# A half-finished run must not leave its container, script, or background
	# jobs behind. The watchdog subshell matters most: it inherits the flock
	# fd, so an orphaned one (still sleeping out timeout_seconds) would hold
	# the lock and make every later run print "already in progress" and skip
	# — the same stranding the `set -m` comment up top describes, reached via
	# a signal instead of a normal exit. Negative PID = its whole process
	# group, courtesy of that same `set -m`.
	[ -n "$watchdog_pid" ] && kill -- -"$watchdog_pid" 2>/dev/null || true
	[ -n "$run_pid" ] && kill -- -"$run_pid" 2>/dev/null || true
	[ -n "$container_name" ] && docker rm -f "$container_name" >/dev/null 2>&1 || true
	[ -n "$script_path" ] && rm -f "$script_path" || true
	exit "$rc"
}
trap on_exit EXIT
# systemd's TimeoutStartSec sends SIGTERM; turn signals into a normal exit so
# on_exit above runs for them too.
trap 'exit 143' TERM INT HUP

# Request/result files this old were abandoned: Polaris gives up on a call
# after timeout_seconds plus a short grace (tools/code_exec.go), and removes
# its own request file when it does. Anything past this is a leftover from a
# crash or restart. Logged by name rather than deleted quietly, so a stuck
# request is visible in the journal.
STALE_MINUTES=15
sweep_stale() {
	local f
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		log "removing stale $(basename "$f") (older than ${STALE_MINUTES} min, abandoned)"
		rm -f "$f"
	done < <(find "$SIGNAL_DIR" -maxdepth 1 \( -name 'requested-*.json*' -o -name 'result-*.json*' \) -mmin "+$STALE_MINUTES" 2>/dev/null || true)
}

# find | sort: if more than one request file exists, process the oldest by
# name (uuids sort arbitrarily, so this is really just "pick one
# deterministically"). The loop at the bottom keeps going until none are
# left, so a request that arrives while another is running is picked up by
# this same invocation instead of depending on the path unit re-firing.
next_request() {
	find "$SIGNAL_DIR" -maxdepth 1 -name 'requested-*.json' 2>/dev/null | sort | head -1 || true
}

process_one() {
req_file="$1"
current_req="$req_file"
step="reading the request"
id="$(basename "$req_file" .json)"
id="${id#requested-}"
current_id="$id"
result_file="$SIGNAL_DIR/result-${id}.json"

# json_get prints one top-level field from the request file — a small
# repeated python3 invocation per field rather than one combined call,
# for simplicity; the ~2s container-start cost this whole call already
# pays dwarfs a few extra python3 startups.
json_get() {
	python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
sys.stdout.write(str(data.get(sys.argv[2], sys.argv[3])))
" "$req_file" "$1" "$2"
}

# `docker run -v workspaces/x:/workspace` treats a path with no leading "/"
# as a *named volume* and fails with exit 125 ("includes invalid characters
# for a local volume name") — so the bare-metal dev config's own
# `host_workspace_dir: "./workspaces"` (config.yaml.example, DEVELOPMENT.md)
# never worked: every code_exec call failed before reaching the sandbox.
# Resolve a relative path against the install dir, which is also the working
# directory a bare-metal Polaris runs from. Absolute paths (Docker/production,
# where it is the real host path) pass through untouched.
abs_path() {
	case "$1" in
		"" | /*) printf '%s' "$1" ;;
		*) printf '%s/%s' "$INSTALL_DIR" "${1#./}" ;;
	esac
}

host_workspace_dir="$(abs_path "$(json_get host_workspace_dir "")")"
# Set only for a thread inside a Field (docs/plans/fields.md) — the
# field's shared file pool, mounted READ-ONLY at /field below.
field_host_workspace_dir="$(abs_path "$(json_get field_host_workspace_dir "")")"
memory_mb="$(json_get memory_limit_mb 384)"
pids_limit="$(json_get pids_limit 64)"
timeout_s="$(json_get timeout_seconds 30)"

if [ -z "$host_workspace_dir" ]; then
	log "requested-${id}.json has no host_workspace_dir, refusing to run it"
	write_error_result "$id" "the sandbox request had no host_workspace_dir, so it was not run"
	rm -f "$req_file"
	current_req=""
	return 0
fi

step="writing the script"
mkdir -p "$host_workspace_dir"
script_name=".code_exec_${id}.py"
script_path="$host_workspace_dir/$script_name"

# Writes the "code" field's raw bytes straight to disk — no shell
# interpolation of the content at any point, see this script's header
# comment on why that matters.
python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
with open(sys.argv[2], 'w') as f:
    f.write(data['code'])
" "$req_file" "$script_path"

step="running the sandbox container"
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
exit_code=0
timed_out=false
oom_killed=false

# --network none/--cap-drop=ALL/--read-only/--tmpfs /tmp/--pids-limit:
# the actual lockdown flags docs/plans/code-execution.md's "Sandbox
# mechanism" section chose over Piston/gVisor — Docker's own normal
# protections (seccomp, capability restrictions, device isolation) stay
# fully active since nothing here runs --privileged. --read-only plus a
# writable /tmp tmpfs and the one explicit workspace bind mount is the
# only writable surface the sandboxed process gets.
#
# python -u: unbuffered stdout/stderr — without it, output redirected
# to a file (not a TTY) is fully buffered, so a script killed by the
# timeout watchdog below can lose everything it already printed if it
# hadn't flushed yet. Confirmed live: a 60s sleep with a print()
# before it produced empty stdout in the timeout result despite the
# print having genuinely executed. formatCodeExecResult's "stdout so
# far" wording (tools/code_exec.go) is only honest with this flag set.
#
# --name + a background `docker kill` watchdog, NOT `timeout
# --kill-after` wrapping `docker run` directly — verified live (not
# hypothesized) that the naive version is a real bug: `timeout` only
# ever signals `docker run`'s own foreground CLI process, which is not
# the same thing as the container the daemon is actually running.
# Killing that CLI process (e.g. via SIGKILL after --kill-after's
# grace period) leaves the container itself running, completely
# undetected, for as long as its own code keeps going — confirmed with
# `docker ps` still showing the container "Up" tens of seconds after
# its supposed timeout had elapsed. That's not just a cosmetic bug: it
# silently defeats both the wall-clock limit itself and the "only one
# execution at a time" concurrency guarantee (docs/plans/
# code-execution.md's "Concurrency") the very next call relies on.
# `docker kill` by name goes through the daemon directly, which is the
# only thing that reliably guarantees the sandboxed process actually
# stops consuming host resources.
container_name="codeexec-${id}"
# mktemp -u: prints a unique path WITHOUT creating the file — plain
# `mktemp` creates it immediately, which made the later `[ -f ]` check
# below true unconditionally from the start regardless of whether the
# watchdog ever actually fired (caught live: a fast OOM kill, well
# under the configured timeout, was still misreported as timed_out).
watchdog_fired_file="$(mktemp -u)"

# The field's shared pool, mounted :ro at /field — the mount itself is
# what makes shared originals immutable from inside the sandbox: a script
# that reads /field/x.csv, edits it, and writes back to the same path
# fails at the filesystem, so the only way to keep a change is to write into
# /workspace (this thread's own directory). No copy-tracking logic needed.
# mkdir -p first, same as the workspace above: a missing bind source would
# otherwise be auto-created by the Docker daemon as root. An array (not a
# plain string) so the path survives spaces; the ${arr[@]+...} form is what
# makes an EMPTY array safe under `set -u` on bash < 4.4 (macOS's stock
# bash 3.2 errors on a bare "${arr[@]}" there).
field_mount_args=()
if [ -n "$field_host_workspace_dir" ]; then
	mkdir -p "$field_host_workspace_dir"
	field_mount_args=(-v "$field_host_workspace_dir:/field:ro")
fi

docker run --rm --name "$container_name" \
	--network none \
	--memory="${memory_mb}m" \
	--cpus=1 \
	--pids-limit="${pids_limit}" \
	--cap-drop=ALL \
	--security-opt=no-new-privileges \
	--read-only \
	--tmpfs /tmp \
	-v "$host_workspace_dir:/workspace" \
	${field_mount_args[@]+"${field_mount_args[@]}"} \
	-w /workspace \
	"$SANDBOX_IMAGE" \
	python -u "$script_name" \
	>"$stdout_file" 2>"$stderr_file" &
run_pid=$!

(
	sleep "$timeout_s"
	if docker kill "$container_name" >/dev/null 2>&1; then
		touch "$watchdog_fired_file"
	fi
) &
watchdog_pid=$!

# `|| exit_code=$?`, not a bare `wait` then `exit_code=$?` on the next
# line — under `set -e`, `wait` returning the job's own nonzero exit
# status is a command failure like any other and kills the whole
# script right here, before exit_code is ever assigned or the result
# file is written. Caught live: a sandbox timeout correctly killed the
# container, but the script itself then died silently, leaving Polaris
# to time out its own poll loop and report a generic "no result"
# error instead of the real, already-known timeout outcome.
exit_code=0
wait "$run_pid" || exit_code=$?

step="collecting the result"
# The watchdog either already fired (container gone, `docker kill`
# above no-ops harmlessly on the next check) or is still sleeping and
# no longer needed — either way, safe to tear down unconditionally.
# `|| true` on both: killing/waiting on a process group that already
# exited on its own is an expected outcome, not an error.
#
# `-- -"$watchdog_pid"` (negative PID), not a plain
# `kill "$watchdog_pid"` — see this script's `set -m` comment up top:
# a bare kill only signals the subshell wrapper, not the `sleep` it's
# still blocked in, which then survives as an orphan holding this
# script's own inherited flock fd open. Negating the PID targets the
# whole process group `set -m` gave this background job instead.
kill -- -"$watchdog_pid" 2>/dev/null || true
wait "$watchdog_pid" 2>/dev/null || true

if [ -f "$watchdog_fired_file" ]; then
	timed_out=true
elif [ "$exit_code" -eq 137 ]; then
	# No longer ambiguous the way a shared `timeout --kill-after` exit
	# code would be: the watchdog above didn't fire, so a bare SIGKILL
	# exit here can only be Docker's own OOM killer.
	oom_killed=true
fi
rm -f "$watchdog_fired_file"

# Backstop: --rm should have already removed the container the instant
# it exited, but a leftover (e.g. a daemon hiccup right at the
# kill/exit race) would collide with the next call's `docker run
# --name` on this same id — never actually happens since ids are
# per-call UUIDs, but a stale container from a genuinely wedged prior
# run is exactly the scenario the live bug above was already hiding.
docker rm -f "$container_name" >/dev/null 2>&1 || true

rm -f "$script_path"

# Builds the result JSON from the raw stdout/stderr file bytes via
# python3's own json.dumps, not shell string interpolation — same
# "never hand-escape untrusted bytes into JSON" reasoning as the code
# file write above. The executed script's own stdout/stderr can contain
# absolutely anything (unicode, control characters, embedded quotes).
python3 -c "
import json, sys
with open(sys.argv[1], 'rb') as f:
    stdout = f.read().decode('utf-8', errors='replace')
with open(sys.argv[2], 'rb') as f:
    stderr = f.read().decode('utf-8', errors='replace')
result = {
    'stdout': stdout,
    'stderr': stderr,
    'exit_code': int(sys.argv[3]),
    'timed_out': sys.argv[4] == 'true',
    'oom_killed': sys.argv[5] == 'true',
    'error': '',
}
with open(sys.argv[6], 'w') as f:
    json.dump(result, f)
" "$stdout_file" "$stderr_file" "$exit_code" "$timed_out" "$oom_killed" "$result_file.tmp"
mv "$result_file.tmp" "$result_file"

rm -f "$stdout_file" "$stderr_file" "$req_file"
current_req=""
container_name=""
script_path=""
run_pid=""
watchdog_pid=""
}

# Cap so a request that somehow regenerates itself can't spin this forever.
processed=0
while [ "$processed" -lt 50 ]; do
	sweep_stale
	pending="$(next_request)"
	[ -n "$pending" ] || break
	process_one "$pending"
	processed=$((processed + 1))
done
exit 0
