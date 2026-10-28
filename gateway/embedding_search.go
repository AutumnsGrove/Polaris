// embedding_search.go is hybrid retrieval: keyword and semantic results
// fused into one ranking.
//
// Fused rather than "semantic when available", because the two tiers fail in
// complementary ways — measured on real queries (docs/plans/embeddings.md):
// FTS5 returns nothing at all for 3 of 8 natural-language queries, while
// semantic retrieval is useless on bare single-word queries ("astronauts"
// scored 0.22 against the right star while FTS5 ranked it first). Preferring
// either one outright loses the other's cases.
package gateway

import (
	"context"
	"sort"
	"time"

	"polaris/store"
)

// rrfK is Reciprocal Rank Fusion's smoothing constant, at the value the
// original RRF paper uses. It decides how quickly score decays with rank;
// 60 is deliberately flat, so a row that both retrievers like (even
// mediocrely) outranks one that a single retriever loves. That's the
// property being bought here.
const rrfK = 60

// hybridPoolSize is how many candidates each retriever contributes before
// fusion, independent of the caller's own limit. Pulling a wider pool is
// what lets a row ranked 12th by FTS5 but 2nd semantically still surface.
const hybridPoolSize = 30

// queryEmbedTimeout bounds one search-time query embedding. Same 5s ceiling
// embed.singleEmbedTimeout applies on the research loop's own critical path:
// past that, keyword-only results are better than a stalled search.
const queryEmbedTimeout = 5 * time.Second

// fuseRRF merges ranked id lists into one ranking. Each list contributes
// 1/(rrfK + rank) per id, so the absolute scores are never compared across
// retrievers — only positions, which is the whole point: bm25 and cosine
// live on incomparable scales (cosine compresses into 0.2–0.7 on this
// corpus), and any attempt to weight them would need per-query tuning.
//
// Ties break on lower id to keep output stable run to run; identical
// rankings from both retrievers are the common case for exact-keyword
// queries.
func fuseRRF(lists ...[]int64) []int64 {
	scores := make(map[int64]float64, len(lists)*len(lists))
	for _, list := range lists {
		for i, id := range list {
			scores[id] += 1 / float64(rrfK+i+1)
		}
	}
	ids := make([]int64, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool {
		if scores[ids[a]] != scores[ids[b]] {
			return scores[ids[a]] > scores[ids[b]]
		}
		return ids[a] < ids[b]
	})
	return ids
}

// embedQuery embeds one search query, returning nil (and no error) whenever
// the semantic tier simply isn't available — no service, nothing indexed
// yet, or the provider unreachable. Callers treat nil as "keyword-only for
// this call", which is exactly today's behavior, so a broken provider can
// never make search worse than it was before this subsystem existed.
func (v *embedService) embedQuery(ctx context.Context, text string) []float32 {
	if v == nil {
		return nil
	}
	// EmbedBatch rather than Embed: it returns the provider's reported cost,
	// and a query is one text. The timeout is this path's own hard ceiling,
	// same reasoning (and same 5s) as embed.singleEmbedTimeout — a search
	// must not stall on a slow embedding, it must fall back to keyword-only.
	ctx, cancel := context.WithTimeout(ctx, queryEmbedTimeout)
	defer cancel()

	res, err := v.client.EmbedBatch(ctx, []string{text})
	if err != nil {
		log.Warn("embeddings: query embedding failed, falling back to keyword search", "err", err)
		return nil
	}
	// One aux row per query embedding. Ideally this would be ctx.AddCost on
	// the live turn, but this seam (a store-shaped closure the tools call)
	// has no turn to bill — and the money lands in the same Embeddings bucket
	// either way, which is what the settings panel shows.
	if res.CostUSD > 0 {
		if err := v.db.RecordAuxCost(store.AuxKindEmbedding, res.CostUSD); err != nil {
			log.Warn("embeddings: recording query embedding cost failed", "err", err)
		}
	}
	if len(res.Vectors) == 0 {
		return nil
	}
	return res.Vectors[0]
}

