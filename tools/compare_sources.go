// compare_sources checks whether two or more of a turn's own cited sources
// actually agree on a specific fact, using a decision model (see the decisions
// package) rather than the model's own read of them.
// Backend-only — no UI, no WS event, no store column: the model decides
// whether and how to mention the result in its own prose, the same way it
// already decides how to describe anything else it found. See
// docs/plans/source-verification-compare-tool.md for the live-spiked design
// this implements.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/decisions"
	"polaris/llm"
)

var compareSourcesDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "compare_sources",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/compare_sources.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"urls": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Two or more URLs you've already read this turn (via web_read) that might disagree on a specific point.",
				},
				"question": map[string]interface{}{
					"type":        "string",
					"description": "The one specific fact to check — e.g. \"What is the launch date?\" not a broad topic.",
				},
			},
			"required": []string{"urls", "question"},
		},
	},
}

func init() { Register("compare_sources", handleCompareSources) }

// decisionsPerTurnCapUSD bounds one turn's total decisions spend across every
// compare_sources call it makes — a backstop against a pathological turn
// (many clusters, each re-comparing large sources) spending disproportionately
// before the monthly cap below ever notices. Roughly 100-400x the
// ~$0.000025-0.0001/call observed live for a typical cluster comparison —
// see docs/plans/source-verification-compare-tool.md's cost-tracking
// section. Shared in spirit with the future verification-badge feature's own
// per-turn cap, though each tracks its own running total independently
// (DecisionsSpentThisTurn is per-Context, not global).
const decisionsPerTurnCapUSD = 0.01

// decisionsMonthlyCapUSD is a real ceiling on total monthly decisions spend across every
// compare_sources call, checked via store.Store's decisions_usage table — same
// "fail closed like Brave/Parallel being nil" shape as every other capped
// provider. A generous multiple of the per-turn cap, not a tightly-reasoned
// figure — deliberately adjustable without needing to touch call sites.
const decisionsMonthlyCapUSD = 5.00

func handleCompareSources(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		URLs     []string `json:"urls"`
		Question string   `json:"question"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "compare_sources", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "compare_sources",
		"args":    map[string]interface{}{"urls": args.URLs, "question": args.Question},
		"call_id": callID,
	})

	respond := func(result string) string {
		ctx.Emit("tool_result", map[string]interface{}{"tool": "compare_sources", "result": result, "call_id": callID})
		return result
	}

	if len(args.URLs) < 2 {
		return respond("error: need at least two URLs to compare")
	}
	if strings.TrimSpace(args.Question) == "" {
		return respond("error: question is required")
	}
	if ctx.Decisions == nil {
		return respond("comparison unavailable — verification isn't configured for this deployment")
	}
	if ctx.DecisionsSpentThisTurn() >= decisionsPerTurnCapUSD {
		return respond("comparison unavailable — this turn's verification budget has already been used")
	}
	if ctx.DecisionsCostThisMonth != nil {
		if used, err := ctx.DecisionsCostThisMonth(); err != nil {
			log.Warn("compare_sources: checking decisions monthly cost failed, proceeding anyway", "err", err)
		} else if used >= decisionsMonthlyCapUSD {
			return respond("comparison unavailable — monthly verification budget has been reached")
		}
	}

	// Assign short, stable labels (A, B, C, ...) so the summary reads
	// naturally and the pairwise question keys stay short — dedupe by URL
	// along the way, since a model could plausibly repeat one.
	labelForURL := map[string]string{}
	var labels []string
	var states []decisions.SourceState
	var missing []string
	for _, url := range args.URLs {
		if _, seen := labelForURL[url]; seen {
			continue
		}
		text, ok := ctx.EvidenceForURL(url)
		if !ok || strings.TrimSpace(text) == "" {
			missing = append(missing, url)
			continue
		}
		label := sourceLabel(len(labels))
		labelForURL[url] = label
		labels = append(labels, label)
		states = append(states, decisions.SourceState{Source: label, Text: text})
	}
	if len(missing) > 0 {
		return respond(fmt.Sprintf("error: no stored text for %s — call web_read on it first, then compare_sources again", strings.Join(missing, ", ")))
	}
	if len(states) < 2 {
		return respond("error: need at least two distinct sources with stored text to compare")
	}

	type pair struct{ a, b string }
	var pairs []pair
	questions := map[string]decisions.ChoiceQuestion{}
	for i := 0; i < len(labels); i++ {
		for j := i + 1; j < len(labels); j++ {
			a, b := labels[i], labels[j]
			pairs = append(pairs, pair{a, b})
			questions[a+"_vs_"+b] = decisions.ChoiceQuestion{
				Instructions: fmt.Sprintf("Do sources %s and %s agree on: %s?", a, b, args.Question),
				Criteria: map[string]string{
					"agree":                "The two sources say the same thing, even if worded differently or with different precision.",
					"disagree":             "The two sources genuinely conflict on this specific point.",
					"insufficient_overlap": "At least one of the two sources doesn't address this point at all, so there's nothing to compare.",
				},
			}
		}
	}

	resp, err := ctx.Decisions.AskChoice(ctx.Ctx, states, questions)
	if err != nil {
		log.Warn("compare_sources: decisions call failed", "question", args.Question, "err", err)
		return respond("error: comparison failed — " + err.Error())
	}

	cost := resp.Usage.CostUSD
	ctx.AddCost(cost)
	ctx.AddDecisionsCost(cost)
	logCost := ctx.LogCompareDecisionsCost
	if logCost == nil {
		logCost = ctx.LogDecisionsCost
	}
	if logCost != nil {
		if err := logCost(cost); err != nil {
			log.Warn("compare_sources: logging decisions cost failed", "err", err)
		}
	}

	urlForLabel := make(map[string]string, len(labelForURL))
	for url, label := range labelForURL {
		urlForLabel[label] = url
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Comparing %d sources on %q:", len(states), args.Question))
	for _, p := range pairs {
		key := p.a + "_vs_" + p.b
		ans, ok := resp.Answers[key]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (%s) vs %s (%s): %s (confidence %.2f)",
			p.a, urlForLabel[p.a], p.b, urlForLabel[p.b], ans.Choice, ans.Confidence))
	}

	log.Info("compare_sources", "question", args.Question, "sources", len(states), "cost_usd", cost)
	return respond(strings.Join(lines, "\n"))
}

// sourceLabel returns A, B, C, ... Z, AA, AB, ... for index i — plenty of
// headroom for any realistic number of URLs in one call.
func sourceLabel(i int) string {
	var letters []byte
	for {
		letters = append([]byte{byte('A' + i%26)}, letters...)
		i = i/26 - 1
		if i < 0 {
			break
		}
	}
	return string(letters)
}
