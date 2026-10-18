// comet_generate.go assembles one period's Comet edition — the four fixed
// blocks (stat row, category breakdown, spiked/faded, new stars) plus the
// one sentence of masthead framing the page is allowed. Everything except
// the spike/fade clustering is straight aggregation over data Polaris
// already timestamped; the one LLM call is a bounded, tool-forced synthesis
// pass (see docs/plans/comet.md's "Spiked/faded" section).
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
// clustering can't do much with anyway. Keeps the prompt size bounded
// regardless of how busy the month was.
const cometTrendQueryCap = 120

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
					"description": "Exactly one sentence of editorial framing for the page's masthead — what the month was mostly about. One sentence, specific to this person's actual month.",
				},
			},
			"required": []string{"spiked", "faded", "hook"},
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

	// The one LLM call: cluster this period's raw search queries against the
	// trailing baseline and write the masthead hook. A failure here degrades
	// to empty trends rather than failing the whole edition — the rest of
	// the page (stats, categories, stars) is real data regardless.
	spiked, faded, hook, synthCost, terr := s.synthesizeCometTrends(reqCtx, client, period)
	if terr != nil {
		log.Warn("comet: trend synthesis failed, generating without spikes/fades", "period", period, "err", terr)
	}
	if synthCost > 0 {
		// Recorded in the aux ledger so GetStats counts this real spend —
		// no thread or turn owns this call.
		if aerr := s.db.RecordAuxCost("comet_synthesis", synthCost); aerr != nil {
			log.Warn("comet: recording synthesis cost failed", "err", aerr)
		}
	}

	// Normalize nil slices to empty ones before they're marshalled — Go's
	// json.Marshal writes a nil slice as `null`, and a JSON `null` reaching
	// the frontend's `.length`/`.slice` calls throws and freezes the page
	// (a real bug, caught live navigating to a month with no faded topics
	// or no new stars). An empty array is the honest wire shape here.
	if categories == nil {
		categories = []store.CometCategory{}
	}
	if stars == nil {
		stars = []store.CometStar{}
	}
	if spiked == nil {
		spiked = []store.CometTrend{}
	}
	if faded == nil {
		faded = []store.CometTrend{}
	}

	data := store.CometEditionData{
		PeriodLabel: cometPeriodLabel(period),
		Stats: store.CometStats{
			Turns:    turns,
			NewStars: starTotal,
			Searches: searches,
			CostUSD:  periodCost,
		},
		Categories: categories,
		Spiked:     spiked,
		Faded:      faded,
		Stars:      stars,
		StarTotal:  starTotal,
		GhostCount: ghostCount,
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

// synthesizeCometTrends runs the one bounded synthesis call. Returns empty
// slices on any parse failure so the caller can still generate the page.
func (s *Server) synthesizeCometTrends(reqCtx context.Context, client llm.ChatClient, period string) (spiked, faded []store.CometTrend, hook string, cost float64, err error) {
	startStr, endStr, ok := cometPeriodDB(period)
	if !ok {
		return nil, nil, "", 0, fmt.Errorf("invalid period %q", period)
	}
	periodStats, err := s.db.CometSearchQueryStats(startStr, endStr)
	if err != nil {
		return nil, nil, "", 0, err
	}
	baselineStart, ok := cometBaselineStart(period, cometBaselineDays)
	if !ok {
		return nil, nil, "", 0, fmt.Errorf("invalid period %q", period)
	}
	baselineStats, err := s.db.CometSearchQueryStats(baselineStart, startStr)
	if err != nil {
		return nil, nil, "", 0, err
	}
	if len(periodStats) == 0 && len(baselineStats) == 0 {
		// Nothing searched at all — skip the call entirely.
		return nil, nil, "", 0, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Month being reviewed: %s.\n\nSearches during the month:\n", cometPeriodLabel(period))
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

	messages := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Comet.TrendsSystem},
		{Role: "user", Content: b.String()},
	}
	var resp *llm.ChatResponse
	// Prefer the forced-tool call, which structurally can't come back as
	// prose. Fall back to the auto-tool path only for callers whose client
	// isn't the concrete *llm.Client (tests' mocks) — the real one always is.
	if forced, ok := client.(*llm.Client); ok {
		resp, err = forced.ChatCompletionForcedTool(reqCtx, messages, cometTrendsToolDef, func(string) {}, nil)
	} else {
		resp, err = client.ChatCompletionWithTools(reqCtx, messages, []llm.ToolDef{cometTrendsToolDef}, func(string) {}, nil)
	}
	if err != nil {
		return nil, nil, "", 0, err
	}
	cost = resp.CostUSD
	if len(resp.ToolCalls) == 0 {
		log.Warn("comet: trend synthesis returned no tool call", "period", period, "content", truncateForLog(resp.Content, 300))
		return nil, nil, "", cost, fmt.Errorf("model never called record_trends")
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
		Hook string `json:"hook"`
	}
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil {
		return nil, nil, "", cost, err
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
	return spiked, faded, strings.TrimSpace(args.Hook), cost, nil
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
