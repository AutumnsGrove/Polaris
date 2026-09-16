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

# find | sort: if more than one request file somehow exists (shouldn't
# happen — see the mutex reasoning above), process the oldest by name
# (uuids sort arbitrarily, so this is really just "pick one
# deterministically") and leave the rest for the next
# DirectoryNotEmpty trigger rather than guessing which is current.
req_file="$(find "$SIGNAL_DIR" -maxdepth 1 -name 'requested-*.json' 2>/dev/null | sort | head -1 || true)"
if [ -z "$req_file" ]; then
	# The path unit's own re-check after this script exits, or a stray
	# trigger with nothing pending — normal, not an error.
	exit 0
fi

id="$(basename "$req_file" .json)"
id="${id#requested-}"
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

host_workspace_dir="$(json_get host_workspace_dir "")"
memory_mb="$(json_get memory_limit_mb 384)"
pids_limit="$(json_get pids_limit 64)"
timeout_s="$(json_get timeout_seconds 30)"

if [ -z "$host_workspace_dir" ]; then
	echo "requested-${id}.json has no host_workspace_dir, refusing to run it" >&2
	rm -f "$req_file"
	exit 1
fi

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
