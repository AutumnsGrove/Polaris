package tools

// PageStats is what web_read measured from a page's *full* extracted text at
// fetch time, before FilterExtractedText summarized it and before windowText
// trimmed it for display. The `readings` block (docs/plans/intelligent-ui.md)
// shows a reader how long a page is; the model's own view of the page is a
// filtered subset, so its output can say nothing about length (a two-line
// answer to a question, from a 15-minute article).
type PageStats struct {
	// Words is the full extracted word count. Display divides this by ~230 wpm
	// (see web/src/lib/components/ui/UiReadings.svelte). Zero for a PDF: only
	// one page's text was extracted (ExtractPDFPage), so there is no full text
	// to count — Pages carries the real signal instead.
	Words int
	// Pages is a PDF's own page count from the file. Zero for anything else.
	Pages int
	// Reliable is false when the fetched text looked like a paywall stub or an
	// empty JS shell: the text is real but too short to measure, and a word
	// count off it would read as "1 min" for a long article. A false here
	// suppresses the minutes but does not un-open the page.
	Reliable bool
}

// AddPageStats records what web_read measured for url. Last write wins (unlike
// AddEvidence's append): re-reading a URL this turn measures it again, and the
// later fetch is the number worth showing. Safe to call concurrently, same
// reasoning as AddEvidence/AddCitation.
func (c *Context) AddPageStats(url string, s PageStats) {
	c.pageStatsMu.Lock()
	defer c.pageStatsMu.Unlock()
	if c.pageStats == nil {
		c.pageStats = map[string]PageStats{}
	}
	c.pageStats[url] = s
}

// PageStatsForURL returns the measurement recorded for url, and whether
// web_read actually opened that page this turn (any recorded stats — even an
// unreliable one — means it was opened). URLs are matched exactly as passed to
// web_read, which is also the key AddEvidence uses. Safe to call concurrently.
func (c *Context) PageStatsForURL(url string) (PageStats, bool) {
	c.pageStatsMu.Lock()
	defer c.pageStatsMu.Unlock()
	s, ok := c.pageStats[url]
	return s, ok
}

// citationWithMeasurement fills cit's Opened/Words/Pages from the page
// measurement web_read recorded for its URL, if any. A citation that never
// went through web_read (a search snippet, a Maps place) is returned as-is,
// and so is not "opened" — it has no evidence. Words is only copied when the
// fetch was reliable, so a paywall stub suppresses the minutes without
// un-opening the page.
func citationWithMeasurement(c *Context, cit Citation) Citation {
	s, opened := c.PageStatsForURL(cit.URL)
	if !opened {
		return cit
	}
	cit.Opened = true
	cit.Pages = s.Pages
	if s.Reliable {
		cit.Words = s.Words
	}
	return cit
}
