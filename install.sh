#!/usr/bin/env bash
#
# Polaris one-shot installer.
#
#   curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
#
# Two install modes, chosen via POLARIS_INSTALL_MODE (default
# "bare-metal", matching this script's original behavior):
#
#   bare-metal (default) — clones the repo, builds the polaris binary
#     with the local Go toolchain, brings up a standalone SearXNG dev
#     container via Docker (installing Docker itself if it's missing),
#     copies config.yaml.example to config.yaml, and opens it for you
#     to drop in an OpenRouter key.
#
#   docker — clones the repo, ensures Docker + the Compose plugin are
#     present, copies .env.example/compose/polaris/config.yaml.example
#     to their real counterparts, and (Linux only) installs the host
#     update watcher's systemd units (see compose/watcher/) so the
#     settings panel's "Update Polaris" button works. Does NOT run
#     `docker compose up` itself — same "go fill in the config first"
#     philosophy as the bare-metal path below.
#
#     POLARIS_INSTALL_MODE=docker curl -fsSL .../install.sh | bash
#
# Safe to re-run in either mode — every step checks what's already
# there before doing anything.
#
# What neither mode does: start the Polaris server itself. The config
# needs a real API key first, so the last step is always "go fill this
# in", not "silently start a half-configured server in the background."
set -euo pipefail

REPO_URL="https://github.com/AutumnsGrove/Polaris.git"
INSTALL_DIR="${POLARIS_INSTALL_DIR:-$HOME/Polaris}"
SEARXNG_PORT="${POLARIS_SEARXNG_PORT:-18888}"
SEARXNG_CONTAINER="${POLARIS_SEARXNG_CONTAINER:-searxng-dev}"
INSTALL_MODE="${POLARIS_INSTALL_MODE:-bare-metal}"

# Set to 1 after a fresh Docker Engine install on Linux, where the
# current shell session doesn't have the new `docker` group membership
# yet (that needs a re-login) — every docker invocation for the rest of
# THIS run goes through sudo instead.
DOCKER_NEEDS_SUDO=0

# ---- output helpers ---------------------------------------------------

step() { printf '\n\033[1;33m==>\033[0m \033[1m%s\033[0m\n' "$1"; }
info() { printf '    %s\n' "$1"; }
warn() { printf '    \033[1;31m! %s\033[0m\n' "$1"; }

docker_cmd() {
	if [ "$DOCKER_NEEDS_SUDO" = 1 ]; then
		sudo docker "$@"
	else
		docker "$@"
	fi
}

# ask_yes_no prompts $1 and returns 0 for yes, 1 for no (including every
# non-interactive case) — the only interactive prompt anywhere in this
# script, so it earns its own helper rather than being inlined once.
# Reads from /dev/tty explicitly, not plain stdin: this script's primary
# documented invocation is `curl -fsSL ... | bash`, which feeds the
# script's own bytes into stdin — a bare `read -p` there either hangs or
# silently reads empty input, never the actual person running it. /dev/tty
# is the real controlling terminal even under that pipe, as long as one
# is actually attached (an interactive shell session); falls through to
# the default "no" when it isn't (CI, cron, any other genuinely
# non-interactive context) rather than hanging forever waiting on input
# that will never come.
ask_yes_no() {
	local prompt="$1"
	if [ ! -r /dev/tty ]; then
		return 1
	fi
	local reply
	read -r -p "    $prompt [y/N] " reply 2>/dev/null </dev/tty || return 1
	case "$reply" in
		[yY] | [yY][eE][sS]) return 0 ;;
		*) return 1 ;;
	esac
}

# with_timeout runs a command bounded to $1 seconds if the `timeout`
# binary is available, otherwise runs it unbounded — `timeout` is GNU
# coreutils, present by default on Linux but NOT on stock macOS (unlike
# the potato, which this script's Linux path targets, macOS here is a
# dev machine that may have neither Homebrew nor coreutils installed).
# Degrading to "no timeout" rather than failing outright keeps a fresh
# macOS install working exactly as before; a slow/stalled network step
# is still better than a script that refuses to even start.
with_timeout() {
	local seconds="$1"
	shift
	if command -v timeout >/dev/null 2>&1; then
		timeout "$seconds" "$@"
	else
		"$@"
	fi
}

# ---- -1. install mode check ---------------------------------------------

case "$INSTALL_MODE" in
	bare-metal | docker) ;;
	*)
		warn "Unknown POLARIS_INSTALL_MODE: \"$INSTALL_MODE\" (expected \"bare-metal\" or \"docker\")"
		exit 1
		;;
