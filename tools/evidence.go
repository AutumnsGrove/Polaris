package tools

import (
	"strings"
)

// AddEvidence records the raw extracted text web_read fetched for a URL
// this turn — see Evidence's doc comment. Appends rather than overwrites, so
// a multi-page PDF read across the turn (different `page` arguments, same
// URL) accumulates the union of every page actually read instead of losing
// all but the last. Skips an exact-duplicate re-read of the same text (e.g.
// the model re-reading the same page twice) rather than storing it twice.
// Safe to call concurrently, same reasoning as AddCitation/AddCard.
func (c *Context) AddEvidence(url, text string) {
	c.evidenceMu.Lock()
	defer c.evidenceMu.Unlock()
	if c.evidence == nil {
		c.evidence = map[string][]string{}
	}
	for _, existing := range c.evidence[url] {
		if existing == text {
			return
		}
	}
	c.evidence[url] = append(c.evidence[url], text)
}

// EvidenceForURL returns the raw text stored for url — every distinct piece
// recorded this turn (e.g. every PDF page actually read) joined with blank
// lines — and whether anything was stored at all. False for a URL only ever
// seen via web_search's snippet, never actually web_read. Safe to call
// concurrently.
func (c *Context) EvidenceForURL(url string) (string, bool) {
	c.evidenceMu.Lock()
	defer c.evidenceMu.Unlock()
	pieces, ok := c.evidence[url]
	if !ok {
		return "", false
	}
	return strings.Join(pieces, "\n\n"), true
}

// EvidenceSnapshot returns a copy of every URL's own joined evidence text
// recorded so far — used by agent.RunSubAgent to fold a finished
// sub-agent's own web_read/reference_lookup/youtube_transcript evidence
// back into the parent turn's Context once it returns. Each sub-agent gets
// its own zero-value Context (see agent/subagent.go's newSubAgentContext
// doc comment on why), so without this, every citation spawn_researchers
// surfaces would carry no evidence at all — confirmed live: Deep
// Research's own citations came back with "no stored evidence for this
// URL" for every claim until this existed. Safe to call concurrently.
func (c *Context) EvidenceSnapshot() map[string]string {
	c.evidenceMu.Lock()
	defer c.evidenceMu.Unlock()
	out := make(map[string]string, len(c.evidence))
	for url, pieces := range c.evidence {
		out[url] = strings.Join(pieces, "\n\n")
	}
	return out
}
