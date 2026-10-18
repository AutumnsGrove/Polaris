// comet_generate.go assembles one period's Comet edition — the stat row,
// category breakdown, rhythm, tools, new stars and Constellation growth
// blocks, plus the masthead hook and highlight picks. Everything except
// the spike/fade clustering and those two picks is straight aggregation
// over data Polaris already timestamped; the one LLM call is a bounded,
// tool-forced synthesis pass (see docs/plans/comet.md's "Spiked/faded"
// section).
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
)

// cometBaselineDays is the trailing window a period's spiked/faded pass
// compares against — see cometBaselineStart.
const cometBaselineDays = 90

// cometStarShowcaseLimit is how many new stars the edition carries. The
// mockup shows 4 of 8 with "+N more", so this is the fetch cap (the
// frontend decides how many to actually render); the total count travels
// alongside it in StarTotal.
const cometStarShowcaseLimit = 8

// cometCategoryLimit is the plan doc's hard cap on category-breakdown rows.
const cometCategoryLimit = 5

// cometTrendQueryCap bounds how many distinct query lines the synthesis call
// is given, per window. A busy month can hold hundreds of distinct queries;
// the counts are sorted most-used-first, so the tail is one-off phrasing the
// clustering can't do much with anyway. Kept deliberately modest: a
// September-sized month (876 searches, 500+ distinct queries) reproduced a
// provider hang/empty-completion at 120 per window, while a 80-cap plus one
// retry went through cleanly. Regard this as a load-bearing ceiling, not a
// formatting nicety.
const cometTrendQueryCap = 80

// cometTrendCandidateCap is how many conversations the synthesis pass may
// pick highlights from — a smaller list keeps the prompt bounded too.
const cometTrendCandidateCap = 10

// comet trends tool — forced structured output for the one synthesis call,
// same shape as dailyVerdictToolDef.
var cometTrendsToolDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "record_trends",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"spiked": map[string]interface{}{
					"type":        "array",
					"maxItems":    3,
					"description": "At most three topics that surged this period, most significant first. Empty if nothing genuinely spiked.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"topic": map[string]interface{}{"type": "string", "description": "Short human topic name, not a raw query."},
							"count": map[string]interface{}{"type": "integer", "description": "How many related searches this period."},
						},
						"required": []string{"topic", "count"},
					},
				},
				"faded": map[string]interface{}{
					"type":        "array",
					"maxItems":    3,
					"description": "At most three topics that went quiet this period, most significant first. Empty if nothing genuinely faded.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"topic": map[string]interface{}{"type": "string", "description": "Short human topic name, not a raw query."},
							"count": map[string]interface{}{"type": "integer", "description": "How many related searches in the baseline window."},
							"since": map[string]interface{}{"type": "string", "description": "Short qualifier like \"quiet since Aug 14\"."},
						},
						"required": []string{"topic", "count", "since"},
					},
				},
				"hook": map[string]interface{}{
					"type":        "string",
					"description": "Exactly one sentence of editorial framing for the page's masthead — what the month was mostly about, grounded in the whole month (topics, stars, tools, unresolved threads), not just the search log. One sentence, specific to this person's actual month.",
				},
				"highlights": map[string]interface{}{
					"type":        "array",
					"maxItems":    3,
					"description": "Up to three candidate conversations (by their number in the candidate list) that best defined the month, most significant first. Empty if there are no candidates or none genuinely stand out.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"index": map[string]interface{}{"type": "integer", "description": "1-based number of the conversation from the candidate list."},
							"why":   map[string]interface{}{"type": "string", "description": "One short clause on what made it defining."},
						},
						"required": []string{"index", "why"},
					},
				},
			},
			"required": []string{"spiked", "faded", "hook", "highlights"},
		},
	},
}