esac

# ---- 0. platform check --------------------------------------------------

OS="$(uname -s)"
case "$OS" in
	Darwin | Linux) ;;
	*)
		warn "Unsupported OS: $OS. This script supports macOS and Linux."
		warn "See the README for manual setup: https://github.com/AutumnsGrove/Polaris"
		exit 1
		;;
esac

# ---- 1. prerequisites: git, (go for bare-metal only) ---------------------

step "Checking prerequisites"

if ! command -v git >/dev/null 2>&1; then
	warn "git is required and wasn't found."
	if [ "$OS" = "Darwin" ]; then
		warn "Install it via Xcode Command Line Tools: xcode-select --install"
	else
		warn "Install it via your package manager (e.g. apt install git) and re-run."
	fi
	exit 1
fi
info "git: $(git --version)"

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	if ! command -v go >/dev/null 2>&1; then
		warn "Go is required and wasn't found."
		warn "Install it from https://go.dev/dl/ and re-run this script."
		exit 1
	fi
	info "go: $(go version)"
else
	info "Docker mode — no local Go toolchain needed (the image builds its own)."
fi

# ---- 2. clone or update the repo ---------------------------------------

step "Fetching Polaris into $INSTALL_DIR"

if [ -d "$INSTALL_DIR/.git" ]; then
	info "Already cloned — pulling latest instead of re-cloning."
	# --ff-only can never leave a stuck merge (it refuses outright instead
	# of attempting one on any real divergence), but that refusal's own
	# error message gives no hint what to actually do about it — add one
	# here rather than letting `set -e` exit on git's raw stderr alone.
	if ! with_timeout 60 git -C "$INSTALL_DIR" pull --ff-only; then
		warn "Couldn't fast-forward $INSTALL_DIR to the latest commit — it has"
		warn "local changes or commits that don't match origin/main. Either"
		warn "commit/stash whatever's there, or move $INSTALL_DIR aside and"
		warn "re-run this script for a clean checkout."
		exit 1
	fi
elif [ -e "$INSTALL_DIR" ]; then
	warn "$INSTALL_DIR exists and isn't a git checkout. Move it aside or set"
	warn "POLARIS_INSTALL_DIR to a different path, then re-run."
	exit 1
else
	# timeout 60: a stalled connection used to just hang here forever with
	# no feedback — a curl-piped install looks frozen with nothing to Ctrl-C
	# toward. Same reasoning as compose/watcher/update.sh's own git timeout.
	if ! with_timeout 60 git clone "$REPO_URL" "$INSTALL_DIR"; then
		warn "Cloning $REPO_URL timed out or failed — check your network"
		warn "connection and re-run this script."
		exit 1
	fi
fi

cd "$INSTALL_DIR"

# Auto-rebuilds web/build/ on every commit (see README) — a no-op for
# anyone who's just running Polaris, but harmless either way and needed
# if this checkout is ever committed to.
git config core.hooksPath .githooks

# ---- 3. build the binary (bare-metal only) -------------------------------

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	step "Building the polaris binary"

	go build -o polaris .
	info "Built ./polaris"
fi

# ---- 4. Docker ------------------------------------------------------------
#
# Needed in both modes: bare-metal uses it for the standalone SearXNG
# dev container below; docker mode uses it for the whole compose stack.

step "Setting up Docker"

if ! command -v docker >/dev/null 2>&1; then
	info "Docker not found — installing it."
	if [ "$OS" = "Darwin" ]; then
		if command -v brew >/dev/null 2>&1; then
			brew install --cask docker
			info "Docker Desktop installed. Launching it now — first launch may ask you"
			info "to grant it permissions."
		else
			warn "Homebrew isn't installed, so Docker can't be installed automatically."
			warn "Install Docker Desktop yourself: https://www.docker.com/products/docker-desktop"
			warn "Then re-run this script — everything else will pick up where it left off."
			exit 1
		fi
	else
		info "Installing Docker Engine via Docker's official install script (needs sudo)."
		curl -fsSL https://get.docker.com | sh
		DOCKER_NEEDS_SUDO=1
		if sudo usermod -aG docker "$USER"; then
			info "Added $USER to the docker group — log out and back in for passwordless"
			info "'docker' access afterward. Using sudo for the rest of this run."
		else
			# Doesn't block the install — DOCKER_NEEDS_SUDO=1 already makes
			# this run correct either way — but silently swallowing this used
			# to leave the info message above claiming passwordless access
			# would work later when it actually wouldn't, with no indication
			# why the next plain `docker` command outside this script still
			# needed sudo.
			warn "Couldn't add $USER to the docker group — you'll need to run"
			warn "'docker' commands with sudo, or add yourself to the group"
			warn "manually: sudo usermod -aG docker \$USER"
		fi
	fi
