package tools

// Card is a structured rich-result item — an image, a title, an optional
// subtitle, and a link — meant to be rendered as its own visual block
// (e.g. a carousel) rather than woven into the model's prose or listed as
// a text citation. General-purpose: music's recommendation cards are the
// first user, but nothing here is music-specific, so a future tool (repo
// cards, place cards with photos) can populate the same field.
type Card struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	URL      string `json:"url"`
	// Kind selects which frontend treatment renders this card. Empty/
	// omitted means "media" — today's carousel behavior, unchanged for
	// every existing caller (music/movies/books never set this field).
	// image_search is the only "image" caller — see its doc comment.
	// highlight is the only "highlight" caller — see highlight.go.
	Kind string `json:"kind,omitempty"` // "" (media, default) | "image" | "highlight"
	// FullImageURL is a higher-resolution image than ImageURL's deliberately
	// small thumbnail — set only by image_search (Kind "image"), for a
	// lightbox/full-screen preview to use instead of upscaling the
	// thumbnail. Empty falls back to ImageURL on the frontend.
	FullImageURL string `json:"full_image_url,omitempty"`
	// Price is set only by highlight (Kind "highlight") — optional free
	// text ("$129.99", "£45", "~$40, limited stock"), not a structured
	// amount+currency pair, since nothing downstream sorts or computes on
	// it — see highlight.go's doc comment. Empty for every other caller
	// and for any non-shopping highlight item.
	Price string `json:"price,omitempty"`
	// Why is set only by highlight (Kind "highlight") — a one-sentence
	// reason this pick fits what was asked, rendered on the card itself.
	// It exists specifically so per-item reasoning has somewhere to live
	// other than the model's text reply — without it, a request that
	// needs justification per item (e.g. "which of these fits me best and
	// why") pushed the model to restate every card's title/url in prose
	// alongside the cards themselves, duplicating the whole answer. See
	// highlight.go's doc comment.
	Why string `json:"why,omitempty"`
}

// AddCard appends a card unless its URL is already present, same
// dedup-by-URL rationale as AddCitation. Safe to call concurrently.
func (c *Context) AddCard(card Card) {
	c.cardsMu.Lock()
	defer c.cardsMu.Unlock()
	for _, existing := range c.Cards {
		if existing.URL == card.URL {
			return
		}
	}
	c.Cards = append(c.Cards, card)
}

// CardsSnapshot returns a copy of the cards gathered so far — same
// concurrent-read rationale as CitationsSnapshot.
func (c *Context) CardsSnapshot() []Card {
	c.cardsMu.Lock()
	defer c.cardsMu.Unlock()
	out := make([]Card, len(c.Cards))
	copy(out, c.Cards)
	return out
}
