#!/usr/bin/env bash
#
# Runs on the host (not inside any container) as a systemd oneshot
# service, triggered by polaris-update.path the instant
# update-signal/requested appears, or by polaris-update.timer every
# hour as a backstop against a missed inotify event. Never invoked by
# the polaris container itself — see docker-compose.yml's comment on
# why Docker control deliberately stays on this side of the container
# boundary, not mounted into the one process that runs model-directed
# outbound requests.
#
# update-signal/requested holds the exact image reference (by digest,
# not a floating tag) the settings panel's "Update Polaris" button
# showed the user. Pulling that exact reference — by rewriting
# POLARIS_IMAGE in .env, which docker-compose.yml's `image:` field
# interpolates — instead of re-resolving whatever :latest happens to
# mean at pull time closes the gap where a second commit landing
# between the click and this script running could silently ship
# something the user never saw in the release notes.
set -euo pipefail

# This script lives at <install_dir>/compose/watcher/update.sh, two
# levels below the install root where .env/update-signal/ actually are.
INSTALL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="$INSTALL_DIR/.env"
SIGNAL_DIR="$INSTALL_DIR/update-signal"
LOCK_FILE="$INSTALL_DIR/.update-watcher.lock"
REQUESTED_FILE="$SIGNAL_DIR/requested"
RESULT_FILE="$SIGNAL_DIR/result"
# Reused across every risky command below (git pull, docker compose
# pull/up) — each writes here via `tee` so the real output still streams
# live to the journal exactly as before, while also being available for
# a failure's write_result detail instead of a fixed generic string.
# Overwritten per-command, not appended: only the most recent attempt's
# output is ever relevant to the result this run is about to write.
CMD_LOG="$SIGNAL_DIR/.last-command.log"

cd "$INSTALL_DIR"

# json_escape backslash-escapes backslashes and double-quotes, the only
# two characters that would otherwise break a value out of a JSON
# string — everything this script feeds it (a status word, our own
# detail messages, an image digest reference) is program-controlled and
# newline-free, so this doesn't need to handle the full JSON escape
# table (control chars, unicode) the way a general-purpose serializer
# would. printf %q was tried here first and is wrong for this: it does
# bash quoting (backslash-escaped spaces, no surrounding quotes), not
# JSON string escaping.
json_escape() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\n'
}

# truncate_detail keeps write_result's JSON detail field readable — full
# docker compose pull/up output can run to dozens of per-layer progress
# lines, and only the last few ever contain the actual error. 400 chars
# comfortably fits a real error message without bloating
# update-signal/result or the settings panel's failure text.
truncate_detail() {
	local text="$1" max=400
	if [ "${#text}" -gt "$max" ]; then
		printf '...%s' "${text: -$max}"
	else
		printf '%s' "$text"
	fi
}

write_result() {
	# status: "ok" | "failed" | "skipped". Read by gateway/update.go's
	# Docker-mode status poll (mirrors updateStatus today) so the
	# settings panel can show the same spinner->success/failure UX it
	# already does for the bare-metal update path.
	local status="$1" detail="$2"
	printf '{"status":"%s","detail":"%s","target":"%s","finished_at":"%s"}\n' \
		"$(json_escape "$status")" "$(json_escape "$detail")" \
		"$(json_escape "${TARGET_IMAGE:-}")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		>"$RESULT_FILE.tmp"
	mv "$RESULT_FILE.tmp" "$RESULT_FILE"
}

# Sets POLARIS_IMAGE=<value> in .env, replacing any existing line —
# used both to pin the new target before pulling and to roll back to
# the previous image if the new one fails.
set_pinned_image() {
	local value="$1"
	if grep -q '^POLARIS_IMAGE=' "$ENV_FILE" 2>/dev/null; then
		sed -i.bak "s|^POLARIS_IMAGE=.*|POLARIS_IMAGE=$value|" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
	else
		printf 'POLARIS_IMAGE=%s\n' "$value" >>"$ENV_FILE"
	fi
}

current_pinned_image() {
	# `|| true`: no POLARIS_IMAGE line yet (the very first update after
	# install, before this script has ever pinned anything) is a normal,
	# expected case, not a failure — without this, `pipefail` plus
	# `set -e` would kill the whole script right here on a bare grep
	# miss, since this runs as a plain assignment's command substitution.
	grep '^POLARIS_IMAGE=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true
}

# rollback_container re-pins .env to the previous image and tries to get
# a running container back — called from every failure path below once
# the new image has actually been touched. Returns non-zero (via the
# caller checking $?) if the rollback attempt *itself* also failed, so
# callers can tell "update failed, old version still fine" apart from
# "update failed AND the rollback failed too — polaris may be down
# right now", which used to be silently indistinguishable (the rollback
# `up` was fire-and-forget via `|| true`).
rollback_container() {
	set_pinned_image "$PREVIOUS_IMAGE"
	if timeout 60 docker compose up -d --force-recreate --no-deps polaris 2>&1 | tee "$CMD_LOG"; then
		return 0
	fi
	return 1
}

