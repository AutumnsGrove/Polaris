// Package web embeds the built SvelteKit static site (web/build, produced
// by `pnpm run build` with adapter-static) directly into the Go binary.
// Not committed to git — Docker's image build always runs `pnpm run
// build` fresh from web/src/ (see Dockerfile's frontend-build stage);
// for bare-metal dev, run it by hand whenever the frontend changes.
package web

import "embed"

// all: prefix is required — Vite's output includes an "_app" directory,
// and Go's embed skips paths starting with "_" or "." by default.
//
//go:embed all:build
var Assets embed.FS
