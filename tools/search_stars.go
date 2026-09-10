// search_stars is Weaver's dedup/link-discovery lookup over Constellation's
// existing stars — see docs/plans/constellation.md's "Weaver's tools".
// Only ever offered inside a Weaver shooting-star run (requires:
// weaver_run in tools/descriptions/search_stars.yaml).
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
)

var searchStarsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "search_stars",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/search_stars.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Keywords to match against existing stars' titles/summaries.",
				},
			},
			"required": []string{"query"},
		},
	},
}

func init() { Register("search_stars", handleSearchStars) }

func handleSearchStars(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "search_stars", nil, "error: "+err.Error(), callID)
	}
	if strings.TrimSpace(args.Query) == "" {
		return emitToolError(ctx, "search_stars", map[string]interface{}{"query": args.Query}, "error: query is required", callID)
	}
	if ctx.WeaverSearchStars == nil {
		return emitToolError(ctx, "search_stars", map[string]interface{}{"query": args.Query}, "error: search_stars is not available in this context", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "search_stars", "args": map[string]interface{}{"query": args.Query}, "call_id": callID})

	results, err := ctx.WeaverSearchStars(args.Query)
	if err != nil {
		return emitToolError(ctx, "search_stars", map[string]interface{}{"query": args.Query}, "error: "+err.Error(), callID)
	}

	var result string
	if len(results) == 0 {
		result = "no matching stars found"
	} else {
		var b strings.Builder
		for _, r := range results {
			fmt.Fprintf(&b, "star_id=%d [%s] %s — %s\n", r.ID, r.Status, r.Title, r.Summary)
		}
		result = strings.TrimRight(b.String(), "\n")
	}

	ctx.Emit("tool_result", map[string]interface{}{"tool": "search_stars", "result": result, "call_id": callID})
	return result
}
