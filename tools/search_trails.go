// search_trails is the Approach classifier's lookup over prior Comet
// trails — see docs/plans/comet.md's "Cross-month behavior". Two jobs:
// (1) surface a thread's own prior Trail(s) when it gets a second Trail in
// a later period, so the new classification can note "picked back up from
// last period's result" instead of re-deriving the whole thread cold; (2)
// keep topic/mood phrasing consistent run over run, the same reason
// search_stars exists for Weaver's internal use. Only ever offered inside
// an Approach run (requires: approach_run in
// tools/descriptions/search_trails.yaml).
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
)

var searchTrailsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "search_trails",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/search_trails.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Keywords to match against prior trails' topic/mood/intent and their thread titles.",
				},
			},
			"required": []string{"query"},
		},
	},
}

func init() { Register("search_trails", handleSearchTrails) }

func handleSearchTrails(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "search_trails", nil, "error: "+err.Error(), callID)
	}
	if strings.TrimSpace(args.Query) == "" {
		return emitToolError(ctx, "search_trails", map[string]interface{}{"query": args.Query}, "error: query is required", callID)
	}
	if ctx.ApproachSearchTrails == nil {
		return emitToolError(ctx, "search_trails", map[string]interface{}{"query": args.Query}, "error: search_trails is not available in this context", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "search_trails", "args": map[string]interface{}{"query": args.Query}, "call_id": callID})

	results, err := ctx.ApproachSearchTrails(args.Query)
	if err != nil {
		return emitToolError(ctx, "search_trails", map[string]interface{}{"query": args.Query}, "error: "+err.Error(), callID)
	}

	var result string
	if len(results) == 0 {
		result = "no matching trails found"
	} else {
		var b strings.Builder
		for _, tr := range results {
			fmt.Fprintf(&b, "%s [%s] topic=%q mood=%q intent=%q resolved=%s\n", tr.Period, tr.ThreadTitle, tr.Topic, tr.Mood, tr.Intent, tr.Resolved)
		}
		result = strings.TrimRight(b.String(), "\n")
	}

	ctx.Emit("tool_result", map[string]interface{}{"tool": "search_trails", "result": result, "call_id": callID})
	return result
}
