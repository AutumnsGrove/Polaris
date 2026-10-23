package voice

import "testing"

func FuzzSplitIntoSpeechChunks(f *testing.F) {
	f.Add("Hello world. This is a test! Is it? Yes.")
	f.Add("**bold** and *italic* [Source](https://a.org) `code`\n# Header\nDone.")
	f.Add("")
	f.Add("...!!!???")
	f.Fuzz(func(t *testing.T, text string) {
		_ = StripMarkdown(text)
		for i, c := range SplitIntoSpeechChunks(text) {
			if c == "" {
				t.Errorf("chunk %d is empty for input %q", i, text)
			}
		}
	})
}
