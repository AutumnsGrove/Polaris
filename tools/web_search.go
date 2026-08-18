package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
)

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
		// Tavily is a completely different service (its own index, not a
		// SearXNG engine) with a scarce shared monthly credit budget (see
		// tavily package doc comment) — worth spending only on a confirmed
		// SearXNG failure, never as a blanket fallback for an ordinary
		// empty result.
		if ctx.Tavily != nil {
			if formatted, ok := tavilyFallback(ctx, args.Query); ok {
				return formatted
			}
		}
		log.Warn("web_search: searxng degraded", "query", args.Query, "category", args.Category, "unresponsive_engines", resp.UnresponsiveEngines)
		msg := fmt.Sprintf(
			"web search is temporarily degraded (SearXNG engines unresponsive: %s) — this is not a confirmed absence of results. Say so plainly if you rely on this, and prefer waiting or rephrasing over treating it as a real answer.",
			strings.Join(resp.UnresponsiveEngines, ", "),
		)
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
