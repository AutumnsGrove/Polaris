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
FROM node:22-bookworm AS frontend-build
WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate && \
    pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm run build

# --- build ------------------------------------------------------------
FROM golang:1.26-alpine AS build

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

# CGO_ENABLED=0: modernc.org/sqlite is a pure-Go SQLite driver, so this
# binary has no C dependency at all — a fully static binary that runs in
# a scratch-derived runtime stage with no libc, no musl, nothing to patch
# for CVEs beyond the binary itself.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/polaris .

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

# prompt.md/prompts.yaml/blocked_sources.txt are loaded relative to CWD
# and hot-reloaded from disk (see prompts/prompts.go, agent's
# loadSystemPrompt, search.LoadBlocklist) — baked in here so the image
# runs standalone, but docker-compose.yml bind-mounts the repo's real
# copies over these so editing them on the host still takes effect
# without a rebuild, same as the bare-metal deployment.
COPY prompt.md prompts.yaml blocked_sources.txt /app/

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
