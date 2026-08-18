package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"polaris/llm"
)

// parallelMonthlyCap is the hard ceiling on Parallel Search API calls per
// calendar month — its free tier is 5,000, but the account has a card on
// file, so going over bills real money automatically. Deliberately a
// little under 5,000 rather than exactly at it: store.Store.
// IncrementAPIUsage/GetAPIUsage round-trip through the DB per call, so a
// burst of concurrent turns could each pass the check before any of
// their increments land — a small buffer costs nothing (the free tier is
// still free at 4,900) and makes that race harmless instead of a
// principle to get exactly right.
const parallelMonthlyCap = 4900

var webSearchDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "web_search",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/web_search.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum results to return (default 5, max 10).",
				},
				"category": map[string]interface{}{
					"type": "string",
					"description": "Optional: \"news\" routes to dedicated news-search engines instead of " +
						"general web search. Use this for current-events/news queries — general search often " +
						"surfaces an outlet's homepage instead of a specific story for broad queries like " +
						"\"<city> news\".",
					"enum": []string{"general", "news"},
				},
			},
			"required": []string{"query"},
		},
	},
}

func init() { Register("web_search", handleWebSearch) }

func handleWebSearch(argsJSON string, ctx *Context) string {
	var args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
		Category   string `json:"category"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "web_search", nil, "error: "+err.Error())
	}
	if args.Query == "" {
		return emitToolError(ctx, "web_search", map[string]interface{}{"query": args.Query}, "error: query is required")
	}
	if args.MaxResults <= 0 || args.MaxResults > 10 {
		args.MaxResults = 5
	}
	if args.Category == "general" {
		args.Category = "" // SearXNG's default category — no filter needed
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "web_search",
		"args": map[string]interface{}{"query": args.Query},
	})

	if ctx.SearXNG == nil {
		result := "error: web search is not configured"
		log.Warn("web_search called with no SearXNG client configured", "query", args.Query)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": result})
		return result
	}

	resp, err := ctx.SearXNG.Search(ctx.Ctx, args.Query, args.MaxResults, args.Category)
	if err != nil {
		log.Warn("web_search failed", "query", args.Query, "category", args.Category, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "error: " + err.Error()})
		return "error: " + err.Error()
	}

	if len(resp.Results) == 0 && resp.Degraded {
		// SearXNG itself is failing (its own engines are rate-limited or
		// CAPTCHA'd — see search.SearchResponse.Degraded), not "this query
		// has no results". Those look identical otherwise (both are just
		// an empty slice with no error), and reporting the wrong one as
		// the other is a real trust problem: it either says "no results"
		// for a question that plainly has an answer, or (worse) lets the
		// model quietly answer from ungrounded memory instead.
		//
		// Parallel first, then Tavily — both completely different
		// services (their own indexes, not SearXNG engines) with their
		// own scarce budgets, worth spending only on a confirmed SearXNG
		// failure, never as a blanket fallback for an ordinary empty
		// result. Parallel goes first because its free tier (5,000/mo) is
		// 5x Tavily's (1,000/mo) — see parallelMonthlyCap — so exhausting
		// the cheaper one first stretches the combined budget further.
		if ctx.Parallel != nil && ctx.ParallelUsageThisMonth != nil {
			if used, uErr := ctx.ParallelUsageThisMonth(); uErr != nil {
				log.Warn("web_search: checking parallel usage failed, skipping to next fallback", "query", args.Query, "err", uErr)
			} else if used >= parallelMonthlyCap {
				log.Warn("web_search: parallel monthly cap reached, skipping to next fallback", "query", args.Query, "used", used, "cap", parallelMonthlyCap)
			} else if formatted, ok := parallelFallback(ctx, args.Query); ok {
				return formatted
			}
		}
		if ctx.Tavily != nil {
			if formatted, ok := tavilyFallback(ctx, args.Query); ok {
				return formatted
			}
		}

		log.Warn("web_search: searxng degraded, no fallback available or all fallbacks failed", "query", args.Query, "category", args.Category, "unresponsive_engines", resp.UnresponsiveEngines)
		msg := fmt.Sprintf(
			"web search is degraded and completely unavailable right now — every SearXNG engine (%s) is being rate-limited or blocked, and no fallback (Parallel/Tavily) is configured or able to help. "+
				"This is not a confirmed absence of results, so don't report it as one; say plainly that search is down right now rather than answering from memory.",
			strings.Join(resp.UnresponsiveEngines, ", "),
		)
		if !resp.RetryAfter.IsZero() {
			wait := time.Until(resp.RetryAfter).Round(time.Minute)
			if wait > 0 {
				msg += fmt.Sprintf(" SearXNG itself won't be retried for about %s.", wait)
			}
		}
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": msg})
		return msg
	}

	if len(resp.Results) == 0 {
		log.Info("web_search: no results", "query", args.Query, "category", args.Category)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "no results"})
		return "no results found"
	}

	urls := make([]string, 0, len(resp.Results))
	var sb strings.Builder
	for i, r := range resp.Results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
		ctx.AddCitation(Citation{Title: r.Title, URL: r.URL})
		urls = append(urls, r.URL)
	}
	formatted := sb.String()

	log.Info("web_search", "query", args.Query, "category", args.Category, "results", len(resp.Results), "urls", urls)

	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_search",
		"result":    formatted,
		"citations": ctx.CitationsSnapshot(),
	})

	return formatted
}

// parallelFallback tries Parallel's Search API once SearXNG has confirmed
// itself degraded and the caller has already checked the monthly usage
// cap (see handleWebSearch). Only increments the persisted usage counter
// once the request actually completed (err == nil) — a network failure
// or non-200 response never reached Parallel's own billing, so it
// shouldn't count against the budget either. A completed request with
// zero results still increments: Parallel did the work and (per its
// per-request pricing) almost certainly still billed for it, whether or
// not anything useful came back. Returns ok=false on any failure so the
// caller falls through to Tavily instead.
func parallelFallback(ctx *Context, query string) (formatted string, ok bool) {
	resp, err := ctx.Parallel.Search(ctx.Ctx, query, 5)
	if err != nil {
		log.Warn("web_search: parallel fallback failed", "query", query, "err", err)
		return "", false
	}
	if ctx.IncrementParallelUsage != nil {
		if incErr := ctx.IncrementParallelUsage(); incErr != nil {
			log.Warn("web_search: recording parallel usage failed", "query", query, "err", incErr)
		}
	}
	if len(resp.Results) == 0 {
		log.Warn("web_search: parallel fallback returned no results", "query", query)
		return "", false
	}

	urls := make([]string, 0, len(resp.Results))
	var sb strings.Builder
	for i, r := range resp.Results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
		ctx.AddCitation(Citation{Title: r.Title, URL: r.URL})
		urls = append(urls, r.URL)
	}
	formatted = sb.String()

	log.Info("web_search: served from parallel fallback (searxng degraded)", "query", query, "results", len(resp.Results), "urls", urls)

	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_search",
		"result":    formatted,
		"citations": ctx.CitationsSnapshot(),
	})
	return formatted, true
}

// tavilyFallback tries Tavily's Search API once SearXNG has confirmed
// itself degraded (see handleWebSearch). Returns ok=false on any failure
// or an empty result so the caller falls through to the plain "degraded"
// message instead — this is a best-effort rescue, not something worth its
// own error path back to the model.
func tavilyFallback(ctx *Context, query string) (formatted string, ok bool) {
	resp, err := ctx.Tavily.Search(ctx.Ctx, query, 5)
	if err != nil || len(resp.Results) == 0 {
		log.Warn("web_search: tavily fallback failed", "query", query, "err", err)
		return "", false
	}

	urls := make([]string, 0, len(resp.Results))
	var sb strings.Builder
	for i, r := range resp.Results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
		ctx.AddCitation(Citation{Title: r.Title, URL: r.URL})
		urls = append(urls, r.URL)
	}
	formatted = sb.String()

	log.Info("web_search: served from tavily fallback (searxng degraded)", "query", query, "results", len(resp.Results), "urls", urls)

	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_search",
		"result":    formatted,
		"citations": ctx.CitationsSnapshot(),
	})
	return formatted, true
}
