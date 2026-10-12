package threadexport

// EstimateTokens approximates the document's size as characters/4 — the
// modal's deliberately rough "will this fit a context window?" number, not
// a real tokenizer (docs/plans/thread-export.md). It sums the same text the
// renderers emit, so it is computed from the built Doc without rendering
// either format; the two real outputs differ slightly (markup, JSON field
// names and escaping), which is well within the estimate's error bars.
func EstimateTokens(d Doc) int {
	chars := len(d.Thread.Title)
	if d.Summary != nil {
		chars += len(d.Summary.Text)
	}
	for _, t := range d.Turns {
		for _, e := range [2]*Entry{t.User, t.Assistant} {
			if e == nil {
				continue
			}
			chars += len(e.Content)
			for _, c := range e.Citations {
				chars += len(c.Title) + len(c.URL) + len(c.SiteName)
			}
			for _, a := range e.Attachments {
				chars += len(a.Filename) + len(a.ContentType)
			}
		}
	}
	return (chars + 3) / 4
}
