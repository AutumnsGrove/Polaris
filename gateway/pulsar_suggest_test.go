package gateway

import (
	"net/http"
	"strings"
	"testing"

	"polaris/store"
)

func TestParsePulsarSuggestion(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantName   string
		wantPrompt string
	}{
		{
			name:       "the requested shape",
			raw:        "Name: Prada resale prices\n---\nTrack Prada bag resale prices on The RealReal and Vestiaire and report what moved.",
			wantName:   "Prada resale prices",
			wantPrompt: "Track Prada bag resale prices on The RealReal and Vestiaire and report what moved.",
		},
		{
			name:       "markdown fence around the whole reply",
			raw:        "```\nName: Guild Wars 3 news\n---\nRound up the biggest Guild Wars 3 news today.\n```",
			wantName:   "Guild Wars 3 news",
			wantPrompt: "Round up the biggest Guild Wars 3 news today.",
		},
		{
			// A dropped separator must not discard the draft — it's the
			// part that actually matters; only the name is lost.
			name:       "no separator falls back to the whole reply as the prompt",
			raw:        "Track the price of the Framework 13 every week.",
			wantName:   "",
			wantPrompt: "Track the price of the Framework 13 every week.",
		},
		{
			name:       "missing Name: label still yields the name",
			raw:        "DeepSeek papers\n---\nList new DeepSeek papers on arXiv.",
			wantName:   "DeepSeek papers",
			wantPrompt: "List new DeepSeek papers on arXiv.",
		},
		{
			name:       "quoted name is unquoted",
			raw:        "Name: \"TV deals\"\n---\nCheck current prices on 43-50\" TVs.",
			wantName:   "TV deals",
			wantPrompt: "Check current prices on 43-50\" TVs.",
		},
		{
			// A multi-paragraph prompt keeps its internal newlines.
			name:       "multi-line prompt is preserved",
			raw:        "Name: Watch\n---\nFirst line.\n\nSecond paragraph.",
			wantName:   "Watch",
			wantPrompt: "First line.\n\nSecond paragraph.",
		},
		{
			name:       "CRLF from a proxy is normalized",
			raw:        "Name: Watch\r\n---\r\nCheck the score.",
			wantName:   "Watch",
			wantPrompt: "Check the score.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, prompt := parsePulsarSuggestion(tc.raw)
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if prompt != tc.wantPrompt {
				t.Errorf("prompt = %q, want %q", prompt, tc.wantPrompt)
			}
		})
	}
}

func TestBuildPulsarSuggestTranscript(t *testing.T) {
	msgs := []store.Message{
		{Role: "user", Content: "looking at prada bags"},
		{Role: "assistant", Content: "here are three"},
		{Role: "user", Content: "what about the resale value?"},
		{Role: "assistant", Content: ""},   // never persisted, but must not render as "Assistant: "
		{Role: "tool", Content: "ignored"}, // not a chat turn
	}

	got := buildPulsarSuggestTranscript(msgs)
	want := "User: looking at prada bags\n\nAssistant: here are three\n\nUser: what about the resale value?"
	if got != want {
		t.Errorf("transcript = %q, want %q", got, want)
	}
}

// The tail is kept, not the head: the recurring interest a routine is
// derived from is whatever the conversation most recently converged on.
func TestBuildPulsarSuggestTranscript_KeepsTheTailAndDropsEmpty(t *testing.T) {
	var msgs []store.Message
	for range 10 {
		msgs = append(msgs, store.Message{Role: "assistant", Content: strings.Repeat("x", pulsarSuggestMaxPerMessage)})
	}
	msgs = append(msgs, store.Message{Role: "user", Content: "the actual question"})

	got := buildPulsarSuggestTranscript(msgs)
	if !strings.HasSuffix(got, "User: the actual question") {
		t.Errorf("want the newest message kept at the end, got %q", tail(got, 80))
	}
	if len(got) > pulsarSuggestMaxTranscript+pulsarSuggestMaxPerMessage {
		t.Errorf("transcript not bounded: %d bytes", len(got))
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("short", 20); got != "short" {
		t.Errorf("under the cap must be returned unchanged, got %q", got)
	}
	got := truncateRunes(strings.Repeat("a", 50), 10)
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) || !strings.HasSuffix(got, "[…]") {
		t.Errorf("over the cap must be cut and marked, got %q", got)
	}
	// Rune-safe: emoji must not be cut in half.
	if got := truncateRunes(strings.Repeat("🌙", 10), 3); strings.Contains(got, "\uFFFD") {
		t.Errorf("truncation split a rune: %q", got)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestHandleSuggestPulsarPrompt_RejectsUnknownKind(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	resp, err := http.Post(h.url("/api/pulsar/suggest"), "application/json",
		strings.NewReader(`{"thread_id":"t1","kind":"weekly"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown kind", resp.StatusCode)
	}
}