else
	info "Docker is already installed."
fi

# Make sure the daemon is actually running, not just the CLI installed.
if [ "$OS" = "Darwin" ]; then
	if ! docker info >/dev/null 2>&1; then
		info "Starting Docker Desktop…"
		open -a Docker
		for _ in $(seq 1 30); do
			docker info >/dev/null 2>&1 && break
			sleep 2
		done
		if ! docker info >/dev/null 2>&1; then
			warn "Docker Desktop didn't come up in time. Open it manually, wait for it"
			warn "to finish starting, then re-run this script."
			exit 1
		fi
	fi
else
	if ! docker_cmd info >/dev/null 2>&1; then
		info "Starting the Docker service…"
		sudo systemctl enable --now docker
		# Unlike the macOS branch above, this used to assume success and
		# fall straight through to "Docker daemon is up" — if the service
		# failed to actually start (masked unit, resource issue, whatever),
		# the first real symptom was an unrelated-looking failure several
		# steps later (docker compose version, or the searxng container
		# setup) instead of a clear message about the actual problem.
		for _ in $(seq 1 30); do
			docker_cmd info >/dev/null 2>&1 && break
			sleep 2
		done
		if ! docker_cmd info >/dev/null 2>&1; then
			warn "Docker service didn't come up after 'systemctl enable --now"
			warn "docker'. Check its status yourself (systemctl status docker),"
			warn "fix whatever's wrong, then re-run this script."
			exit 1
		fi
	fi
fi
info "Docker daemon is up."

if [ "$INSTALL_MODE" = "docker" ]; then
	if ! docker_cmd compose version >/dev/null 2>&1; then
		warn "docker compose (the v2 plugin) wasn't found even though Docker is installed."
		warn "Docker Desktop and the get.docker.com script both bundle it — if this is a"
		warn "custom Docker install, add the compose plugin and re-run."
		exit 1
	fi
	info "docker compose: $(docker_cmd compose version --short 2>/dev/null || echo present)"
fi

# ---- 4b. standalone SearXNG dev container (bare-metal only) --------------
#
# Docker mode doesn't need this — docker-compose.yml brings up its own
# searxng service with JSON output already enabled (see
# compose/searxng/settings.yml), no separate container to manage.

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	step "Setting up SearXNG (local web search backend)"

	# Reuse an existing container (start it if stopped) rather than always
	# recreating — a fresh `docker run` with the same --name would just fail
	# with "container already exists" on a second run of this script.
	if docker_cmd ps -a --format '{{.Names}}' | grep -qx "$SEARXNG_CONTAINER"; then
		if docker_cmd ps --format '{{.Names}}' | grep -qx "$SEARXNG_CONTAINER"; then
			info "$SEARXNG_CONTAINER is already running."
		else
			info "$SEARXNG_CONTAINER exists but isn't running — starting it."
			docker_cmd start "$SEARXNG_CONTAINER"
		fi
	else
		info "Creating the $SEARXNG_CONTAINER container on port $SEARXNG_PORT."
		docker_cmd run -d --name "$SEARXNG_CONTAINER" -p "$SEARXNG_PORT:8080" \
			-v "$INSTALL_DIR/dev/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
			searxng/searxng:latest
	fi
fi

# ---- 5. config -------------------------------------------------------------

CONFIG_WAS_FRESH=0
COMPOSE_CONFIG_WAS_FRESH=0

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	step "Setting up config.yaml"

	if [ -f config.yaml ]; then
		info "config.yaml already exists — leaving it alone."
	else
		cp config.yaml.example config.yaml
		info "Copied config.yaml.example to config.yaml."
		CONFIG_WAS_FRESH=1
	fi
