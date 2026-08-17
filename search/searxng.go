// Package search wraps a self-hosted SearXNG instance for web search.
// Ported from her-go's search/searxng.go.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// rrfK is the damping constant from the original Reciprocal Rank Fusion
// paper (Cormack et al., 2009) — large enough that the difference between
// e.g. rank 1 and rank 2 doesn't dominate the fused score, so a result
// several engines agree on (even at middling positions) can outrank one
// only a single engine ranks first.
const rrfK = 60.0

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	// Score is a Reciprocal Rank Fusion value, not a 0-1 relevance
	// score — see rrfScore. It's meaningful only for relative ordering
	// between results in the same response, not as an absolute number.
	Score     float64  `json:"score"`
	Thumbnail string   `json:"thumbnail,omitempty"`
	Engine    string   `json:"engine,omitempty"`
	Engines   []string `json:"engines,omitempty"`
	// RankState and Pinned reflect this result's domain-ranking state (see
	// DomainRankings) — surfaced so the ranking popover can show a result's
	// current state without a second lookup. RankState is "default" when
	// the domain has no explicit entry (or no DomainRankings is configured
	// at all).
	RankState string `json:"rank_state,omitempty"`
	Pinned    bool   `json:"pinned,omitempty"`
}

type SearchResponse struct {
	Query   string         `json:"query"`
	Answer  string         `json:"answer,omitempty"`
	Results []SearchResult `json:"results"`
	// Degraded is true only when EVERY engine SearXNG queried for this
	// category failed — not just one of several. A single engine being
	// rate-limited while the others are fine still produces normal
	// results (or a normal, genuine empty result if the healthy engines
	// really do have nothing for this query); that's not an outage, and
	// shouldn't spend a Tavily fallback credit on what's actually just
	// this query having no coverage. Only "nothing came back because
	// nothing *could*" counts. See generalCategoryEngineCount's comment
	// for why this is a known count rather than something derived from
	// the response itself.
	Degraded bool `json:"degraded,omitempty"`
	// UnresponsiveEngines is always populated when SearXNG reports any,
	// regardless of Degraded — useful for logging even when other engines
	// still returned enough to answer the query.
	UnresponsiveEngines []string `json:"unresponsive_engines,omitempty"`
	// RetryAfter is set only on a response served from the cooldown
	// short-circuit (see SearXNGClient.cooldownUntil) — when the real
	// engines will actually be tried again, so a caller can tell the
	// user/model roughly how long to wait instead of an open-ended
	// "try again later".
	RetryAfter time.Time `json:"retry_after,omitempty"`
}

// generalCategoryEngineCount is how many engines SearXNG's "general"
// category (category == "") actually queries on this deployment —
// brave, duckduckgo, google cse, and startpage, confirmed empirically
// against the real instance (curl .../search?format=json against a
// scratch query showed exactly these 4 in unresponsive_engines during a
// full outage). SearXNG's JSON API has no field reporting "how many
// engines are configured for this category" — unresponsive_engines only
// lists the ones that failed, so there's no way to derive "all of them
// failed" from the response alone without knowing the total up front.
//
// This needs updating by hand if the engine set for "general" ever
// changes (compose/searxng/settings.yml, dev/searxng/settings.yml use
// use_default_settings: true, so SearXNG's own upstream defaults decide
// this, not a file in this repo). A stale-but-too-high count just means
// Degraded under-fires (a real full outage briefly gets reported as a
// plain empty result instead) rather than over-firing on a single
// engine's hiccup — the safer direction to be wrong in, given Tavily's
// fallback credits are the scarce resource this whole check protects.
const generalCategoryEngineCount = 4

// degradedCooldown is how long Search stops actually contacting SearXNG
// after detecting a full outage (Degraded), before trying it again.
// Repeatedly hitting an instance whose engines are already rate-limited
// or CAPTCHA'd doesn't help it recover — it very plausibly makes things
// worse, and it definitely burns time on requests that were never going
// to succeed. 20 minutes is a starting guess, not measured against how
// long these providers' own suspensions actually last; adjust if it
// turns out to be too short (still hitting the same outage) or too long
// (SearXNG's clearly fine again but nothing tries it for the rest of
// the window).
const degradedCooldown = 20 * time.Minute

