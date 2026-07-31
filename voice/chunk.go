// chunk.go prepares an answer's text for speech: strip the markdown a
// Polaris answer is normally formatted with (Kokoro would otherwise read
// "asterisk asterisk" and URLs aloud literally), then split it into
// sentence-sized pieces so the streaming /api/speak/stream endpoint can
// synthesize and start playing the first sentence long before the rest
// of a multi-paragraph answer has been through Kokoro.
package voice

import (
	"regexp"
	"strings"
)

// maxChunkChars caps a single speech chunk's length — long enough to
// usually cover a couple of short sentences, short enough that Kokoro's
// per-chunk synthesis latency stays low. That's the whole point of
// chunking at all: get audio playing quickly, not wait for one giant
// synthesis call covering the entire answer.
const maxChunkChars = 300

// sentenceEndRe splits on one or more .!? followed by whitespace (or end
// of string) — a plain heuristic, not a full sentence tokenizer ("Dr.
// Smith" or "3.14" will split early). Good enough here: a slightly early
// or late chunk boundary just changes where the audio has a small pause,
// it doesn't produce a wrong answer the way a real parsing bug would.
var sentenceEndRe = regexp.MustCompile(`[.!?]+(?:\s+|$)`)

// The markdown patterns that actually show up in a Polaris answer (see
// prompt.md's citation format and normal prose formatting) — not a full
// markdown parser, just enough to stop Kokoro from reading punctuation
// marks and raw URLs aloud.
var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdBoldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalicRe = regexp.MustCompile(`[*_]([^*_]+)[*_]`)
	mdHeaderRe = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	mdCodeRe   = regexp.MustCompile("`([^`]*)`")
)

// StripMarkdown reduces a markdown-formatted answer to plain prose: link
// text survives, the URL doesn't; bold/italic markers are dropped but the
// wrapped text stays; headers lose their leading #s; inline code loses
// its backticks.
func StripMarkdown(text string) string {
	text = mdLinkRe.ReplaceAllString(text, "$1")
	text = mdBoldRe.ReplaceAllString(text, "$1")
	text = mdItalicRe.ReplaceAllString(text, "$1")
	text = mdHeaderRe.ReplaceAllString(text, "")
	text = mdCodeRe.ReplaceAllString(text, "$1")
	return text
}

// SplitIntoSpeechChunks strips markdown (see StripMarkdown) and splits
// the result into sentence-aligned chunks, each at most maxChunkChars —
// packing multiple short sentences into one chunk where they fit, and
// letting a single sentence longer than the cap through on its own
// rather than cutting it off mid-sentence.
func SplitIntoSpeechChunks(text string) []string {
	cleaned := strings.TrimSpace(StripMarkdown(text))
	if cleaned == "" {
		return nil
	}

	var sentences []string
	last := 0
	for _, loc := range sentenceEndRe.FindAllStringIndex(cleaned, -1) {
		sentences = append(sentences, cleaned[last:loc[1]])
		last = loc[1]
	}
	if last < len(cleaned) {
		sentences = append(sentences, cleaned[last:])
	}

	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if current.Len() > 0 && current.Len()+1+len(s) > maxChunkChars {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(s)
		if current.Len() >= maxChunkChars {
			flush()
		}
	}
	flush()
	return chunks
}
