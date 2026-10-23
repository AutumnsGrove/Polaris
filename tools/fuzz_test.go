package tools

import (
	"encoding/base64"
	"strings"
	"testing"
)

// PDFs come straight off the open web, and the parser is third-party.
func FuzzExtractPDFPage(f *testing.F) {
	for _, b64 := range []string{twoPageTestPDFBase64, minimalTestPDFBase64} {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, 1)
		f.Add(data, 0)
		f.Add(data[:len(data)/2], 2)
	}
	f.Add([]byte("%PDF-1.4\n"), 1)
	f.Add([]byte{}, 1)
	f.Fuzz(func(t *testing.T, data []byte, page int) {
		_, text, total, err := ExtractPDFPage(data, page)
		if total < 0 {
			t.Errorf("totalPages = %d", total)
		}
		if err != nil && text != "" {
			t.Errorf("error %v returned alongside text %q", err, text)
		}
	})
}

func FuzzExtractJSONBlock(f *testing.F) {
	f.Add("```json\n{\"a\":1}\n```")
	f.Add("prose {\"a\":{\"b\":2}} trailing")
	f.Add("```\n{\"a\":1}")
	f.Add("}{")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		got := extractJSONBlock(raw)
		if got != "" && !strings.HasPrefix(got, "{") {
			t.Errorf("result %q does not start with {", got)
		}
	})
}

func FuzzParseSubAgentReport(f *testing.F) {
	f.Add("obj", "```json\n{\"findings\":[{\"claim\":\"c\",\"sources\":[\"u\"]}]}\n```")
	f.Add("obj", "plain prose answer")
	f.Add("obj", "{}")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, objective, raw string) {
		r := ParseSubAgentReport(objective, raw, []Citation{{URL: "https://a.org"}})
		if r.Objective != objective {
			t.Errorf("objective = %q, want fallback %q", r.Objective, objective)
		}
		if len(r.Findings) == 0 {
			t.Errorf("no findings for %q", raw)
		}
	})
}

func FuzzParseTavilyYouTubeTranscript(f *testing.F) {
	f.Add("Title line\n\nTranscript\n0:01 hello\n0:05 world", "fb")
	f.Add("Transcript", "fb")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, raw, fallback string) {
		_, transcript, ok := parseTavilyYouTubeTranscript(raw, fallback)
		if ok && strings.TrimSpace(transcript) == "" {
			t.Errorf("ok with empty transcript for %q", raw)
		}
	})
}

func FuzzParseGitHubRepo(f *testing.F) {
	f.Add("owner/repo")
	f.Add("https://github.com/owner/repo.git")
	f.Add("git@github.com:owner/repo")
	f.Add("github.com/owner/repo/blob/main/x.go")
	f.Add("")
	f.Fuzz(func(t *testing.T, in string) {
		owner, repo, err := parseGitHubRepo(in)
		if err != nil {
			return
		}
		if owner == "" || repo == "" || strings.ContainsAny(owner+repo, "/ \t\n") {
			t.Errorf("parseGitHubRepo(%q) = %q, %q", in, owner, repo)
		}
	})
}

func FuzzExtractYouTubeID(f *testing.F) {
	f.Add("dQw4w9WgXcQ")
	f.Add("https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=5")
	f.Add("https://youtu.be/dQw4w9WgXcQ")
	f.Add("https://youtube.com/shorts/dQw4w9WgXcQ")
	f.Add("%%%")
	f.Fuzz(func(t *testing.T, in string) {
		id, err := extractYouTubeID(in)
		if err == nil && len(id) != 11 {
			t.Errorf("extractYouTubeID(%q) = %q, want 11 chars", in, id)
		}
	})
}

func FuzzParseGitHubSince(f *testing.F) {
	f.Add("2026-01-02")
	f.Add("2026-01-02T03:04:05Z")
	f.Add("")
	f.Add("yesterday")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseGitHubSince(s)
	})
}

func FuzzParseGitHubLastPage(f *testing.F) {
	f.Add(`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=9>; rel="last"`)
	f.Add(`<x?page=99999999999999999999>; rel="last"`)
	f.Add("")
	f.Fuzz(func(t *testing.T, h string) {
		if n := parseGitHubLastPage(h); n < 0 {
			t.Errorf("parseGitHubLastPage(%q) = %d", h, n)
		}
	})
}

func FuzzStripBoilerplateLines(f *testing.F) {
	f.Add("nav\n" + strings.Repeat("a real article sentence. ", 10) + "\nfooter")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		got := stripBoilerplateLines(raw)
		if len(got) > len(raw) {
			t.Errorf("output longer than input")
		}
	})
}