type SearXNGClient struct {
	baseURL            string
	http               *http.Client
	blocklist          *Blocklist
	domainRankingsPath string

	// cooldownUntil/cooldownMu implement a simple circuit breaker across
	// calls — see degradedCooldown. In-memory only (resets on a process
	// restart), which is an acceptable cost: a restart is rare, and the
	// alternative (persisting this to the DB) doesn't buy anything a
	// fixed wait doesn't already provide on its own. Applies to every
	// category once tripped, not just whichever one triggered it — the
	// actual failure mode this protects against (the whole box's
	// outbound IP getting rate-limited/CAPTCHA'd) affects every engine
	// regardless of which category a given query used.
	cooldownMu    sync.Mutex
	cooldownUntil time.Time
}

// NewSearXNGClient builds a client for the given SearXNG instance.
// blocklist may be nil — Search then applies no filtering. Chain
// WithDomainRankings to enable the 5-state ranking system on top.
func NewSearXNGClient(baseURL string, blocklist *Blocklist) *SearXNGClient {
	return &SearXNGClient{
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		http:      &http.Client{Timeout: 15 * time.Second},
		blocklist: blocklist,
	}
}

// inCooldown reports whether Search should currently short-circuit
// without contacting SearXNG at all, and until when.
func (c *SearXNGClient) inCooldown() (bool, time.Time) {
	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()
	return time.Now().Before(c.cooldownUntil), c.cooldownUntil
}

// startCooldown begins (or restarts) the cooldown window from now —
// called once Search itself confirms a full outage.
func (c *SearXNGClient) startCooldown() {
	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()
	c.cooldownUntil = time.Now().Add(degradedCooldown)
}

// WithDomainRankings enables domain ranking (Block/Lower/Default/Raise/Pin)
// on this client, hot-reloaded from path on every Search call — see
// DomainRankings/LoadDomainRankings. A separate method rather than a
// NewSearXNGClient parameter so existing call sites (tests especially,
// which mostly don't care about ranking) don't all need updating.
func (c *SearXNGClient) WithDomainRankings(path string) *SearXNGClient {
	c.domainRankingsPath = path
	return c
}

// DomainRankingsPath returns the file this client's ranking is loaded
// from ("" if WithDomainRankings was never called) — so a caller writing
// a ranking change (see SetDomainRanking) always targets exactly the file
// Search itself reads, rather than re-deriving the path from config and
// risking the two drifting apart.
func (c *SearXNGClient) DomainRankingsPath() string {
	return c.domainRankingsPath
}

type searxngResponse struct {
	Query   string          `json:"query"`
	Results []searxngResult `json:"results"`
	// UnresponsiveEngines is SearXNG's own [["engine", "reason"], ...]
	// shape (e.g. ["brave", "Suspended: too many requests"]) — a plain
	// [][]string rather than a named struct since JSON can't decode a
	// 2-element array into named fields, and the reason string is only
	// ever used for logging, not branched on.
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
}

type searxngResult struct {
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Content   string   `json:"content"`
	Thumbnail string   `json:"thumbnail"`
	Engine    string   `json:"engine"`
	Engines   []string `json:"engines"`
	// Positions is each contributing engine's own 1-indexed rank for this
	// result — SearXNG already merges near-duplicate results across engines
	// before returning them, so a result two engines both ranked highly
	// arrives here as one entry with e.g. positions [1, 2], not two
	// separate entries. This is what rrfScore fuses on.
	Positions []int `json:"positions"`
}

// rrfScore fuses per-engine rank positions into a single score via
// Reciprocal Rank Fusion: 1/(k+rank) per engine, summed. Unlike SearXNG's
// own raw `score` field, this never compares magnitudes across engines —
// it only uses each engine's ranking of its own results, which is the one
// signal that's actually comparable when merging DuckDuckGo/Brave/Bing
// News/etc, whose native scores live on unrelated scales (and which don't
// all report a score at all).
func rrfScore(positions []int) float64 {
	var score float64
	for _, p := range positions {
		score += 1.0 / (rrfK + float64(p))
	}
	return score
}

