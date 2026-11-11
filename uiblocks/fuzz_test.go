package uiblocks

import (
	"strings"
	"testing"
)

// uiblocks parses model output, which is arbitrary text; a panic here would
// take down the whole server mid-turn.
func FuzzFlattenAndStrip(f *testing.F) {
	f.Add("intro\n\n```ui\n{\"c\":\"compare\",\"cols\":[\"A\",\"B\"],\"pick\":0}\n{\"row\":\"x\",\"v\":[\"1\",\"2\"]}\n{\"c\":\"steps\"}\n{\"i\":\"s\"}\n```\n\nend\n")
	f.Add("a\n```ui\n{\"c\":\"stat\",\"value\":\"1\"}\n```\nb\n")
	f.Add("a\n```ui\n{\"c\":\"stat\",\"value\":\"1\"}\n")
	f.Add("```go\nui := 1\n```\n")
	f.Add("```ui\n{\"c\":\"tabs\"}\n{\"tab\":\"A\",\"text\":\"x\"}\n```\n")
	f.Add("```ui\n[1,2,3]\nnot json\n{\"c\":5}\n```\n")
	f.Fuzz(func(t *testing.T, src string) {
		_ = Flatten(src)
		stripped := Strip(src)
		if !strings.Contains(src, "ui") && stripped != src {
			t.Errorf("Strip changed content with no %q anywhere in it: %q -> %q", "ui", src, stripped)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add("{\"c\":\"stat\",\"value\":\"1\"}\n")
	f.Add("{\"c\":\"compare\",\"cols\":[\"A\"]}\n{\"row\":\"x\",\"v\":[\"1\"]}\n")
	f.Add("\n\n{\"c\":\n")
	f.Fuzz(func(t *testing.T, src string) {
		_ = parse(src)
	})
}

func FuzzSites(f *testing.F) {
	f.Add("```ui\n{\"c\":\"compare\",\"cols\":[\"A\"]}\n{\"row\":\"x\",\"v\":[\"[a](https://a.org)\"]}\n```\n")
	f.Add("```ui\n{\"c\":\"stat\",\"value\":\"1\",\"src\":[\"https://a.org\"]}\n")
	f.Fuzz(func(t *testing.T, content string) {
		_ = Sites(content, func(string) bool { return true })
	})
}

func FuzzCloseBrackets(f *testing.F) {
	f.Add(`{"row":"x","v":["a","b"}`)
	f.Add(`[1,[2}`)
	f.Add(`{"a":"unterminated`)
	f.Add(`]}`)
	f.Fuzz(func(t *testing.T, s string) {
		out, ok := closeBrackets(s)
		if ok && len(out) < len(s) {
			t.Errorf("repair shortened %q to %q", s, out)
		}
	})
}

func FuzzClip(f *testing.F) {
	f.Add("hello", 3)
	f.Add("😀😀😀", 2)
	f.Add("", 1)
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 1 || max > 1000 {
			t.Skip()
		}
		_ = clip(s, max)
	})
}
