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
	git -C "$INSTALL_DIR" pull --ff-only
elif [ -e "$INSTALL_DIR" ]; then
	warn "$INSTALL_DIR exists and isn't a git checkout. Move it aside or set"
	warn "POLARIS_INSTALL_DIR to a different path, then re-run."
	exit 1
else
	git clone "$REPO_URL" "$INSTALL_DIR"
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
		sudo usermod -aG docker "$USER" || true
		DOCKER_NEEDS_SUDO=1
		info "Added $USER to the docker group — log out and back in for passwordless"
		info "'docker' access afterward. Using sudo for the rest of this run."
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

if [ "$INSTALL_MODE" = "bare-metal" ]; then
	step "Setting up config.yaml"

	if [ -f config.yaml ]; then
		info "config.yaml already exists — leaving it alone."
	else
		cp config.yaml.example config.yaml
		info "Copied config.yaml.example to config.yaml."
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