// SearchStarsHybrid backs Weaver's search_stars: keyword and semantic leads
// fused, rejected stars included (see store.Store.SearchStars).
func (v *embedService) SearchStarsHybrid(ctx context.Context, query string, limit int) ([]store.StarSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	fts, err := v.db.SearchStars(query, hybridPoolSize)
	if err != nil {
		return nil, err
	}
	ids := fuseRRF(idsOfStarLeads(fts), v.semanticStarIDs(ctx, query))

	rows, err := v.db.StarsByIDs(ids)
	if err != nil {
		return nil, err
	}
	ordered := orderStarLeadsByIDs(rows, ids)
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

// SearchLibraryStarsHybrid backs the `stars` tool and the Library's search
// box: same fusion, but only auto/confirmed stars, so a rejected or hidden
// star can never render as a Library card (see store.SearchLibraryStars).
func (v *embedService) SearchLibraryStarsHybrid(ctx context.Context, query string, limit int) ([]store.Star, error) {
	if limit <= 0 {
		limit = 10
	}
	fts, err := v.db.SearchLibraryStars(query, hybridPoolSize)
	if err != nil {
		return nil, err
	}
	ftsIDs := make([]int64, len(fts))
	for i, s := range fts {
		ftsIDs[i] = s.ID
	}
	ids := fuseRRF(ftsIDs, v.semanticStarIDs(ctx, query))

	rows, err := v.db.LibraryStarsByIDs(ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]store.Star, len(rows))
	for _, s := range rows {
		byID[s.ID] = s
	}
	ordered := make([]store.Star, 0, len(rows))
	for _, id := range ids {
		if s, ok := byID[id]; ok {
			ordered = append(ordered, s)
		}
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

// semanticStarIDs returns the star ids the vector cache ranks for query, or
// nil when the semantic tier is unavailable.
//
// Deliberately one call for both star surfaces rather than sharing a fused
// cache: the two differ only in which statuses are eligible, and applying
// status filters afterwards (in StarsByIDs / LibraryStarsByIDs) keeps each
// surface's own rule in the place it already lives.
func (v *embedService) semanticStarIDs(ctx context.Context, query string) []int64 {
	if v == nil || v.CachedCount(store.EmbeddingKindStar) == 0 {
		return nil
	}
	vec := v.embedQuery(ctx, query)
	if vec == nil {
		return nil
	}
	hits := v.Search(store.EmbeddingKindStar, vec, hybridPoolSize)
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.RefID
	}
	return ids
}

func idsOfStarLeads(leads []store.StarSearchResult) []int64 {
	ids := make([]int64, len(leads))
	for i, l := range leads {
		ids[i] = l.ID
	}
	return ids
}

// orderStarLeadsByIDs reorders fetched rows to match the fused ranking
// (SQL's IN doesn't preserve order), dropping any id the fetch didn't
// return.
func orderStarLeadsByIDs(rows []store.StarSearchResult, ids []int64) []store.StarSearchResult {
	byID := make(map[int64]store.StarSearchResult, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]store.StarSearchResult, 0, len(rows))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// searchStarsHybrid is the nil-safe entry point: FTS-only when there's no
// embedding service, hybrid when there is. Keeping the branch here rather
// than at each call site means a nil service can never accidentally be
// dereferenced by a caller that forgot to check.
func searchStarsHybrid(svc *embedService, db *store.Store, ctx context.Context, query string, limit int) ([]store.StarSearchResult, error) {
	if svc == nil {
		return db.SearchStars(query, limit)
	}
	return svc.SearchStarsHybrid(ctx, query, limit)
}

func searchLibraryStarsHybrid(svc *embedService, db *store.Store, ctx context.Context, query string, limit int) ([]store.Star, error) {
	if svc == nil {
		return db.SearchLibraryStars(query, limit)
	}
	return svc.SearchLibraryStarsHybrid(ctx, query, limit)
}

// SearchChatsHybrid backs the `search_chats` tool (and Weaver's copy of it):
// keyword and semantic message hits fused, message-level, in the same result
// shape the tool already renders.
//
// Deliberately NOT used by the sidebar's own search box
// (gateway/threads.go's handleSearchThreads). That one is a human typing
// short prefixes, where FTS5's prefix matching is exactly right and a
// ~0.5s query-embedding round trip per keystroke would be pure cost. Hybrid
// retrieval is for the model's paraphrased questions, which is where the
// measured wins are.
func (v *embedService) SearchChatsHybrid(ctx context.Context, query string, limit int) ([]store.MessageSearchResult, error) {
	if limit <= 0 {
		limit = 30
	}
	fts, err := v.db.SearchMessages(query, hybridPoolSize)
	if err != nil {
		return nil, err
	}
	ftsIDs := make([]int64, len(fts))
	for i, r := range fts {
		ftsIDs[i] = r.MessageID
	}

	var semIDs []int64
	if v.CachedCount(store.EmbeddingKindMessage) > 0 {
		if vec := v.embedQuery(ctx, query); vec != nil {
			for _, h := range v.Search(store.EmbeddingKindMessage, vec, hybridPoolSize) {
				semIDs = append(semIDs, h.RefID)
			}
		}
	}

	ids := fuseRRF(ftsIDs, semIDs)

	// Keyword hits keep FTS5's own snippet, highlight markers included — only
	// a row the keyword tier DIDN'T return needs the plain-text preview
	// MessagesByIDs builds. Re-fetching everything would silently strip the
	// highlights from every exact match, which is what the first version of
	// this did (TestSearchChatsHybrid_ExactMatchKeepsItsHighlight caught it).
	byID := make(map[int64]store.MessageSearchResult, len(ids))
	for _, r := range fts {
		byID[r.MessageID] = r
	}
	var semanticOnly []int64
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			semanticOnly = append(semanticOnly, id)
		}
	}
	if len(semanticOnly) > 0 {
		// Fetched by id through MessagesByIDs rather than trusted from the
		// vector cache: that re-applies the full eligibility predicate, so a
		// stale vector for a since-hidden or hard-deleted thread can't
		// surface here.
		rows, err := v.db.MessagesByIDs(semanticOnly)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			byID[r.MessageID] = r
		}
	}

	ordered := make([]store.MessageSearchResult, 0, len(byID))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

// searchChatsHybrid is the nil-safe entry point, same shape as
// searchStarsHybrid: FTS-only when no embedding service exists.
func searchChatsHybrid(svc *embedService, db *store.Store, ctx context.Context, query string, limit int) ([]store.MessageSearchResult, error) {
	if svc == nil {
		return db.SearchMessages(query, limit)
	}
	return svc.SearchChatsHybrid(ctx, query, limit)
}
