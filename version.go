package main

// Version is set at build time via -ldflags "-X main.Version=...". Must
// stay a bare string-literal initializer — the -X linker flag can only
// override a var declared exactly this way; a computed initializer (this
// used to be "dev-" + time.Now().Format(...)) silently defeats it, since
// package init still runs that expression and overwrites whatever -X
// pre-set, with no build error to notice. Found live: the Docker build's
// CI-injected short SHA was genuinely embedded in the binary (confirmed
// with `strings` on the compiled output) but /api/version still reported
// the git-fallback "dev" — the actual Version variable at runtime had
// never been the linker's value at all. cmd/run.go's dev-build filter
// checks for exactly "dev" now, not a "dev-" prefix, to match.
var Version = "dev"
