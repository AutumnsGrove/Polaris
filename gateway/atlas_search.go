package gateway

import (
	"context"

	"polaris/brave"
	"polaris/search"
	"polaris/store"
)

// AtlasSearchMeta carries observability about how a search.SearchResponse
// was actually produced — which provider answered, whether every real
// page touched came straight from cache, how many real pages/live calls
// it took. Exists specifically so a caller (the CLI especially — see
// cmd/atlas.go) can show this directly instead of it only being visible
// in server logs.
type AtlasSearchMeta struct {
	Provider         string
	CacheHit         bool
	RealPagesFetched int
	LiveFetches      int
}

// ResolveAtlasSearch is the shared core behind Atlas's own HTTP endpoint
// (handleSearch) and polaris atlas search's bare-metal CLI path — SearXNG
// first, cached and virtually paginated via search.ResolveVirtualPage;
// on a confirmed full outage, falls back to Brave (its own cache
// namespace, same virtual-paging treatment, gated by the same monthly
// usage cap web_search's own Brave fallback uses). Takes explicit
// dependencies rather than a *Server so the CLI can call it directly
// against its own locally-built clients without needing a full
// gateway.Server — see cmd/atlas.go.
func ResolveAtlasSearch(ctx context.Context, db *store.Store, searxngClient *search.SearXNGClient, braveClient *brave.Client, query, category string, page, maxResults int) (*search.SearchResponse, *AtlasSearchMeta, error) {
	fetch := searxngPageFetcher(searxngClient, query, category, maxResults)
	result, err := search.ResolveVirtualPage(ctx, db, "searxng", query, category, page, maxResults, fetch)
	if err != nil {
		log.Warn("atlas search: searxng request failed, trying brave fallback", "query", query, "category", category, "err", err)
		if resp, meta, ok := resolveAtlasBraveFallback(ctx, db, braveClient, query, page); ok {
			return resp, meta, nil
		}
		return nil, nil, err
	}

	meta := &AtlasSearchMeta{Provider: "searxng", CacheHit: result.CacheHit, RealPagesFetched: result.RealPagesFetched, LiveFetches: result.LiveFetches}

	if len(result.Results) == 0 && result.Degraded {
		// Same degraded-vs-genuinely-empty distinction web_search's own
		// fallback chain makes (see tools/web_search.go) — SearXNG's own
		// engines are down, not "this query has no results". If Brave
		// can't rescue it either, fall through and hand the caller the
		// degraded response as-is (empty results + Degraded/
		// UnresponsiveEngines set) so it can show its own "search is
		// degraded" state.
		log.Warn("atlas search: searxng degraded, trying brave fallback", "query", query, "unresponsive_engines", result.UnresponsiveEngines)
		if resp, braveMeta, ok := resolveAtlasBraveFallback(ctx, db, braveClient, query, page); ok {
			return resp, braveMeta, nil
		}
	}

	return atlasSearchResponse(query, result), meta, nil
}

// atlasSearchResponse converts a provider-agnostic VirtualPageResult
// (store.CachedSearchResult rows) into the search.SearchResponse shape
// Atlas's HTTP endpoint and CLI both already speak.
func atlasSearchResponse(query string, result *search.VirtualPageResult) *search.SearchResponse {
	results := make([]search.SearchResult, len(result.Results))
	for i, r := range result.Results {
		results[i] = search.SearchResult{
			Title:     r.Title,
			URL:       r.URL,
			Content:   r.Content,
			Score:     r.Score,
			Thumbnail: r.Thumbnail,
			Engine:    r.Engine,
			Engines:   r.Engines,
			RankState: r.RankState,
			Pinned:    r.Pinned,
		}
	}
	return &search.SearchResponse{
		Query:               query,
		Results:             results,
		Degraded:            result.Degraded,
		UnresponsiveEngines: result.UnresponsiveEngines,
		Page:                result.Page,
		HasMore:             result.HasMore,
	}
}

