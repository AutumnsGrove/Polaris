package tools

type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// SiteName is the publisher/site label from the page's own
	// og:site_name meta tag (e.g. "The Hollywood Reporter"), when web_read
	// fetched the page and it set one — empty for citations that never
	// went through a page fetch (web_search hits, Maps places, weather).
	// The frontend falls back to a hostname-derived label when this is
	// empty, see web/src/lib/citations.ts.
	SiteName string `json:"site_name,omitempty"`

	// ImageURL is an optional thumbnail (album art, a repo's avatar, an
	// article's lead image, etc.) the frontend renders in place of the
	// source list's numbered index badge when present — general-purpose
	// across any tool, not specific to one. Empty means "no image", the
	// normal case; a tool sets this only when it has a real, working image
	// URL in hand already (see tools/music.go's Deezer cover-art
	// enrichment), never a fabricated/guessed one. Usually per-item (an
	// article's own lead photo), but a shared source-identity badge is a
	// legitimate use too — see reference_lookup.go's arxivLogoURL, the
	// same static image on every arXiv citation on purpose, so it reads
	// as "this came from arXiv" at a glance rather than nothing at all.
	ImageURL string `json:"image_url,omitempty"`
}

// AddCitation appends a citation unless its URL is already present —
// web_search and web_read routinely surface the same URL (a search hit
// that then gets read in full), and duplicate source badges in the UI
// look like a bug rather than an accurate source list. Safe to call
// concurrently from multiple tool handlers dispatched in parallel.
func (c *Context) AddCitation(cit Citation) {
	c.citationsMu.Lock()
	defer c.citationsMu.Unlock()
	for _, existing := range c.Citations {
		if existing.URL == cit.URL {
			return
		}
	}
	c.Citations = append(c.Citations, cit)
}

// CitationsSnapshot returns a copy of the citations gathered so far. Tool
// handlers use this — not a direct ctx.Citations read — when building an
// emit payload mid-dispatch, since with parallel tool calls another
// goroutine's AddCitation could be appending at that exact instant; an
// unsynchronized read there would race with it. A direct read of
// ctx.Citations is still fine once all of a turn's dispatches have joined
// (see agent.Run, after its sync.WaitGroup.Wait returns) — that point is
// provably sequential with every AddCitation that ran before it.
func (c *Context) CitationsSnapshot() []Citation {
	c.citationsMu.Lock()
	defer c.citationsMu.Unlock()
	out := make([]Citation, len(c.Citations))
	copy(out, c.Citations)
	return out
}