else
	step "Setting up .env and compose/polaris/config.yaml"

	if [ -f .env ]; then
		info ".env already exists — leaving it alone."
	else
		cp .env.example .env
		if command -v openssl >/dev/null 2>&1; then
			SEARXNG_SECRET="$(openssl rand -hex 32)"
			# macOS's BSD sed needs -i '' (empty extension arg); GNU sed on
			# Linux takes -i with no argument at all — this ternary picks
			# the right invocation per platform rather than assuming one.
			if [ "$OS" = "Darwin" ]; then
				sed -i '' "s/^SEARXNG_SECRET=.*/SEARXNG_SECRET=$SEARXNG_SECRET/" .env
			else
				sed -i "s/^SEARXNG_SECRET=.*/SEARXNG_SECRET=$SEARXNG_SECRET/" .env
			fi
			info "Copied .env.example to .env and generated a random SEARXNG_SECRET."
		else
			warn "openssl not found — copied .env.example to .env, but you'll need to"
			warn "fill in SEARXNG_SECRET yourself (openssl rand -hex 32, or any random string)."
		fi
	fi

	mkdir -p compose/polaris
	if [ -f compose/polaris/config.yaml ]; then
		info "compose/polaris/config.yaml already exists — leaving it alone."
	else
		cp compose/polaris/config.yaml.example compose/polaris/config.yaml
		info "Copied compose/polaris/config.yaml.example to compose/polaris/config.yaml."
		COMPOSE_CONFIG_WAS_FRESH=1
	fi

	# 777, not the default 755: this is bind-mounted into the polaris
	# container at /data/update-signal (docker-compose.yml), which writes
	# to it as the image's non-root polaris user (uid 100) — a numeric
	# uid that essentially never matches whatever host user runs this
	# script. Docker bind mounts don't remap ownership, so without this
	# the container's write (update-signal/requested — see
	# gateway/docker_update.go's writeUpdateSignal) fails outright with
	# a permission error. Found live, testing the real update flow
	# against a real container: not a hypothetical edge case. The
	# watcher script (running as the host user, not uid 100) needs
	# write access here too, for the same reason. Nothing sensitive
	# ever lives in this directory (an image reference string, a small
	# status JSON) — world-writable is the pragmatic fix for a
	# cross-UID-namespace shared directory, not a real exposure.
	mkdir -p update-signal
	chmod 777 update-signal

	# Same cross-UID-namespace bind-mount problem as update-signal above,
	# for the same reason: domain_rankings.yaml is bind-mounted
	# read-write (docker-compose.yml) so the ranking popover UI can write
	# it as the image's non-root polaris user, whose uid won't match
	# whatever host user owns the file by default. 666, not 777 — this
	# is a plain file, not a directory that needs the execute bit.
	if [ -f domain_rankings.yaml ]; then
		chmod 666 domain_rankings.yaml
	fi
fi

# ---- 5b. optional: Ollama for the query-similarity research signal --------
#
# Entirely optional — nudges the model when consecutive web_search
# queries embed as near-duplicates of each other, catching a rephrasing
# loop that a plain "found nothing new" citation check can miss (see
# README's Requirements section and agent/query_similarity.go). Declining
# just leaves that one signal disabled; nothing else is affected. Both
# example config files default to assuming Ollama IS set up (see their
# own ollama.base_url comments) — this step blanks that back out below
# when the answer is no, rather than leaving a dangling URL that fails
# every turn.

step "Optional: Ollama for the query-similarity research signal"
OLLAMA_BASE_URL=""
if command -v ollama >/dev/null 2>&1; then
	info "Ollama is already installed — leaving it as-is (not re-configuring it)."
	OLLAMA_BASE_URL="http://localhost:11434"
else
	info "Nudges the model when it repeats a search that isn't working — see"
	info "README's Requirements section for the full tradeoff. Needs ~300MB RAM"
	info "while active, so it's worth a firm no on very memory-constrained hardware."
	if ask_yes_no "Install Ollama + nomic-embed-text for this?"; then
		if [ "$OS" = "Darwin" ]; then
			if command -v brew >/dev/null 2>&1; then
				brew install ollama
				brew services start ollama
				OLLAMA_BASE_URL="http://localhost:11434"
			else
				warn "Homebrew isn't installed, so Ollama can't be installed automatically."
				warn "Install it yourself: https://ollama.com/download"
				warn "Then re-run this script, or add ollama.base_url to config.yaml by hand."
			fi
		else
			info "Installing Ollama via its official install script (needs sudo)."
			curl -fsSL https://ollama.com/install.sh | sh
			OLLAMA_BASE_URL="http://localhost:11434"
		fi
	else
		info "Skipping — this signal will just be disabled, nothing else is affected."
	fi
fi