// searxngPageFetcher adapts search.SearXNGClient.Search to the
// search.PageFetcher shape ResolveVirtualPage expects.
func searxngPageFetcher(client *search.SearXNGClient, query, category string, maxResults int) search.PageFetcher {
	return func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		resp, err := client.Search(ctx, query, maxResults, category, realPage)
		if err != nil {
			return nil, false, false, nil, err
		}
		results := make([]store.CachedSearchResult, len(resp.Results))
		for i, r := range resp.Results {
			results[i] = store.CachedSearchResult{
				Title:     r.Title,
				URL:       r.URL,
				Content:   r.Content,
				Score:     r.Score,
				Thumbnail: r.Thumbnail,
				Engine:    r.Engine,
				Engines:   r.Engines,
				RankState: r.RankState,
				Pinned:    r.Pinned,
			}
		}
		return results, resp.HasMore, resp.Degraded, resp.UnresponsiveEngines, nil
	}
}

// resolveAtlasBraveFallback tries Brave once SearXNG has failed or
// confirmed degraded, gated by the same DB-backed monthly cap
// tools/web_search.go's own Brave fallback checks. Real-page numbering
// here is Brave's own offset+1 (see bravePageFetcher) — cached under
// provider "brave" and maxResults=brave.MaxCount, entirely separate from
// SearXNG's cache entries for the same query. Returns ok=false on any
// failure (not configured, over the cap, the fetch itself erroring, or
// a genuinely empty result) so the caller falls through to the plain
// degraded response.
func resolveAtlasBraveFallback(ctx context.Context, db *store.Store, braveClient *brave.Client, query string, page int) (*search.SearchResponse, *AtlasSearchMeta, bool) {
	if braveClient == nil {
		return nil, nil, false
	}
	used, err := db.GetAPIUsage("brave")
	if err != nil {
		log.Warn("atlas search: checking brave usage failed, skipping brave fallback", "query", query, "err", err)
		return nil, nil, false
	}
	if used >= brave.MonthlyCap {
		log.Warn("atlas search: brave monthly cap reached, skipping brave fallback", "query", query, "used", used, "cap", brave.MonthlyCap)
		return nil, nil, false
	}

	fetch := bravePageFetcher(braveClient, db, query)
	result, err := search.ResolveVirtualPage(ctx, db, "brave", query, "", page, brave.MaxCount, fetch)
	if err != nil {
		log.Warn("atlas search: brave fallback failed", "query", query, "err", err)
		return nil, nil, false
	}
	if len(result.Results) == 0 {
		return nil, nil, false
	}

	return atlasSearchResponse(query, result), &AtlasSearchMeta{Provider: "brave", CacheHit: result.CacheHit, RealPagesFetched: result.RealPagesFetched, LiveFetches: result.LiveFetches}, true
}

// bravePageFetcher adapts brave.Client.Search to the search.PageFetcher
// shape — realPage is 1-indexed (ResolveVirtualPage's own convention,
// shared with SearXNG's numbering) and converted to Brave's own
// 0-indexed offset here. Only increments the persisted usage counter
// once a request actually completes (err == nil), same reasoning as
// tools/web_search.go's braveFallback — a failed call never reached
// Brave's own billing.
func bravePageFetcher(client *brave.Client, db *store.Store, query string) search.PageFetcher {
	return func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		resp, err := client.Search(ctx, query, realPage-1)
		if err != nil {
			return nil, false, false, nil, err
		}
		if _, incErr := db.IncrementAPIUsage("brave"); incErr != nil {
			log.Warn("atlas search: recording brave usage failed", "query", query, "err", incErr)
		}
		results := make([]store.CachedSearchResult, len(resp.Results))
		for i, r := range resp.Results {
			results[i] = store.CachedSearchResult{Title: r.Title, URL: r.URL, Content: r.Content, Engine: "brave"}
		}
		return results, resp.MoreAvailable, false, nil, nil
	}
}
