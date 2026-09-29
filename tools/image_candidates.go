package tools

import (
	"polaris/llm"
)

// AddImageCandidate records an image_search result in the candidate pool
// and returns its 1-based number. An image already in the pool (same
// full-size/thumbnail URL — a second search often re-surfaces the same
// photo) returns its existing number rather than a duplicate, so numbers
// the model was already told stay stable. Safe to call concurrently.
func (c *Context) AddImageCandidate(card Card) int {
	c.imageCandidatesMu.Lock()
	defer c.imageCandidatesMu.Unlock()
	key := card.FullImageURL
	if key == "" {
		key = card.ImageURL
	}
	for i, existing := range c.ImageCandidates {
		existingKey := existing.FullImageURL
		if existingKey == "" {
			existingKey = existing.ImageURL
		}
		// An empty key is a gap left by SeedImageCandidates, never a match.
		if key != "" && existingKey == key {
			return i + 1
		}
	}
	c.ImageCandidates = append(c.ImageCandidates, card)
	return len(c.ImageCandidates)
}

// SeedImageCandidates preloads the pool with earlier turns' candidates
// (element i is number i+1; a zero Card is a gap that keeps later numbers
// where the model was told they were). Called once before a turn runs — the
// model's history still holds the numbered lists from previous turns, so the
// numbers it cites must keep resolving.
func (c *Context) SeedImageCandidates(cards []Card) {
	c.imageCandidatesMu.Lock()
	defer c.imageCandidatesMu.Unlock()
	c.ImageCandidates = append([]Card(nil), cards...)
}

// ImageCandidate returns the candidate at 1-based number n, or ok=false if
// n is out of range.
func (c *Context) ImageCandidate(n int) (card Card, ok bool) {
	c.imageCandidatesMu.Lock()
	defer c.imageCandidatesMu.Unlock()
	if n < 1 || n > len(c.ImageCandidates) {
		return Card{}, false
	}
	card = c.ImageCandidates[n-1]
	if card.ImageURL == "" && card.FullImageURL == "" {
		return Card{}, false // a gap, see SeedImageCandidates
	}
	return card, true
}

// ImageCandidatesSnapshot returns a copy of the candidate pool — same
// concurrent-read rationale as CardsSnapshot.
func (c *Context) ImageCandidatesSnapshot() []Card {
	c.imageCandidatesMu.Lock()
	defer c.imageCandidatesMu.Unlock()
	out := make([]Card, len(c.ImageCandidates))
	copy(out, c.ImageCandidates)
	return out
}

// AddPendingImageMessage records a synthetic image-carrying message from a
// view_image "see" call — see PendingImageMessages' doc comment for why
// this must be flushed only after a tool-call batch's own result messages,
// never immediately. Safe to call concurrently, same reasoning as
// AddCitation/AddCard.
func (c *Context) AddPendingImageMessage(msg llm.ChatMessage) {
	c.pendingImageMu.Lock()
	defer c.pendingImageMu.Unlock()
	c.PendingImageMessages = append(c.PendingImageMessages, msg)
}

// FlushPendingImageMessages returns every pending image message gathered
// since the last flush and clears the accumulator — agent.Run calls this
// once per tool-call batch, after appending that batch's own tool-result
// messages, never before or interleaved with them.
func (c *Context) FlushPendingImageMessages() []llm.ChatMessage {
	c.pendingImageMu.Lock()
	defer c.pendingImageMu.Unlock()
	out := c.PendingImageMessages
	c.PendingImageMessages = nil
	return out
}
