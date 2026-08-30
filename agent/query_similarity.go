package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"polaris/embed"
	"polaris/prompts"
)

// extractSearchQuery pulls the "query" argument out of a web_search
// call's raw JSON arguments (real tool call or pseudo-tool-call — both
// end up as a JSON args string by the time this is called). Returns ""
// on any parse failure or a missing/empty query, which callers treat as
// "nothing to track" rather than an error — malformed tool args are
// handleWebSearch's problem to report, not this signal's.
func extractSearchQuery(argsJSON string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return args.Query
}

// querySimilarityThreshold/querySimilarityStreakThreshold tune the third
// stale-search signal — cosine similarity between consecutive web_search
// queries, independent of researchCheckInInterval/staleStreakThreshold's
// citation-count-based signals (see trackResearchCall). Needed because a
// rephrased query that pulls in different (but equally useless) junk
// URLs each time still grows the citation count, which defeats
// staleStreakThreshold entirely — confirmed live via the BrowseComp
// benchmark: one riddle-style question burned ~90 Brave calls rephrasing
// the same dead-end query, each rephrase "finding" nominally new spam
// URLs, with zero staleStreakWarning firings the whole time.
//
// 0.90 similarity and a streak of 2 are conservative starting points, not
// yet measured against real usage the way staleStreakThreshold's 2 was —
// this needs the same live-usage tuning pass (via the benchmark harness
// or real traffic) before being trusted as tightly as the citation-based
// signals. Requiring TWO consecutive high-similarity queries (not one)
// before firing means a single legitimate near-duplicate rephrase — a
// normal, fine thing to do once — doesn't trip it.
const querySimilarityThreshold = 0.90
const querySimilarityStreakThreshold = 2

// querySimilarityMessage is deliberately as blunt as staleStreakMessage —
// evidence of repeating the same search, not a time-based nudge. Wording
// lives in prompts.yaml (agent.query_similarity_warning).
func querySimilarityMessage(streak int) string {
	return fmt.Sprintf(prompts.Get().Agent.QuerySimilarityWarning, streak)
}

// searchQueryTracker holds trackSearchQuery's running state across a
// turn's web_search calls — just the previous query's embedding and how
// many consecutive queries have been near-duplicates of it. Lives on the
// stack in Run, one per turn-loop invocation, same lifetime as
// researchCalls/lastCitationCount/staleStreak.
type searchQueryTracker struct {
	lastEmbedding []float32
	streak        int
}

// trackSearchQuery embeds query and compares it against the immediately
// preceding web_search query's embedding — not a wider window, matching
// staleStreakThreshold's own "consecutive" framing. That keeps this cheap
// (one comparison per call) and matches the failure mode actually
// observed live: query N resembling N-1 resembling N-2, not query 7
// suddenly resembling query 2.
//
// Silently does nothing (nil embed client, embedding failure, or the
// first call with no prior embedding to compare against) rather than
// erroring or blocking — this signal degrading gracefully is never worth
// slowing down, let alone failing, the actual research loop over.
func trackSearchQuery(reqCtx context.Context, embedClient *embed.Client, tracker *searchQueryTracker, query string) (nudge string, fired bool) {
	if embedClient == nil {
		return "", false
	}
	vec, err := embedClient.Embed(reqCtx, query)
	if err != nil {
		log.Warn("query-similarity: embedding failed, skipping this signal for this call", "query", query, "err", err)
		return "", false
	}
	prev := tracker.lastEmbedding
	tracker.lastEmbedding = vec
	if prev == nil {
		return "", false
	}

	if embed.CosineSimilarity(prev, vec) < querySimilarityThreshold {
		tracker.streak = 0
		return "", false
	}
	tracker.streak++
	if tracker.streak >= querySimilarityStreakThreshold {
		msg := querySimilarityMessage(tracker.streak)
		tracker.streak = 0 // fire once per streak, matching trackResearchCall's staleStreak reset
		return msg, true
	}
	return "", false
}
