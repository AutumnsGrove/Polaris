package gateway

import (
	"encoding/json"

	"polaris/store"
	"polaris/tools"
)

// loadImageCandidates preloads a turn's tools.Context with the thread's
// persisted image_search candidates (see store/image_candidates.go). Failure
// only costs the model the ability to cite an earlier turn's numbers — it
// can always search again — so it warns and carries on rather than failing
// the turn.
func loadImageCandidates(db *store.Store, threadID string, ctx *tools.Context) {
	blobs, err := db.LoadImageCandidates(threadID)
	if err != nil {
		log.Warn("loading image candidates failed, earlier image numbers won't resolve", "thread_id", threadID, "err", err)
		return
	}
	if len(blobs) == 0 {
		return
	}
	cards := make([]tools.Card, len(blobs))
	for i, blob := range blobs {
		if blob == "" {
			continue // a gap: keep later numbers where the model was told they were
		}
		if err := json.Unmarshal([]byte(blob), &cards[i]); err != nil {
			log.Warn("decoding a saved image candidate failed, leaving a gap", "thread_id", threadID, "num", i+1, "err", err)
			cards[i] = tools.Card{}
		}
	}
	ctx.SeedImageCandidates(cards)
}

// saveImageCandidates persists the turn's full pool (earlier turns' entries
// included — the store upserts, so unchanged rows are a no-op rewrite).
func saveImageCandidates(db *store.Store, threadID string, ctx *tools.Context) {
	cards := ctx.ImageCandidatesSnapshot()
	if len(cards) == 0 {
		return
	}
	blobs := make([]string, len(cards))
	for i, card := range cards {
		if card.ImageURL == "" && card.FullImageURL == "" && card.URL == "" {
			continue
		}
		b, err := json.Marshal(card)
		if err != nil {
			log.Warn("encoding an image candidate failed, skipping it", "thread_id", threadID, "num", i+1, "err", err)
			continue
		}
		blobs[i] = string(b)
	}
	if err := db.SaveImageCandidates(threadID, blobs); err != nil {
		log.Warn("saving image candidates failed, later turns can't cite this turn's image numbers", "thread_id", threadID, "err", err)
	}
}
