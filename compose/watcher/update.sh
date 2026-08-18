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
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
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

PREVIOUS_IMAGE="$(current_pinned_image)"
echo "updating polaris: $PREVIOUS_IMAGE -> $TARGET_IMAGE"

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
if ! git pull origin main --quiet; then
	write_result "failed" "git pull origin main failed, aborting before touching the container"
	rm -f "$REQUESTED_FILE"
	exit 1
fi

set_pinned_image "$TARGET_IMAGE"

# Scoped to the polaris service specifically (not a bare `docker
# compose pull`/`up -d`) so an update can never touch, restart, or
# otherwise disturb the searxng container as a side effect.
if ! docker compose pull polaris; then
	write_result "failed" "docker compose pull failed for $TARGET_IMAGE"
	# Always roll back — even when PREVIOUS_IMAGE is empty (the very
	# first update attempt, nothing pinned yet). An earlier version of
	# this only rolled back when PREVIOUS_IMAGE was non-empty, which on
	# a first-ever failed attempt left .env pinned to the *broken*
	# target instead of back to "nothing pinned" (which falls through
	# to docker-compose.yml's :latest default) — strictly worse than
	# doing nothing, since a later `docker compose up` would then try
	# to start from that same broken reference. Caught by actually
	# testing this exact scenario live against the potato.
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
if ! docker compose up -d --force-recreate --no-deps polaris; then
	write_result "failed" "docker compose up failed for $TARGET_IMAGE"
	set_pinned_image "$PREVIOUS_IMAGE"
	docker compose up -d --force-recreate --no-deps polaris || true
	rm -f "$REQUESTED_FILE"
	exit 1
fi

write_result "ok" "updated to $TARGET_IMAGE"
rm -f "$REQUESTED_FILE"
