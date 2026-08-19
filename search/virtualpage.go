package search

import (
	"context"
	"time"

	"polaris/store"
)

// VirtualPageSize is the fixed display-page size every provider serves
// through ResolveVirtualPage, regardless of how big a "real" page that
// provider's own API returns — SearXNG's raw page size varies with
// engine health, Brave's is a fixed 20/request. Unifying on one small
// size lets a single real fetch cover several virtual pages (SearXNG) or
// exactly two (Brave's 20/10), so a user paging through results doesn't
// force a new live request for every click.
const VirtualPageSize = 10

// cacheTTL is how long a cached real page is served without a live
// re-fetch. 24h: long enough that re-running the same search later the
// same day, paging back and forth, or reopening a tab doesn't re-hit a
// rate-limited SearXNG or a billed Brave call, short enough that results
// don't visibly go stale across days.
const cacheTTL = 24 * time.Hour

// maxRealPagesPerResolve bounds how many real pages one ResolveVirtualPage
// call will walk to satisfy a single virtual page — without this, a
// request for a very deep virtual page (or a provider that always
// reports HasMore) could transitively trigger an unbounded chain of live
// fetches. Matches web_search tool's own page-argument cap.
const maxRealPagesPerResolve = 5

// PageFetcher fetches one real, 1-indexed page live from a provider.
// SearXNGClient and brave.Client have different native shapes (variable
// count + pageno vs. fixed count=20 + offset), so ResolveVirtualPage
// stays provider-agnostic by taking this as a caller-supplied closure
// instead of depending on either concrete client.
type PageFetcher func(ctx context.Context, realPage int) (results []store.CachedSearchResult, hasMore, degraded bool, unresponsiveEngines []string, err error)

// VirtualPageResult is what ResolveVirtualPage returns — enough to both
// answer the request and observe how it was answered (CacheHit/
// RealPagesFetched exist specifically so a CLI or debug view can show
// "this came straight from cache" vs. "this cost 2 live requests").
type VirtualPageResult struct {
	Results             []store.CachedSearchResult
	Page                int
	HasMore             bool
	Degraded            bool
	UnresponsiveEngines []string
	// CacheHit is true only if every real page touched to build this
	// virtual page was served fresh from cache — a single live fetch
	// (even for just one of several real pages walked) makes this false.
	CacheHit bool
	// RealPagesFetched is how many real pages (1-indexed, so 1 means
	// "just the first real page") were consulted — walked cache-first,
	// live on miss/stale — to produce this virtual page.
	RealPagesFetched int
	// LiveFetches is how many of those real pages actually required a
	// live provider call (as opposed to being served from a fresh
	// cache entry) — the number a caller tracking API spend cares about.
	LiveFetches int
}

// ResolveVirtualPage answers a 1-indexed virtual page (VirtualPageSize
// results each) for (provider, query, category), walking real pages
// 1, 2, 3... via fetch as needed: cache-first per real page (serving
// fresh — within cacheTTL — cache immediately with no call to fetch at
// all), live on a cache miss or stale entry, and falling back to a
// stale cache entry (rather than erroring or returning nothing) if a
// live attempt itself fails or reports the provider degraded. maxResults
// is the real-page fetch size passed to fetch and used as part of the
// cache key (see store.SaveCachedSearchPage) — it is NOT the same
// number as VirtualPageSize, and different values are cached
// independently.
func ResolveVirtualPage(ctx context.Context, db *store.Store, provider, query, category string, virtualPage, maxResults int, fetch PageFetcher) (*VirtualPageResult, error) {
	if virtualPage < 1 {
		virtualPage = 1
	}
	startOffset := (virtualPage - 1) * VirtualPageSize
	endOffset := virtualPage * VirtualPageSize

	result := &VirtualPageResult{Page: virtualPage, CacheHit: true}
	var accumulated []store.CachedSearchResult

	for realPage := 1; realPage <= maxRealPagesPerResolve; realPage++ {
		results, hasMore, degraded, unresponsive, cacheHit, err := getOrFetchRealPage(ctx, db, provider, query, category, realPage, maxResults, fetch)
		if err != nil {
			return nil, err
		}
		result.RealPagesFetched = realPage
		if !cacheHit {
			result.CacheHit = false
			result.LiveFetches++
		}
		accumulated = append(accumulated, results...)
		result.HasMore = hasMore
		result.Degraded = degraded
		result.UnresponsiveEngines = unresponsive

		// Stop walking further real pages once there's enough to cover
		// this virtual slice, the provider says there's nothing more,
		// or this real page came back degraded — continuing to walk a
		// downed provider's later pages wastes a request that was never
		// going to succeed.
		if len(accumulated) >= endOffset || !hasMore || degraded {
			break
		}
	}

	if startOffset < len(accumulated) {
		end := endOffset
		if end > len(accumulated) {
			end = len(accumulated)
		}
		result.Results = accumulated[startOffset:end]
	}
	// More-available for the virtual page itself: either there's already
	// more accumulated beyond this slice (no extra request needed to
	// know that), or the last real page walked still has more to offer.
	result.HasMore = len(accumulated) > endOffset || result.HasMore

	return result, nil
}

// getOrFetchRealPage is ResolveVirtualPage's per-real-page cache lookup
// + TTL check + live fetch + stale-fallback + save. A cache read error is
// treated as a miss (log-and-continue, not fail-the-search) — same
// "best-effort, off the critical path" posture store.Store's other
// cache-ish writes (RecordSearch, IncrementAPIUsage) already take.
func getOrFetchRealPage(ctx context.Context, db *store.Store, provider, query, category string, realPage, maxResults int, fetch PageFetcher) (results []store.CachedSearchResult, hasMore, degraded bool, unresponsive []string, cacheHit bool, err error) {
	cached, hit, cacheErr := db.GetCachedSearchPage(provider, query, category, realPage, maxResults)
	if cacheErr != nil {
		blocklistLog.Warn("search cache: read failed, treating as a miss", "provider", provider, "query", query, "real_page", realPage, "err", cacheErr)
		hit = false
	}
	if hit && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Results, cached.HasMore, cached.Degraded, nil, true, nil
	}

	liveResults, liveHasMore, liveDegraded, liveUnresponsive, liveErr := fetch(ctx, realPage)
	if liveErr != nil || liveDegraded {
		if hit {
			// Stale cache as a fallback — this is exactly the scenario
			// the TTL is designed around: a temporarily degraded or
			// unreachable provider shouldn't erase what was already
			// known to work, especially since serving it costs nothing.
			blocklistLog.Info("search cache: live fetch failed/degraded, serving stale cache instead", "provider", provider, "query", query, "real_page", realPage, "live_err", liveErr, "live_degraded", liveDegraded)
			return cached.Results, cached.HasMore, cached.Degraded, nil, true, nil
		}
		if liveErr != nil {
			return nil, false, false, nil, false, liveErr
		}
		return nil, liveHasMore, liveDegraded, liveUnresponsive, false, nil
	}

	if saveErr := db.SaveCachedSearchPage(provider, query, category, realPage, maxResults, liveResults, liveHasMore, liveDegraded); saveErr != nil {
		blocklistLog.Warn("search cache: saving failed, continuing without caching this page", "provider", provider, "query", query, "real_page", realPage, "err", saveErr)
	}
	return liveResults, liveHasMore, liveDegraded, liveUnresponsive, false, nil
}
