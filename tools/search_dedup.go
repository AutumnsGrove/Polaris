package tools

import (
	"fmt"
	"strings"
)

// searchDedupKey builds the key dedupedCall shares concurrent calls on —
// normalized so cosmetic differences (case, incidental whitespace) in two
// sub-agents' phrasing of "the same" query still collide, while every
// parameter that actually changes what's returned (provider, category,
// page, max_results) keeps them distinct. See docs/plans/deep-research-
// two-tier.md's "Budget (session-level)" section: "two sub-agents issuing
// the same or near-identical query in one session cost one real API
// call."
func searchDedupKey(provider, query, category string, page, maxResults int) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	return fmt.Sprintf("%s|%s|%s|%d|%d", provider, normalized, category, page, maxResults)
}

// dedupedCall runs fn through ctx.SearchDedup (Tier 2 Deep Research's
// session-scoped singleflight.Group — see Context.SearchDedup's doc
// comment) keyed by key, so two sub-agent goroutines issuing the same
// search concurrently trigger one real fn execution and share its
// result. When ctx.SearchDedup is nil (every non-sub-agent caller), fn
// always runs directly and shared is always false — no behavior change
// from before dedup existed.
//
// shared reports whether this specific call received another in-flight
// caller's result rather than triggering fn itself — callers use this to
// avoid double-counting ResearchBudget for a call that never actually
// happened on the wire.
func dedupedCall[T any](ctx *Context, key string, fn func() (T, error)) (result T, shared bool, err error) {
	if ctx.SearchDedup == nil {
		result, err = fn()
		return result, false, err
	}
	v, sfErr, sh := ctx.SearchDedup.Do(key, func() (interface{}, error) {
		return fn()
	})
	if v != nil {
		result = v.(T)
	}
	return result, sh, sfErr
}
