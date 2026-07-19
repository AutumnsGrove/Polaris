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

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "web_search",
		"args": map[string]interface{}{"query": args.Query},
	})

	resp, err := ctx.SearXNG.Search(ctx.Ctx, args.Query, args.MaxResults)
	if err != nil {
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "error: " + err.Error()})
		return "error: " + err.Error()
	}

	if len(resp.Results) == 0 {
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "no results"})
		return "no results found"
	}

	var sb strings.Builder
	for i, r := range resp.Results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
		ctx.AddCitation(Citation{Title: r.Title, URL: r.URL})
	}
	formatted := sb.String()

	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_search",
		"result":    formatted,
		"citations": ctx.Citations,
	})

	return formatted
}