// Search performs a web search via SearXNG and returns up to maxResults
// relevance-ranked results. SearXNG doesn't produce an AI-generated
// answer summary, so Answer is always empty here (unlike Tavily).
// category filters which SearXNG engines answer the query — "" (SearXNG's
// default "general" category) searches ordinary web-indexed pages, which
// for broad queries like "atlanta ga news" routinely surface each outlet's
// homepage rather than a specific story, since the homepage's title/text
// matches a broad phrase just as well as any article does. "news" routes
// to dedicated news-search engines (Google News, Bing News, etc.), which
// index actual articles rather than static pages.
func (c *SearXNGClient) Search(ctx context.Context, query string, maxResults int, category string) (*SearchResponse, error) {
	if cooling, until := c.inCooldown(); cooling {
		blocklistLog.Info("searxng: skipping request, still cooling down after a full outage", "query", query, "retry_after", until)
		return &SearchResponse{Query: query, Degraded: true, RetryAfter: until}, nil
	}

	if maxResults <= 0 {
		maxResults = 5
	}

	u := fmt.Sprintf("%s/search?format=json&q=%s", c.baseURL, url.QueryEscape(query))
	if category != "" {
		u += "&categories=" + url.QueryEscape(category)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng error (status %d): %s", resp.StatusCode, string(body))
	}

	var searxngResp searxngResponse
	if err := json.Unmarshal(body, &searxngResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	rankings := LoadDomainRankings(c.domainRankingsPath)

	results := make([]SearchResult, 0, len(searxngResp.Results))
	for i, r := range searxngResp.Results {
		// Filtered out before ranking/truncation, not after — a blocked
		// result sitting early in SearXNG's own ordering would otherwise
		// crowd a legitimate result off the end of a maxResults-capped list.
		// Both the curated Blocklist and a user's own "block" ranking are
		// hard excludes, checked the same way.
		if c.blocklist.Blocked(r.URL) {
			continue
		}
		state := rankings.State(r.URL)
		if state == RankBlock {
			continue
		}

		positions := r.Positions
		if len(positions) == 0 {
			// Defensive fallback for responses that omit positions (older
			// SearXNG versions, or hand-built test fixtures) — treat this
			// result's place in SearXNG's own merged list as its one position.
			positions = []int{i + 1}
		}
		score := rrfScore(positions)
		switch state {
		case RankRaise:
			score *= raiseMultiplier
		case RankLower:
			score *= lowerMultiplier
		}

		results = append(results, SearchResult{
			Title:     r.Title,
			URL:       r.URL,
			Content:   r.Content,
			Score:     score,
			Thumbnail: r.Thumbnail,
			Engine:    r.Engine,
			Engines:   r.Engines,
			RankState: string(state),
			Pinned:    state == RankPin,
		})
	}

	// Stable sort: pinned results first (in their own relative fused-score
	// order), then everyone else by fused score descending. Results tied
	// on score (common for single-engine, single-position results near the
	// tail) keep SearXNG's own relative order rather than shuffling
	// arbitrarily.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Pinned != results[j].Pinned {
			return results[i].Pinned
		}
		return results[i].Score > results[j].Score
	})

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	unresponsive := make([]string, 0, len(searxngResp.UnresponsiveEngines))
	for _, e := range searxngResp.UnresponsiveEngines {
		if len(e) > 0 {
			unresponsive = append(unresponsive, e[0])
		}
	}

	// Only the "general" category (category == "") has a known engine
	// count to compare against — see generalCategoryEngineCount. Other
	// categories (e.g. "news") have their own, different engine sets that
	// haven't been measured, so there's no reliable "all of them" to
	// check against; Degraded just never fires for those rather than
	// guessing, consistent with under- rather than over-firing being the
	// safe direction here.
	degraded := false
	if category == "" && len(results) == 0 {
		degraded = len(unresponsive) >= generalCategoryEngineCount
	}
	if degraded {
		c.startCooldown()
		blocklistLog.Warn("searxng: full outage detected, entering cooldown", "query", query, "unresponsive_engines", unresponsive, "cooldown", degradedCooldown)
	}

	return &SearchResponse{
		Query:               query,
		Results:             results,
		Degraded:            degraded,
		UnresponsiveEngines: unresponsive,
	}, nil
}
