#!/usr/bin/env bash
#
# Polaris one-shot installer.
#
#   curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
#
# Clones the repo, builds the binary, brings up a local SearXNG instance
# via Docker (installing Docker itself if it's missing), copies
# config.yaml.example to config.yaml, and opens it in your default editor
# so you can drop in your OpenRouter key. Safe to re-run — every step
# checks what's already there before doing anything.
#
# What it deliberately does NOT do: start the Polaris server itself. The
# config needs a real API key first, so the last step is always "go fill
# this in", not "silently start a half-configured server in the
# background."
set -euo pipefail

REPO_URL="https://github.com/AutumnsGrove/Polaris.git"
INSTALL_DIR="${POLARIS_INSTALL_DIR:-$HOME/Polaris}"
SEARXNG_PORT="${POLARIS_SEARXNG_PORT:-18888}"
SEARXNG_CONTAINER="${POLARIS_SEARXNG_CONTAINER:-searxng-dev}"

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

# ---- 1. prerequisites: git, go -----------------------------------------

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

if ! command -v go >/dev/null 2>&1; then
	warn "Go is required and wasn't found."
	warn "Install it from https://go.dev/dl/ and re-run this script."
	exit 1
fi
info "go: $(go version)"

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

# ---- 3. build the binary ------------------------------------------------

step "Building the polaris binary"

go build -o polaris .
info "Built ./polaris"

# ---- 4. Docker + SearXNG -------------------------------------------------

step "Setting up SearXNG (local web search backend)"

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

# ---- 5. config.yaml -------------------------------------------------------

step "Setting up config.yaml"

if [ -f config.yaml ]; then
	info "config.yaml already exists — leaving it alone."
else
	cp config.yaml.example config.yaml
	info "Copied config.yaml.example to config.yaml."
fi

# ---- 6. open it for editing ------------------------------------------------

step "Opening config.yaml for you to add your OpenRouter API key"

CONFIG_PATH="$INSTALL_DIR/config.yaml"
if [ "$OS" = "Darwin" ]; then
	# Deliberately `open -e`, not a bare `open` — a bare `open` defers to
	# whatever LaunchServices has registered as the default handler for
	# .yaml, which is unpredictable and, on at least one real machine,
	# resolved to Xcode and triggered an entire Xcode install just to
	# show a text file. `-e` forces TextEdit specifically, which is
	# guaranteed present on every Mac and can't trigger anything like
	# that.
	open -e "$CONFIG_PATH"
elif command -v xdg-open >/dev/null 2>&1; then
	xdg-open "$CONFIG_PATH" >/dev/null 2>&1 &
else
	info "No GUI editor available on this machine (likely a headless server)."
	info "Edit it yourself: \${EDITOR:-nano} $CONFIG_PATH"
fi

# ---- done -------------------------------------------------------------------

step "Done"
info "Polaris is built and SearXNG is running at http://localhost:$SEARXNG_PORT"
info ""
info "Next steps:"
info "  1. In config.yaml, set openrouter.api_key to your real key"
info "     (get one at https://openrouter.ai/keys)"
info "  2. cd $INSTALL_DIR && ./polaris run"
info "  3. Open http://localhost:8899"
