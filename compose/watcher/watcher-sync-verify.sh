#!/usr/bin/env bash
#
# Root-side gatekeeper for sync-units.sh. This wrapper — NOT
# sync-units.sh itself — is what install.sh's sudoers rule actually
# grants NOPASSWD access to, and install.sh copies it to a fixed,
# root-owned path outside the git checkout (/etc/polaris/
# watcher-sync-verify.sh) that update.sh's automated git-pull flow
# never touches.
#
# Why this exists: sync-units.sh runs as root (it writes into
# /etc/systemd/system and calls systemctl/daemon-reload). If the
# sudoers rule pointed directly at sync-units.sh's path inside the
# checkout, ANY commit that reaches main — including a malicious one
# from a compromised contributor account or token, not just a
# deliberate change — would get root-executed on every production host
# the next time `polaris update` runs, with zero human review. That's a
# real supply-chain-to-privilege-escalation path, not a hypothetical
# one, and it's strictly new: before sync-units.sh existed, a malicious
# commit could only ever affect what runs *inside* Polaris's own
# container (deliberately walled off from Docker/root — no socket
# mount, see gateway/docker_update.go), never the host directly.
#
# The fix: only actually run sync-units.sh if its current content's
# SHA-256 still matches the hash install.sh pinned into
# /etc/polaris/watcher-sync.sha256 the last time a human ran it. A
# malicious (or even a legitimate but unreviewed) commit to
# sync-units.sh changes that hash, so this refuses to run it — the
# automated update flow just degrades to "the watcher units stay as
# they are" instead of "run whatever main currently contains as root".
# Moving the pin forward requires an actual human at a terminal running
# install.sh (who can review the diff first) — something no amount of
# git pushes can trigger by itself, since update.sh never invokes
# install.sh on its own.
set -euo pipefail

INSTALL_DIR="$1"
SCRIPT="$INSTALL_DIR/compose/watcher/sync-units.sh"
PINNED_HASH_FILE="/etc/polaris/watcher-sync.sha256"

if [ ! -f "$SCRIPT" ]; then
	echo "sync-units.sh not found at $SCRIPT — nothing to verify or run" >&2
	exit 1
fi
if [ ! -f "$PINNED_HASH_FILE" ]; then
	echo "no pinned hash at $PINNED_HASH_FILE — run install.sh to approve sync-units.sh first" >&2
	exit 1
fi

actual_hash="$(sha256sum "$SCRIPT" | awk '{print $1}')"
pinned_hash="$(cat "$PINNED_HASH_FILE")"

if [ "$actual_hash" != "$pinned_hash" ]; then
	echo "compose/watcher/sync-units.sh has changed since it was last approved" >&2
	echo "by install.sh (pinned: $pinned_hash, current: $actual_hash) — refusing" >&2
	echo "to run it as root. Review the change, then re-run install.sh to" >&2
	echo "re-approve and update the pin." >&2
	exit 1
fi

# $@ still holds this wrapper's own original args (INSTALL_DIR,
# DEPLOY_USER) — sync-units.sh takes the exact same two positional
# args, so they pass straight through unchanged.
exec "$SCRIPT" "$@"
