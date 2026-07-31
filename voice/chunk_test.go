package voice

import (
	"strings"
	"testing"
)

func TestStripMarkdown(t *testing.T) {
	cases := map[string]string{
		"**bold** text":                     "bold text",
		"*italic* text":                     "italic text",
		"[a link](https://example.com)":     "a link",
		"# Heading":                         "Heading",
		"some `code` here":                  "some code here",
		"plain text, nothing to strip here": "plain text, nothing to strip here",
	}
	for input, want := range cases {
		if got := StripMarkdown(input); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSplitIntoSpeechChunks_Empty(t *testing.T) {
	if got := SplitIntoSpeechChunks(""); got != nil {
		t.Errorf("SplitIntoSpeechChunks(\"\") = %v, want nil", got)
	}
	if got := SplitIntoSpeechChunks("   "); got != nil {
		t.Errorf("SplitIntoSpeechChunks(whitespace) = %v, want nil", got)
	}
}

func TestSplitIntoSpeechChunks_PacksShortSentences(t *testing.T) {
	chunks := SplitIntoSpeechChunks("One. Two. Three.")
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (short sentences should pack together): %v", len(chunks), chunks)
	}
	if chunks[0] != "One. Two. Three." {
		t.Errorf("chunk = %q, want %q", chunks[0], "One. Two. Three.")
	}
}

func TestSplitIntoSpeechChunks_SplitsAtCap(t *testing.T) {
	long := strings.Repeat("word ", 40) + "sentence one. " + strings.Repeat("word ", 40) + "sentence two."
	chunks := SplitIntoSpeechChunks(long)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want at least 2 for text well over the cap: %v", len(chunks), chunks)
	}
	for i, c := range chunks {
		if len(c) > maxChunkChars+50 { // small slack for a single over-cap sentence
			t.Errorf("chunk %d is %d chars, want roughly <= %d: %q", i, len(c), maxChunkChars, c)
		}
	}
}

func TestSplitIntoSpeechChunks_StripsMarkdownFirst(t *testing.T) {
	chunks := SplitIntoSpeechChunks("Check out [this page](https://example.com) for **details**.")
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %v", len(chunks), chunks)
	}
	if strings.Contains(chunks[0], "https://") || strings.Contains(chunks[0], "*") || strings.Contains(chunks[0], "[") {
		t.Errorf("chunk still contains markdown: %q", chunks[0])
	}
}

func TestSplitIntoSpeechChunks_ReassemblesToRoughlyTheOriginalContent(t *testing.T) {
	chunks := SplitIntoSpeechChunks("First sentence here. Second sentence here. Third one too.")
	joined := strings.Join(chunks, " ")
	for _, want := range []string{"First sentence here.", "Second sentence here.", "Third one too."} {
		if !strings.Contains(joined, want) {
			t.Errorf("joined chunks %q missing %q", joined, want)
		}
	}
}
