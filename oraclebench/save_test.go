package oraclebench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlugAndBaseName(t *testing.T) {
	tests := map[string]string{
		"full re-run, 295-case bank": "full-re-run-295-case-bank",
		"":                           "run",
		"  !!  ":                     "run",
		"Smoke 1.13 vs 1.14":         "smoke-1-13-vs-1-14",
	}
	for in, want := range tests {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if long := Slug(strings.Repeat("abc ", 40)); len(long) > 48 || strings.HasSuffix(long, "-") {
		t.Errorf("long label must be cut cleanly, got %q", long)
	}
	if got := BaseName("2026-10-16T12:03:01Z", "Full run"); got != "20261016T120301Z-full-run" {
		t.Errorf("BaseName = %q", got)
	}
}

func TestSaveResults_WritesBothFilesAndNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "runs") // must be created
	board := BuildLeaderboard(Leaderboard{Cases: 295, Repeats: 3, Concurrency: 2, Budget: 5 * time.Second, WeightsHash: "abc",
		Entries: []Entry{entry("typesafe/jev-1.14", 88, 88, 300*time.Millisecond, 500*time.Millisecond)}})

	paths, err := SaveResults(dir, "2026-10-16T12:03:01Z", "full run", board)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.JSON) != "20261016T120301Z-full-run.json" || filepath.Base(paths.Text) != "20261016T120301Z-full-run.txt" {
		t.Errorf("paths = %+v", paths)
	}

	raw, err := os.ReadFile(paths.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var back Leaderboard
	if err := json.Unmarshal(raw, &back); err != nil || back.Entries[0].Model != "typesafe/jev-1.14" || back.WeightsHash != "abc" {
		t.Errorf("saved JSON doesn't round-trip: %v %+v", err, back)
	}
	text, _ := os.ReadFile(paths.Text)
	if !strings.Contains(string(text), "typesafe/jev-1.14") || !strings.Contains(string(text), "Latency, fastest first") {
		t.Errorf("saved text is not the rendered leaderboard:\n%s", text)
	}

	// A second run with the same stamp and label must fail loudly and leave the first intact.
	board.Entries[0].Score.Headline = 1
	if _, err := SaveResults(dir, "2026-10-16T12:03:01Z", "full run", board); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("err = %v, want a refusal to overwrite", err)
	}
	after, _ := os.ReadFile(paths.JSON)
	if string(after) != string(raw) {
		t.Error("the earlier result was modified")
	}
}
