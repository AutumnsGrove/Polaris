package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"

	"polaris/llm"
	"polaris/tools"
)

func FuzzParseReconcileResponse(f *testing.F) {
	f.Add("INVALIDATES: false\nTITLE: New\nSUMMARY: s\nBODY: first\nsecond")
	f.Add("INVALIDATES: true\nBODY: x")
	f.Add("BODY:")
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) {
		title, summary, body, invalidated := parseReconcileResponse(content)
		if invalidated && (title != "" || summary != "" || body != "") {
			t.Errorf("invalidated result carries content: %q %q %q", title, summary, body)
		}
	})
}

func FuzzParsePulsarSuggestion(f *testing.F) {
	f.Add("Name: Morning news\n---\nSummarize the news.")
	f.Add("```\nName: x\n---\ny\n```")
	f.Add("no separator at all")
	f.Add("---")
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = parsePulsarSuggestion(raw)
	})
}

// A title is stored and later rendered, so it must stay valid UTF-8 and
// within the cap whatever the model sends back.
func FuzzSanitizeGeneratedTitle(f *testing.F) {
	f.Add("Title: Go fuzzing basics.")
	f.Add("\"Quoted\"\nSecond line")
	f.Add(strings.Repeat("日本語", 40))
	f.Add(strings.Repeat("é", 59) + "日本")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		got := sanitizeGeneratedTitle(raw)
		if len(got) > maxTitleLen {
			t.Errorf("title is %d bytes, cap is %d: %q", len(got), maxTitleLen, got)
		}
		if utf8.ValidString(raw) && !utf8.ValidString(got) {
			t.Errorf("valid input %q produced invalid UTF-8 title %q", raw, got)
		}
	})
}

func FuzzParseDailyVerdict(f *testing.F) {
	f.Add(`{"verdict":"notable","gist":"g","reasoning":"r"}`, "")
	f.Add(`{"verdict":"bogus"}`, "")
	f.Add(`not json`, "")
	f.Add(``, "plain prose reply")
	f.Fuzz(func(t *testing.T, args, content string) {
		resp := &llm.ChatResponse{Content: content}
		if args != "" {
			resp.ToolCalls = []llm.ToolCall{{Function: llm.FunctionCall{Name: "record_verdict", Arguments: args}}}
		}
		v, err := parseDailyVerdict("block", resp)
		if err == nil && v.Verdict != "unchanged" && v.Verdict != "notable" && v.Verdict != "normal" {
			t.Errorf("unrecognized verdict %q escaped normalization", v.Verdict)
		}
	})
}

// Chunks are handed to the LLM and re-encoded as JSON, so each must be valid
// UTF-8 when the source text is.
func FuzzSplitIntoChunks(f *testing.F) {
	f.Add("para one\n\npara two\n\npara three")
	f.Add(strings.Repeat("日本語の段落。\n\n", 2000))
	f.Add("")
	f.Add(strings.Repeat("é", 20000))
	// Big enough, with an odd-length multi-byte paragraph, to force an overlap
	// that lands mid-rune (the chunk target is tens of thousands of bytes).
	f.Add(strings.Repeat(strings.Repeat("é", 700)+"x\n\n", 40))
	f.Fuzz(func(t *testing.T, text string) {
		for i, c := range splitIntoChunks(text) {
			if utf8.ValidString(text) && !utf8.ValidString(c) {
				t.Fatalf("chunk %d is invalid UTF-8 for valid input", i)
			}
		}
	})
}

func FuzzNormalizeForQuote(f *testing.F) {
	f.Add("“Curly” quotes — and dashes…")
	f.Add("  leading and trailing  ")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		once := normalizeForQuote(s)
		if again := normalizeForQuote(once); again != once {
			t.Errorf("not idempotent: %q -> %q -> %q", s, once, again)
		}
		if strings.HasPrefix(once, " ") || strings.HasSuffix(once, " ") || strings.Contains(once, "  ") {
			t.Errorf("normalized form %q has stray spaces", once)
		}
	})
}

func FuzzExtractClaims(f *testing.F) {
	f.Add("The sky is blue [Source](https://a.org/x). It is also big [Again](https://a.org/x).", "https://a.org/x")
	f.Add("[a](b)", "b")
	f.Add("[[]]()", "")
	f.Fuzz(func(t *testing.T, answer, url string) {
		_ = extractClaims(answer, []tools.Citation{{URL: url}})
	})
}