if [ -n "$OLLAMA_BASE_URL" ]; then
	# Give the service a moment to actually come up before pulling — both
	# install paths above start it, but not synchronously enough to
	# guarantee `ollama pull` won't race a not-yet-listening server.
	for _ in $(seq 1 15); do
		curl -fsS http://127.0.0.1:11434/api/tags >/dev/null 2>&1 && break
		sleep 1
	done
	if curl -fsS http://127.0.0.1:11434/api/tags 2>/dev/null | grep -q nomic-embed-text; then
		info "nomic-embed-text is already pulled."
	else
		info "Pulling nomic-embed-text (small, ~274MB)…"
		# Soft-fail deliberately: Ollama itself is already installed by
		# this point, so a transient pull failure (network blip, disk
		# space) shouldn't abort an otherwise-successful Polaris install
		# under this script's `set -e` — same reasoning as with_timeout's
		# git-clone handling above. config.yaml still ends up pointing at
		# Ollama; the model just needs a manual `ollama pull
		# nomic-embed-text` before the signal actually works.
		if ! ollama pull nomic-embed-text; then
			warn "Pulling nomic-embed-text failed — Ollama itself is installed, but"
			warn "the query-similarity signal won't work until you run this by hand:"
			warn "  ollama pull nomic-embed-text"
		fi
	fi

	if [ "$INSTALL_MODE" = "docker" ] && [ "$OS" = "Linux" ]; then
		# Docker on Linux specifically needs Ollama reachable from the
		# container, not just localhost — its default 127.0.0.1-only bind
		# REFUSES a connection arriving via the Docker bridge gateway
		# (confirmed live: "connection refused", not just slow — see
		# docker-compose.yml's extra_hosts comment). A systemd drop-in
		# (not editing Ollama's own unit file directly, which its
		# installer manages and could overwrite on update) survives
		# Ollama's own updates.
		warn "Docker on Linux needs Ollama to accept non-localhost connections to"
		warn "be reachable from the container. This widens Ollama's reach to your"
		warn "whole LAN too, since it has no built-in auth — only proceed if"
		warn "that's fine on this network."
		if ask_yes_no "Rebind Ollama to 0.0.0.0 so the Polaris container can reach it?"; then
			sudo mkdir -p /etc/systemd/system/ollama.service.d
			printf '[Service]\nEnvironment="OLLAMA_HOST=0.0.0.0:11434"\n' |
				sudo tee /etc/systemd/system/ollama.service.d/override.conf >/dev/null
			sudo systemctl daemon-reload
			sudo systemctl restart ollama
			info "Ollama now listens on 0.0.0.0:11434."
			OLLAMA_BASE_URL="http://host.docker.internal:11434"
		else
			info "Leaving Ollama on localhost — the query-similarity signal stays"
			info "disabled under Docker until this is revisited."
			OLLAMA_BASE_URL=""
		fi
	elif [ "$INSTALL_MODE" = "docker" ] && [ "$OS" = "Darwin" ]; then
		# Docker Desktop's host.docker.internal is a userland proxy that
		# reaches loopback-bound services fine — unlike Linux Docker
		# Engine's real bridge-gateway routing, no rebind needed here.
		OLLAMA_BASE_URL="http://host.docker.internal:11434"
	fi
fi

# Only touch a config file this run FRESHLY created (see step 5 above) —
# a pre-existing config.yaml/compose config might carry the operator's
# own edits, and re-running this script must never clobber those, same
# as every other config step here leaves an existing file alone.
if [ "$INSTALL_MODE" = "bare-metal" ] && [ "$CONFIG_WAS_FRESH" = 1 ] && [ -z "$OLLAMA_BASE_URL" ]; then
	if [ "$OS" = "Darwin" ]; then
		sed -i '' 's|base_url: "http://localhost:11434"|base_url: ""|' config.yaml
	else
		sed -i 's|base_url: "http://localhost:11434"|base_url: ""|' config.yaml
	fi
elif [ "$INSTALL_MODE" = "docker" ] && [ "$COMPOSE_CONFIG_WAS_FRESH" = 1 ]; then
	if [ -z "$OLLAMA_BASE_URL" ]; then
		if [ "$OS" = "Darwin" ]; then
			sed -i '' 's|base_url: "http://host.docker.internal:11434"|base_url: ""|' compose/polaris/config.yaml
		else
			sed -i 's|base_url: "http://host.docker.internal:11434"|base_url: ""|' compose/polaris/config.yaml
		fi
	fi
	# else: OLLAMA_BASE_URL already equals the example's own default
	# ("http://host.docker.internal:11434") in every success path above,
	# so there's nothing to write — the copied file is already correct.