// runCometGeneration assembles (or re-assembles) one period's edition and
// stores it. Called by both the scheduler and the manual "Generate now"
// path — one code path, same as Pulsar Daily's runDailyPipeline.
func (s *Server) runCometGeneration(reqCtx context.Context, period string) error {
	cfg := s.liveConfig()
	cfgRow, err := s.db.GetCometConfig()
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	client := CometClient(cfg, cfgRow.Model)

	startStr, endStr, ok := cometPeriodDB(period)
	if !ok {
		return fmt.Errorf("comet generation: invalid period %q", period)
	}

	turns, searches, err := s.db.CometPeriodCounts(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	periodCost, err := s.db.CometPeriodCost(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	ghostCount, err := s.db.CometPeriodGhostCount(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	stars, starTotal, err := s.db.CometPeriodNewStars(startStr, endStr, cometStarShowcaseLimit)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	categories, err := s.db.CometPeriodCategoryBreakdown(period, cometCategoryLimit)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}

	activity, err := s.db.CometPeriodActivity(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	rhythm := computeCometRhythm(period, activity)

	tools, err := s.db.CometPeriodToolCounts(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}

	// Constellation growth is entirely optional: Comet can be on while
	// Constellation is off, in which case every number here is zero and the
	// frontend hides the block. Never treat that as an error.
	grownStars, newLinks, featuredStar, err := s.db.CometPeriodGrowth(startStr, endStr)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}

	unresolvedTopics, unresolvedCount, err := s.db.CometPeriodUnresolvedTopics(period, 8)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	firsts, err := s.db.CometPeriodFirstTopics(period, 6)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	candidates, err := s.db.CometPeriodCandidateThreads(startStr, endStr, period, cometTrendCandidateCap)
	if err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}

	// The one LLM call: cluster this period's searches, pick the defining
	// threads, and write a masthead hook grounded in the whole month. A
	// failure here degrades rather than failing the edition — everything
	// else on the page is real aggregation regardless.
	spiked, faded, hook, highlights, synthCost, terr := s.synthesizeCometTrends(
		reqCtx, client, period, categories, stars, tools, unresolvedTopics, firsts, candidates,
	)
	if terr != nil {
		log.Warn("comet: trend synthesis failed, generating without hook/spikes/highlights", "period", period, "err", terr)
	}
	if synthCost > 0 {
		// Recorded in the aux ledger so GetStats counts this real spend —
		// no thread or turn owns this call.
		if aerr := s.db.RecordAuxCost("comet_synthesis", synthCost); aerr != nil {
			log.Warn("comet: recording synthesis cost failed", "err", aerr)
		}
	}

	// Normalize nil slices to empty ones in the struct below — Go's
	// json.Marshal writes a nil slice as `null`, and a JSON `null` reaching
	// the frontend's `.length`/`.slice` calls throws and freezes the page
	// (a real bug, caught live navigating to a month with no faded topics
	// or no new stars). An empty array is the honest wire shape here.
	data := store.CometEditionData{
		PeriodLabel: cometPeriodLabel(period),
		Stats: store.CometStats{
			Turns:    turns,
			NewStars: starTotal,
			Searches: searches,
			CostUSD:  periodCost,
		},
		Categories: orEmpty(categories),
		Spiked:     orEmpty(spiked),
		Faded:      orEmpty(faded),
		Stars:      orEmpty(stars),
		StarTotal:  starTotal,
		GhostCount: ghostCount,
		Highlights: orEmpty(highlights),
		Rhythm:     rhythm,
		Tools:      orEmpty(tools),
		Growth: store.CometGrowth{
			NewStars:   starTotal,
			GrownStars: grownStars,
			NewLinks:   newLinks,
			Featured:   featuredStar,
		},
		UnresolvedCount: unresolvedCount,
		Firsts:          orEmpty(firsts),
	}
	if err := s.db.UpsertCometEdition(period, "monthly", hook, data, synthCost); err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	if err := s.db.SetCometLastGenerated(time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		return fmt.Errorf("comet generation: %w", err)
	}
	log.Info("comet: edition generated", "period", period, "turns", turns, "searches", searches, "stars", starTotal, "cost_usd", periodCost)
	return nil
}

// synthesizeCometTrends runs the one bounded synthesis call. It sees the
// whole month, not just the search log: the topic breakdown, the new-star
// titles, tool usage, unresolved topics, and a numbered candidate-thread
// list, so the hook can describe the month rather than fixating on
// whichever search cluster happened to dominate. Returns empty results on
// any failure so the caller can still generate the page.
func (s *Server) synthesizeCometTrends(
	reqCtx context.Context,
	client llm.ChatClient,
	period string,
	categories []store.CometCategory,
	stars []store.CometStar,
	tools []store.CometToolCount,
	unresolvedTopics []string,
	firsts []store.CometFirst,
	candidates []store.CometThreadCandidate,
) (spiked, faded []store.CometTrend, hook string, highlights []store.CometHighlight, cost float64, err error) {
	startStr, endStr, ok := cometPeriodDB(period)
	if !ok {
		return nil, nil, "", nil, 0, fmt.Errorf("invalid period %q", period)
	}
	periodStats, err := s.db.CometSearchQueryStats(startStr, endStr)
	if err != nil {
		return nil, nil, "", nil, 0, fmt.Errorf("period search stats: %w", err)
	}
	baselineStart, ok := cometBaselineStart(period, cometBaselineDays)
	if !ok {
		return nil, nil, "", nil, 0, fmt.Errorf("invalid period %q", period)
	}
	baselineStats, err := s.db.CometSearchQueryStats(baselineStart, startStr)
	if err != nil {
		return nil, nil, "", nil, 0, fmt.Errorf("baseline search stats: %w", err)
	}
	if len(periodStats) == 0 && len(baselineStats) == 0 && len(candidates) == 0 && len(categories) == 0 {
		// Nothing at all to reflect on — skip the call entirely.
		return nil, nil, "", nil, 0, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Month being reviewed: %s.\n", cometPeriodLabel(period))

	if len(categories) > 0 {
		b.WriteString("\nWhere attention went (conversations by topic):\n")
		for _, c := range categories {
			fmt.Fprintf(&b, "- %s: %d\n", c.Name, c.Count)
		}
	}
	if len(stars) > 0 {
		b.WriteString("\nNew stars formed this month (titles):\n")
		for _, st := range stars {
			fmt.Fprintf(&b, "- %s [%s]\n", st.Title, st.Category)
		}
	}
	if len(tools) > 0 {
		b.WriteString("\nTools used:\n")
		for i, t := range tools {
			if i >= 12 {
				break
			}
			fmt.Fprintf(&b, "- %s: %d\n", t.Name, t.Count)
		}
	}
	if len(unresolvedTopics) > 0 {
		b.WriteString("\nTopics that ended unresolved: " + strings.Join(unresolvedTopics, ", ") + "\n")
	}
	if len(firsts) > 0 {
		names := make([]string, 0, len(firsts))
		for _, f := range firsts {
			names = append(names, f.Name)
		}
		b.WriteString("\nTopics explored for the first time this month: " + strings.Join(names, ", ") + "\n")
	}

	fmt.Fprintf(&b, "\nSearches during the month:\n")
	if len(periodStats) == 0 {
		b.WriteString("(none)\n")
	}
	for i, q := range periodStats {
		if i >= cometTrendQueryCap {
			fmt.Fprintf(&b, "(+%d more lower-volume queries omitted)\n", len(periodStats)-cometTrendQueryCap)
			break
		}
		fmt.Fprintf(&b, "- %q (%dx)\n", q.Query, q.Count)
	}
	fmt.Fprintf(&b, "\nSearches in the %d-day baseline before it:\n", cometBaselineDays)
	if len(baselineStats) == 0 {
		b.WriteString("(none)\n")
	}
	for i, q := range baselineStats {
		if i >= cometTrendQueryCap {
			fmt.Fprintf(&b, "(+%d more lower-volume queries omitted)\n", len(baselineStats)-cometTrendQueryCap)
			break
		}
		fmt.Fprintf(&b, "- %q (%dx, last %s)\n", q.Query, q.Count, q.LastSeen)
	}

	if len(candidates) > 0 {
		b.WriteString("\nCandidate conversations (refer to one by its number):\n")
		for i, c := range candidates {
			title := c.Title
			if strings.TrimSpace(title) == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "%d. %s", i+1, title)
			var parts []string
			if c.Topic != "" {
				parts = append(parts, "topic: "+c.Topic)
			}
			if c.Mood != "" {
				parts = append(parts, "mood: "+c.Mood)
			}
			if c.Resolved != "" {
				parts = append(parts, "resolved: "+c.Resolved)
			}
			if c.Turns > 0 {
				parts = append(parts, fmt.Sprintf("%d turns", c.Turns))
			}
			if len(parts) > 0 {
				b.WriteString(" — " + strings.Join(parts, ", "))
			}
			b.WriteString("\n")
		}
	}

	messages := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Comet.TrendsSystem},
		{Role: "user", Content: b.String()},
	}
	// Two attempts, not one: a busy month's prompt occasionally comes back as
	// an empty completion (observed live on a September-sized input — no tool
	// call, no content), and this is the one call that can't be re-derived
	// cheaply later. The retry is bounded and its cost is summed, so a real
	// outage still fails fast rather than looping.
	var resp *llm.ChatResponse
	var callErr error
	for attempt := 1; attempt <= 2; attempt++ {
		// Prefer the forced-tool call, which structurally can't come back as
		// prose. Fall back to the auto-tool path only for callers whose client
		// isn't the concrete *llm.Client (tests' mocks) — the real one always is.
		if forced, ok := client.(*llm.Client); ok {
			resp, callErr = forced.ChatCompletionForcedTool(reqCtx, messages, cometTrendsToolDef, func(string) {}, nil)
		} else {
			resp, callErr = client.ChatCompletionWithTools(reqCtx, messages, []llm.ToolDef{cometTrendsToolDef}, func(string) {}, nil)
		}
		if callErr != nil {
			return nil, nil, "", nil, cost, fmt.Errorf("record_trends call: %w", callErr)
		}
		cost += resp.CostUSD
		if len(resp.ToolCalls) > 0 {
			break
		}
		log.Warn("comet: trend synthesis returned no tool call", "period", period, "attempt", attempt, "content", truncateForLog(resp.Content, 200))
		if attempt == 2 {
			return nil, nil, "", nil, cost, fmt.Errorf("model never called record_trends")
		}
	}
	var args struct {
		Spiked []struct {
			Topic string `json:"topic"`
			Count int    `json:"count"`
		} `json:"spiked"`
		Faded []struct {
			Topic string `json:"topic"`
			Count int    `json:"count"`
			Since string `json:"since"`
		} `json:"faded"`
		Hook       string `json:"hook"`
		Highlights []struct {
			Index int    `json:"index"`
			Why   string `json:"why"`
		} `json:"highlights"`
	}
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil {
		return nil, nil, "", nil, cost, fmt.Errorf("decode record_trends arguments: %w", err)
	}
	for _, sp := range args.Spiked {
		if strings.TrimSpace(sp.Topic) == "" {
			continue
		}
		spiked = append(spiked, store.CometTrend{Topic: strings.TrimSpace(sp.Topic), Count: sp.Count, Delta: fmt.Sprintf("↑ %d searches", sp.Count)})
	}
	for _, fd := range args.Faded {
		if strings.TrimSpace(fd.Topic) == "" {
			continue
		}
		faded = append(faded, store.CometTrend{Topic: strings.TrimSpace(fd.Topic), Count: fd.Count, Since: strings.TrimSpace(fd.Since)})
	}
	for _, h := range args.Highlights {
		// 1-based index into the candidate list. Out-of-range or blank
		// entries are dropped rather than trusted — a model can't invent a
		// thread id this way.
		if h.Index < 1 || h.Index > len(candidates) {
			continue
		}
		c := candidates[h.Index-1]
		highlights = append(highlights, store.CometHighlight{
			ThreadID: c.ThreadID,
			Title:    c.Title,
			Topic:    c.Topic,
			Why:      strings.TrimSpace(h.Why),
		})
		if len(highlights) >= 3 {
			break
		}
	}
	return spiked, faded, strings.TrimSpace(args.Hook), highlights, cost, nil
}

// orEmpty returns s, or a non-nil empty slice when s is nil, so it marshals
// as `[]` rather than `null` — see runCometGeneration's comment on why the
// frontend can't tolerate a null collection.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// truncateForLog caps a model response for a log line.
func truncateForLog(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
