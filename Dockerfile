# syntax=docker/dockerfile:1

# --- build ------------------------------------------------------------
# web/build/ (the SvelteKit static output go:embed pulls in) is committed
# to the repo rather than built here — see README's "Frontend
# development" section. This stage only ever compiles Go.
FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

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
