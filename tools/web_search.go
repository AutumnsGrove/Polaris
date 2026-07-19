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
		Name:        "web_search",
		Description: "Search the web via SearXNG for current information, facts, or sources. Returns titles, URLs, and snippets.",
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
		return "error: " + err.Error()
	}
	if args.Query == "" {
		return "error: query is required"
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

	resp, err := ctx.SearXNG.Search(ctx.Ctx, args.Query, args.MaxResults, args.Category)
	if err != nil {
		log.Warn("web_search failed", "query", args.Query, "category", args.Category, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "error: " + err.Error()})
		return "error: " + err.Error()
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
		"citations": ctx.Citations,
	})

	return formatted
}
