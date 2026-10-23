package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"

	"polaris/llm"
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
