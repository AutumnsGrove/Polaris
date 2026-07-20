package main

import "time"

// Version is set at build time via -ldflags "-X main.Version=..."
// Falls back to dev build timestamp if not set.
var Version = "dev-" + time.Now().Format("20060102-150405")

// BuildTime is the UTC timestamp when this binary was built.
var BuildTime = time.Now().UTC().Format(time.RFC3339)