# flock -n: a second trigger arriving while an update is already in
# flight (a stray timer tick right after a manual click, say) should
# just no-op, not queue up behind the first one and run twice.
exec 200>"$LOCK_FILE"
if ! flock -n 200; then
	echo "update already in progress, skipping this run"
	exit 0
fi

if [ ! -f "$REQUESTED_FILE" ]; then
	# The timer's backstop tick with nothing pending — normal, not an error.
	exit 0
fi

TARGET_IMAGE="$(cat "$REQUESTED_FILE")"
if [ -z "$TARGET_IMAGE" ]; then
	echo "update-signal/requested is empty, ignoring" >&2
	rm -f "$REQUESTED_FILE"
	exit 0
fi

# Sanity-check the shape before it ever reaches .env or docker compose.
# This value is always resolved server-side (gateway/docker_update.go's
# GHCR digest lookup), never typed by a person, so a malformed value
# here means a bug upstream, not an operator mistake — failing fast
# with a clear message beats discovering it three steps later as an
# opaque docker compose error, or worse, silently writing garbage into
# .env.
if ! [[ "$TARGET_IMAGE" =~ ^[A-Za-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]; then
	write_result "failed" "update-signal/requested doesn't look like a real image digest reference, refusing to use it: $TARGET_IMAGE"
	rm -f "$REQUESTED_FILE"
	exit 1
fi

PREVIOUS_IMAGE="$(current_pinned_image)"
echo "updating polaris: $PREVIOUS_IMAGE -> $TARGET_IMAGE"

# Defensive: a previous run that hit a real git merge conflict (see
# below) could have left the checkout mid-merge, which would make every
# run after it fail identically forever — a fresh update attempt has no
# way to know that's what's wrong. Safe to abort unconditionally here:
# MERGE_HEAD only exists before a merge commit is actually made, so this
# can't discard any committed work, at worst it discards an
# already-broken merge attempt that never should have been left lying
# around in the first place.
if [ -f "$INSTALL_DIR/.git/MERGE_HEAD" ]; then
	echo "found a leftover merge in progress from a previous run — aborting it before continuing" >&2
	git merge --abort || true
fi

# Sync the host-side checkout before touching the container — same
# `git pull origin main` the bare-metal path runs (updater.go's Run),
# so both deployment models trust identical git semantics rather than
# Docker inventing its own stricter (and, tested live, not actually
# safer) rule. The image alone isn't the whole deployment:
# docker-compose.yml's env passthrough list, the bind-mounted config
# templates, and this very script all live in this checkout, and
# previously only advanced whenever someone happened to `git pull`
# manually — potentially days after TARGET_IMAGE had already moved.
# Caught live: a PR added PARALLEL_API_KEY to both the image and
# docker-compose.yml's passthrough list; the image update alone left
# the container running the new code with the key silently missing
# from its environment until docker-compose.yml itself was pulled by
# hand.
#
# timeout 60: a stalled connection to the git remote used to just hang
# here forever, holding the flock and silently no-op-ing every future
# trigger (manual click or the hourly backstop) with no explanation.
#
# --no-rebase pins the reconciliation strategy explicitly rather than
# relying on the host's ambient pull.rebase/pull.ff git config, which
# the potato (and this script's own test sandbox) don't set. Verified
# live: on a modern git (2.47+) with neither configured, a genuinely
# divergent checkout doesn't even attempt a merge — it hard-fails with
# "Need to specify how to reconcile divergent branches" instead, which
# would have skipped right past the MERGE_HEAD cleanup below (there's
# no merge in progress to abort) and looked like an ordinary transient
# failure instead of the same "wedged until someone SSHes in and fixes
# git by hand" problem this whole section exists to avoid.
if ! timeout 60 git pull --no-rebase origin main 2>&1 | tee "$CMD_LOG"; then
	# A real conflict (not just a network failure) leaves .git mid-merge
	# — clean that up now so the *next* run starts fresh instead of
	# failing the exact same way forever. See the MERGE_HEAD check above
	# for why this is safe.
	if [ -f "$INSTALL_DIR/.git/MERGE_HEAD" ]; then
		git merge --abort || true
	fi
	write_result "failed" "git pull origin main failed: $(truncate_detail "$(cat "$CMD_LOG")")"
	rm -f "$REQUESTED_FILE"
	exit 1
fi

set_pinned_image "$TARGET_IMAGE"

# Scoped to the polaris service specifically (not a bare `docker
# compose pull`/`up -d`) so an update can never touch, restart, or
# otherwise disturb the searxng container as a side effect.
#
# timeout 300: an image pull over a slow or stalled connection used to
# just hang indefinitely, same reasoning as the git pull timeout above
# — 5 minutes is generous for even a large layer set on modest
# bandwidth, without letting a genuinely stuck connection block updates
# forever.
if ! timeout 300 docker compose pull polaris 2>&1 | tee "$CMD_LOG"; then
	write_result "failed" "docker compose pull failed: $(truncate_detail "$(cat "$CMD_LOG")")"
	# Always roll back — even when PREVIOUS_IMAGE is empty (the very
	# first update attempt, nothing pinned yet). An earlier version of
	# this only rolled back when PREVIOUS_IMAGE was non-empty, which on
	# a first-ever failed attempt left .env pinned to the *broken*
	# target instead of back to "nothing pinned" (which falls through
	# to docker-compose.yml's :latest default) — strictly worse than
	# doing nothing, since a later `docker compose up` would then try
	# to start from that same broken reference. Caught by actually
	# testing this exact scenario live against the potato. Nothing to
	# actually restart here — pull failing means the running container
	# was never touched — so just re-pin .env, no rollback_container call.
	set_pinned_image "$PREVIOUS_IMAGE"
	rm -f "$REQUESTED_FILE"
	exit 1
fi

# --force-recreate: without it, `docker compose up -d` is a no-op
# whenever the desired image/config already matches what's running —
# exactly the case handleDockerRestart (gateway/docker_update.go)
# deliberately creates, since a plain restart pins TARGET_IMAGE to
# whatever's *already* running. Confirmed live: a restart click pulled
# nothing new (correctly — same image), then `up -d` printed "Container
# polaris-polaris-1 Running" and genuinely did nothing, leaving the old
# process running forever while the settings panel polled /api/version
# for a change that could never come. --force-recreate makes both
# "restart, same image" and "update, new image" actually cycle the
# container either way, with the same one code path for both.
if ! timeout 60 docker compose up -d --force-recreate --no-deps polaris 2>&1 | tee "$CMD_LOG"; then
	write_result "failed" "docker compose up failed: $(truncate_detail "$(cat "$CMD_LOG")")"
	if ! rollback_container; then
		# The rollback attempt failed too — this is not an ordinary
		# "update failed, old version still fine" outcome, the service
		# may genuinely be down right now. Write a second, distinctly
		# worded result so that difference is actually visible instead
		# of silently discarded (the old `|| true` on this exact retry).
		write_result "failed" "update failed AND rollback to the previous image also failed — polaris may be down right now: $(truncate_detail "$(cat "$CMD_LOG")")"
	fi
	rm -f "$REQUESTED_FILE"
	exit 1
fi

# `docker compose up -d` returning success only means the container was
# told to start, not that it's actually serving traffic — the
# Dockerfile's own HEALTHCHECK (wget against /healthz) is what actually
# confirms that. Without waiting for it, a bad image (a config-parsing
# bug, a broken migration, a crash loop) could report "ok" here while
# the service is really unusable moments later — the exact class of
# failure this status exists to catch. Polls up to 120s: the
# healthcheck's own worst-case time to a definitive "unhealthy" is
# start_period (10s) + retries (3) * interval (30s) = 100s, so 120s
# leaves a small margin without letting a genuinely broken deploy hang
# the settings panel's spinner indefinitely.
CONTAINER_ID="$(docker compose ps -q polaris)"
HEALTH="unknown"
for _ in $(seq 1 40); do
	HEALTH="$(docker inspect --format '{{.State.Health.Status}}' "$CONTAINER_ID" 2>/dev/null || echo "unknown")"
	if [ "$HEALTH" = "healthy" ] || [ "$HEALTH" = "unhealthy" ]; then
		break
	fi
	sleep 3
done

if [ "$HEALTH" != "healthy" ]; then
	# The container's own recent log output (not the `up` command's,
	# which only ever shows "Container ... Started" and nothing about
	# what happened after) is what actually explains an unhealthy
	# status — a crash-on-boot, a config parse error, whatever it is.
	FAIL_LOG="$(docker compose logs --no-color --tail=30 polaris 2>&1 || true)"
	write_result "failed" "new container started but never reported healthy (status: $HEALTH): $(truncate_detail "$FAIL_LOG")"
	if ! rollback_container; then
		write_result "failed" "new image failed its healthcheck AND rollback to the previous image also failed — polaris may be down right now: $(truncate_detail "$FAIL_LOG")"
	fi
	rm -f "$REQUESTED_FILE"
	exit 1
fi

write_result "ok" "updated to $TARGET_IMAGE"
rm -f "$REQUESTED_FILE"
