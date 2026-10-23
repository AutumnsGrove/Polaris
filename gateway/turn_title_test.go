package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeGeneratedTitle_StripsLabelAndExtraLines(t *testing.T) {
	for in, want := range map[string]string{
		"Thread title: Go Release, Security, and Proposal Research\n\nI searched...": "Go Release, Security, and Proposal Research",
		"**Title:** Framework 13 vs ThinkPad":                                        "Framework 13 vs ThinkPad",
		"\"Plain quoted title\"":                                                     "Plain quoted title",
		"Titanic sinking date":                                                       "Titanic sinking date", // starts like "Title" but has no label
	} {
		if got := sanitizeGeneratedTitle(in); got != want {
			t.Errorf("sanitizeGeneratedTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A byte-index cut at maxTitleLen landed mid-rune for multi-byte text and
// stored invalid UTF-8; found by FuzzSanitizeGeneratedTitle.
func TestSanitizeGeneratedTitle_TruncatesOnRuneBoundary(t *testing.T) {
	raw := strings.Repeat("é", 29) + "日本語の長いタイトル"
	got := sanitizeGeneratedTitle(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("title %q is not valid UTF-8", got)
	}
	if len(got) > maxTitleLen {
		t.Errorf("title is %d bytes, cap is %d", len(got), maxTitleLen)
	}
	if !strings.HasPrefix(raw, got) {
		t.Errorf("title %q is not a prefix of the input", got)
	}
}
