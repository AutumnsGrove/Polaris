package tools

import "strings"

// Recommendation tools (books, movies, music) share image_search's
// judge-then-show flow: nothing they find is displayed until the model picks
// items with highlight (image_index) or show (image_indices). See
// docs/plans/recommendation-candidates.md.

// recommendationsFooter closes every recommendation tool's text result. The
// model can't see the screen, so this is where it learns the user has seen
// nothing yet — without it, a model that skips the display step answers with
// a bare list of titles and no covers.
const recommendationsFooter = "The user has NOT seen any of these yet — nothing is displayed until you call " +
	"highlight (image_index per item, with a one-sentence why; up to 5; the usual choice for recommendations) " +
	"or show (image_indices, a cover gallery). The numbers above are the image_index values. Put only the " +
	"ones you'd actually recommend on screen, and don't re-list in prose what you've displayed."

// AddRecommendationCandidate records a recommendation in the shared
// candidate pool and returns its 1-based number. Unlike AddImageCandidate it
// dedups by page URL (title+subtitle when there is none), not image URL:
// Deezer returns the parent album's cover for a track, so several different
// recommended tracks routinely share one image and must keep distinct
// numbers. Safe to call concurrently.
func (c *Context) AddRecommendationCandidate(card Card) int {
	if card.Kind == "" {
		card.Kind = "image" // renders in show's gallery like any pooled candidate
	}
	c.imageCandidatesMu.Lock()
	defer c.imageCandidatesMu.Unlock()
	for i, existing := range c.ImageCandidates {
		if recommendationKey(existing) == recommendationKey(card) && recommendationKey(card) != "" {
			return i + 1
		}
	}
	c.ImageCandidates = append(c.ImageCandidates, card)
	return len(c.ImageCandidates)
}

func recommendationKey(card Card) string {
	if card.URL != "" {
		return card.URL
	}
	if card.Title == "" {
		return ""
	}
	return card.Title + "\x00" + card.Subtitle
}

// poolRecommendations pools every card and returns their numbers in order.
func poolRecommendations(ctx *Context, cards []Card) []int {
	numbers := make([]int, len(cards))
	for i, card := range cards {
		numbers[i] = ctx.AddRecommendationCandidate(card)
	}
	return numbers
}

// recNumber is the pool number for list position i, falling back to the
// position when a formatter is handed no numbers (tests, empty pools).
func recNumber(numbers []int, i int) int {
	if i < len(numbers) {
		return numbers[i]
	}
	return i + 1
}

// withRecommendationsFooter appends the display instruction to a result that
// actually listed candidates; an empty result gets nothing to act on.
func withRecommendationsFooter(result string, numbers []int) string {
	if len(numbers) == 0 {
		return result
	}
	return strings.TrimSpace(result) + "\n\n" + recommendationsFooter
}

// coverNote is a short per-candidate marker so the model knows whether
// show's gallery will have a cover for it or only highlight can carry it.
func coverNote(card Card) string {
	if card.ImageURL == "" && card.FullImageURL == "" {
		return " [no cover found]"
	}
	return ""
}
