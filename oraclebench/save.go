package oraclebench

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Results are saved by the command itself rather than left to whoever runs it.
// The first full run's leaderboard went to /tmp and survived only because a
// human remembered to copy it; a benchmark whose results depend on that will
// eventually lose one. So a finished run always leaves, with no flags:
//
//	<save-dir>/<stamp>-<label>.json   the leaderboard (small, meant to be committed)
//	<save-dir>/<stamp>-<label>.txt    the same leaderboard as printed, for pasting
//
// and the bulky per-call and per-case detail goes to a gitignored local
// directory (see ResultsDir). Files are timestamped and never overwritten, so a
// re-run can't replace a baseline.

// DefaultSaveDir is where committed leaderboards live.
const DefaultSaveDir = "docs/benchmarks/oracle-decision-models/runs"

// DefaultResultsDir holds the bulky local-only output: raw calls, per-case
// scores, and the tracking database. It is gitignored.
const DefaultResultsDir = "oraclebench/results"

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a label safe and short for a filename ("" becomes "run").
func Slug(label string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if s == "" {
		return "run"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

// RunStamp turns an RFC3339 run time into the compact form used in filenames
// and table names (2026-10-16T12:03:01Z -> 20261016T120301Z).
func RunStamp(runAt string) string { return tableNameSanitizer.ReplaceAllString(runAt, "") }

// BaseName is the shared stem for one run's files.
func BaseName(runAt, label string) string { return RunStamp(runAt) + "-" + Slug(label) }

// SavedPaths lists what SaveResults wrote.
type SavedPaths struct {
	JSON string
	Text string
}

// SaveResults writes the leaderboard JSON and its rendered text into dir
// (created if needed). It refuses to overwrite an existing file: two runs with
// the same stamp and label are a collision to surface, not a result to replace.
func SaveResults(dir, runAt, label string, board Leaderboard) (SavedPaths, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SavedPaths{}, fmt.Errorf("creating %s: %w", dir, err)
	}
	js, err := board.JSON()
	if err != nil {
		return SavedPaths{}, fmt.Errorf("encoding leaderboard: %w", err)
	}
	var text bytes.Buffer
	board.Render(&text)

	base := filepath.Join(dir, BaseName(runAt, label))
	paths := SavedPaths{JSON: base + ".json", Text: base + ".txt"}
	for path, data := range map[string][]byte{paths.JSON: js, paths.Text: text.Bytes()} {
		if err := writeNew(path, data); err != nil {
			return SavedPaths{}, err
		}
	}
	return paths, nil
}

// writeNew creates path with data, failing if it already exists.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("saving %s (an existing result is never overwritten): %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}