fi

# ---- 6. host update watcher (docker mode, Linux only) ---------------------
#
# macOS has no systemd — and isn't the target production deployment
# anyway (see README's "why not just use X": this is built to run on a
# Le Potato SBC, not a dev laptop). A macOS Docker install still works
# fine; "Update Polaris" in the settings panel just won't be wired up,
# same as it wasn't before this step existed.

if [ "$INSTALL_MODE" = "docker" ] && [ "$OS" = "Linux" ]; then
	step "Installing the host update watcher"

	if ! command -v systemctl >/dev/null 2>&1; then
		warn "systemctl not found — skipping the update watcher."
		warn "\"Update Polaris\" in the settings panel won't work until it's set up"
		warn "manually; see compose/watcher/ for the unit files."
	else
		WATCHER_SRC="$INSTALL_DIR/compose/watcher"
		WATCHER_TMP="$(mktemp -d)"
		trap 'rm -rf "$WATCHER_TMP"' EXIT

		for unit in polaris-update.service polaris-update.path polaris-update.timer; do
			sed -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" -e "s|@USER@|$USER|g" \
				"$WATCHER_SRC/$unit" >"$WATCHER_TMP/$unit"
			sudo cp "$WATCHER_TMP/$unit" "/etc/systemd/system/$unit"
		done
		info "Installed polaris-update.service/.path/.timer to /etc/systemd/system/."

		sudo systemctl daemon-reload
		# Enabling --now the .path and .timer is safe at install time even
		# with nothing pending: .path's PathExists condition is false until
		# a real update is requested (see that unit's comment), and
		# .timer's first tick is 5 minutes out (OnBootSec) — neither runs
		# polaris-update.service itself right now.
		sudo systemctl enable --now polaris-update.path polaris-update.timer
		info "Enabled polaris-update.path (instant trigger) and polaris-update.timer"
		info "(hourly backstop)."
	fi
fi

# ---- 7. open config for editing --------------------------------------------

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	step "Opening config.yaml for you to add your OpenRouter API key"
	EDIT_PATHS=("$INSTALL_DIR/config.yaml")
else
	# Only .env needs a fresh install's attention — every field in
	# compose/polaris/config.yaml already has a sane default or gets
	# filled in from .env via ${VAR} (see that file's own comments), so
	# there's nothing actionable in it to open unprompted. It's there to
	# edit later (model choice, voice settings, etc.), not on install.
	step "Opening .env for you to add your OpenRouter API key"
	info "(compose/polaris/config.yaml is also there if you want to tune model/voice"
	info "defaults later — nothing in it needs editing to get started.)"
	EDIT_PATHS=("$INSTALL_DIR/.env")
fi

if [ "$OS" = "Darwin" ]; then
	# Deliberately `open -e`, not a bare `open` — a bare `open` defers to
	# whatever LaunchServices has registered as the default handler for
	# .yaml, which is unpredictable and, on at least one real machine,
	# resolved to Xcode and triggered an entire Xcode install just to
	# show a text file. `-e` forces TextEdit specifically, which is
	# guaranteed present on every Mac and can't trigger anything like
	# that.
	open -e "${EDIT_PATHS[@]}"
elif command -v xdg-open >/dev/null 2>&1; then
	for p in "${EDIT_PATHS[@]}"; do
		xdg-open "$p" >/dev/null 2>&1 &
	done
else
	info "No GUI editor available on this machine (likely a headless server)."
	info "Edit these yourself: \${EDITOR:-nano} ${EDIT_PATHS[*]}"
fi

# ---- done -------------------------------------------------------------------

step "Done"
if [ "$INSTALL_MODE" = "bare-metal" ]; then
	info "Polaris is built and SearXNG is running at http://localhost:$SEARXNG_PORT"
	info ""
	info "Next steps:"
	info "  1. In config.yaml, set openrouter.api_key to your real key"
	info "     (get one at https://openrouter.ai/keys)"
	info "  2. cd $INSTALL_DIR && ./polaris run"
	info "  3. Open http://localhost:8899"
else
	info "Polaris's Docker install is set up in $INSTALL_DIR."
	info ""
	info "Next steps:"
	info "  1. In .env, set OPENROUTER_API_KEY to your real key"
	info "     (get one at https://openrouter.ai/keys)"
	info "  2. cd $INSTALL_DIR && docker compose up -d"
	info "  3. Open http://localhost:8899"
fi
