package tools

import "sync"

// researchBudgetSoftThreshold/researchBudgetSoftThresholdFallback/
// researchBudgetHardCeiling tune ResearchBudget — see
// docs/plans/deep-research-two-tier.md's "Budget (session-level)" section.
// The soft threshold is provider-aware: fallback usage (Brave/Parallel/
// Tavily, only reached once SearXNG has confirmed a full outage — see
// search.SearXNGClient) is the actually expensive scenario, not raw call
// count against free SearXNG, so it gets a much lower bar. The hard
// ceiling is a circuit breaker only, set well above the soft mark (~3x)
// since it exists purely against a genuine runaway/bug case, not as the
// normal stopping point — that's the soft nudge's job.
const (
	researchBudgetSoftThreshold         = 50
	researchBudgetSoftThresholdFallback = 15
	researchBudgetHardCeiling           = 150
)

// ResearchBudget is a shared, session-scoped counter of search calls
// across every sub-agent in one Tier 2 Deep Research session — not a
// global; one instance is created per session and threaded into each
// sub-agent's Context.ResearchBudget field so a query fan-out shares one
// count instead of each sub-agent tracking its own. Layers in front of,
// not instead of, the existing api_usage monthly caps (brave.MonthlyCap,
// parallelMonthlyCap in web_search.go) — this is a per-session guardrail,
// the monthly ceiling enforcement is unchanged. Safe for concurrent use
// from multiple sub-agent goroutines.
type ResearchBudget struct {
	mu            sync.Mutex
	totalCalls    int
	fallbackCalls int
	softNudged    bool
}

// NewResearchBudget returns a fresh, empty budget for one Deep Research
// session.
func NewResearchBudget() *ResearchBudget {
	return &ResearchBudget{}
}

// Allowed reports whether a new search call should be permitted — false
// only once the hard ceiling has already been reached by prior calls.
// Callers should check this before making a search call, not after.
func (b *ResearchBudget) Allowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalCalls < researchBudgetHardCeiling
}

// RecordCall registers one completed search call — fallback should be
// true when it went through Brave/Parallel/Tavily rather than free
// SearXNG. It reports whether this specific call is the one that first
// crosses the soft nudge threshold, true at most once per ResearchBudget,
// so the caller injects exactly one check-in message to the orchestrator
// rather than one per call after crossing.
func (b *ResearchBudget) RecordCall(fallback bool) (softNudgeNow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalCalls++
	if fallback {
		b.fallbackCalls++
	}
	if b.softNudged {
		return false
	}

	threshold, count := researchBudgetSoftThreshold, b.totalCalls
	if b.fallbackCalls > 0 {
		// Any fallback usage this session switches the check to the
		// lower, fallback-specific bar — see the const block's doc
		// comment for why fallback count, not total count, is the
		// expensive signal once it's in play at all.
		threshold, count = researchBudgetSoftThresholdFallback, b.fallbackCalls
	}
	if count >= threshold {
		b.softNudged = true
		return true
	}
	return false
}

// Summary returns calls-so-far as (total, fallback) — the shape the
// check-in nudge message reports (see prompts.yaml's research_check_in
// for the analogous per-sub-agent phrasing this mirrors one level up).
func (b *ResearchBudget) Summary() (total, fallback int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalCalls, b.fallbackCalls
}
