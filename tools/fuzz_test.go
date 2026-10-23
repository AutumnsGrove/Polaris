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
