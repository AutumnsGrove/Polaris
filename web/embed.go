// Package web embeds the built SvelteKit static site (web/build, produced
// by `pnpm run build` with adapter-static) directly into the Go binary.
// This is committed to git deliberately — it means the Le Potato never
// needs Node/pnpm installed, `update` only ever runs `git pull && go build`.
// Rebuild and commit `build/` locally whenever the frontend changes.
package web

import "embed"

// all: prefix is required — Vite's output includes an "_app" directory,
// and Go's embed skips paths starting with "_" or "." by default.
//
//go:embed all:build
var Assets embed.FS
