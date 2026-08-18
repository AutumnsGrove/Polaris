# syntax=docker/dockerfile:1

# --- frontend build -----------------------------------------------------
# web/build/ is committed to git for the bare-metal path (see README's
# "Frontend development": the Le Potato SBC can't afford pnpm install +
# vite build on every self-update, so that cost stays off-device,
# permanently, via a checked-in prebuilt copy). That constraint doesn't
# apply here — this image is always built on a real machine (CI, or a
# dev machine via `docker compose up --build`), never on the potato
# itself, which only ever pulls an already-built image. So instead of
# depending on the committed copy staying in sync (a real footgun: see
# .github/workflows/frontend-build-sync.yml, which exists purely to
# catch that drift), this stage just builds straight from web/src every
# time — always exactly what's in this commit, no possibility of drift.
# Node/pnpm versions matched to that same workflow for the same
# reproducibility reason it pins them.
#
# --platform=$BUILDPLATFORM pins this stage to the build host's own
# architecture regardless of which platform(s) this image is being
# built for (see the release workflow's `platforms: linux/amd64,
# linux/arm64`) — a Vite/SvelteKit build produces plain JS/CSS/HTML,
# architecture-independent output, so there's no reason to run it once
# per target arch under QEMU emulation (slow, and Node's native addons
# are the exact thing QEMU handles worst). It builds once, natively,
# and every target-arch stage below reuses the same output.
FROM --platform=$BUILDPLATFORM node:22-bookworm AS frontend-build
WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate && \
    pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm run build

# --- build ------------------------------------------------------------
# Also pinned to --platform=$BUILDPLATFORM, same reasoning as
# frontend-build but for a different underlying reason: Go cross-
# compiles natively (just set GOOS/GOARCH — no C toolchain involved
# since CGO_ENABLED=0, see below), so running this stage under
# emulation for the target arch would only make it slower for zero
# correctness benefit. TARGETOS/TARGETARCH (buildx-provided build args
# describing what we're actually compiling *for*) drive the
# cross-compilation directly instead.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overwrites whatever committed web/build/ COPY . . just brought in
# with the freshly built copy from frontend-build above — see that
# stage's comment for why this image never relies on the committed one
# being accurate.
COPY --from=frontend-build /web/build ./web/build

# ARG (not just a shell default inside the RUN below) is required for
# --build-arg VERSION=... to actually reach this step at all — without
# declaring it, ${VERSION} in the RUN command below silently resolves
# to nothing but its own :- fallback every time, regardless of what's
# passed at `docker build` time. Meant to be the git short SHA (CI's
# release workflow passes one; a local `docker build` with no
# --build-arg falls back to this default) — gateway/version.go prefers
# this over shelling out to git for its own version reporting, since
# there's no .git directory in this image to shell out to (see
# .dockerignore) — see gateway.Server's version field's doc comment.
ARG VERSION=dev-docker
# TARGETOS/TARGETARCH: automatically populated by buildx per platform
# in `platforms: linux/amd64,linux/arm64` — no ARG declaration needed
# for these two specifically (buildx predefines them), but they must be
# named here to be visible inside this stage's RUN commands.
ARG TARGETOS
ARG TARGETARCH

# CGO_ENABLED=0: modernc.org/sqlite is a pure-Go SQLite driver, so this
# binary has no C dependency at all — a fully static binary that runs in
# a scratch-derived runtime stage with no libc, no musl, nothing to patch
# for CVEs beyond the binary itself. That same fact is what makes
# GOOS/GOARCH cross-compilation just work here with zero extra setup —
# no per-arch C toolchain to install, unlike a cgo-dependent build.
RUN --mount=type=cache,target=/root/.cache/go-build,id=go-build-${TARGETOS}-${TARGETARCH} \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/polaris .

# --- runtime ------------------------------------------------------------
FROM alpine:3.20

# ca-certificates: every tool call this makes (OpenRouter, SearXNG,
# TMDB, Last.fm, Foursquare, GitHub, Wikipedia, arXiv...) is outbound
# HTTPS, so this is not optional the way it might be in a purely
# internal service.
RUN apk add --no-cache ca-certificates && \
    addgroup -S polaris && adduser -S polaris -G polaris

WORKDIR /app
COPY --from=build /out/polaris /app/polaris

# prompt.md/prompts.yaml/blocked_sources.txt/domain_rankings.yaml/
# tools/descriptions/*.yaml are loaded relative to CWD and hot-reloaded
# from disk (see prompts/prompts.go, agent's loadSystemPrompt,
# search.LoadBlocklist, search.LoadDomainRankings, tools/catalog.go's
# loadCatalog) — baked in here so the image runs standalone, but
# docker-compose.yml bind-mounts the repo's real copies over these so
# editing them on the host still takes effect without a rebuild, same as
# the bare-metal deployment. domain_rankings.yaml's mount is read-write.
# not read-only like the other three, since the ranking popover UI
# writes to it too (see search.SetDomainRanking) — install.sh chmods the
# host file so the container's non-root uid can write it.
COPY prompt.md prompts.yaml blocked_sources.txt domain_rankings.yaml /app/
COPY tools/descriptions/ /app/tools/descriptions/

# /data holds everything config.yaml.example points relative paths at
# by default (database, logs, attachments) — a single named volume
# covers all three instead of juggling three mounts.
RUN mkdir -p /data/logs /data/attachments && chown -R polaris:polaris /app /data

USER polaris
EXPOSE 8899

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8899/healthz || exit 1

ENTRYPOINT ["/app/polaris"]
CMD ["run", "--config", "/data/config.yaml"]
