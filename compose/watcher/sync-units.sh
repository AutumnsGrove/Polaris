#!/usr/bin/env bash
#
# Re-syncs the watcher's own systemd unit files from this checkout into
# /etc/systemd/system/ whenever they've drifted, so a fix or feature
# landed in compose/watcher/*.service|*.path|*.timer actually reaches an
# already-installed host through the normal `polaris update` flow
# instead of requiring a fresh install.sh run or a manual SSH copy —
# see issue #85. This is deliberately its own tiny, fixed-purpose
# script rather than folding the logic into update.sh directly:
# update.sh runs unprivileged (see polaris-update.service's own comment
# on why), and the whole point of install.sh's generated sudoers rule
# granting NOPASSWD access to exactly this one script's absolute path,
# rather than to `cp`/`systemctl` directly, is keeping root's reachable
# surface as narrow and auditable as this one file.
#
# Always invoked via `sudo` (update.sh does so once per run, right
# after a successful `git pull`), so this itself executes as root —
# everything it touches (/etc/systemd/system, daemon-reload, restarting
# units) needs that.
set -euo pipefail

INSTALL_DIR="$1"
DEPLOY_USER="$2"
WATCHER_SRC="$INSTALL_DIR/compose/watcher"
UNIT_DIR="/etc/systemd/system"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

changed=()
# Same fixed unit list and @INSTALL_DIR@/@USER@ templating install.sh
# uses at install time — kept in sync by hand since both are short and
# rarely change; see install.sh's own copy of this list.
for unit in polaris-update.service polaris-update.path polaris-update.timer \
	polaris-codeexec.service polaris-codeexec.path; do
	sed -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" -e "s|@USER@|$DEPLOY_USER|g" \
		"$WATCHER_SRC/$unit" >"$tmp_dir/$unit"
	if ! cmp -s "$tmp_dir/$unit" "$UNIT_DIR/$unit" 2>/dev/null; then
		cp "$tmp_dir/$unit" "$UNIT_DIR/$unit"
		changed+=("$unit")
	fi
done

if [ "${#changed[@]}" -eq 0 ]; then
	exit 0
fi

echo "watcher unit files changed, re-syncing: ${changed[*]}"
systemctl daemon-reload

# .service units here are oneshots that aren't persistently running —
# daemon-reload alone is enough, since the next trigger reads the
# just-updated file straight off disk. .path/.timer units, by
# contrast, stay active/waiting continuously and need an explicit
# restart to pick up a changed condition path or interval; restarting
# is safe even mid-wait, since neither unit has any in-progress state
# of its own to lose (that all lives in the .service units they
# trigger).
for unit in "${changed[@]}"; do
	case "$unit" in
	*.path | *.timer)
		systemctl restart "$unit"
		;;
	esac
done
