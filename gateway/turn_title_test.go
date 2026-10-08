package gateway

import "testing"

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
