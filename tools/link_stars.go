// link_stars records a reflection-layer connection between two distinct
// stars — see docs/plans/constellation.md's "Weaver's tools". Only ever
// offered inside a Weaver shooting-star run.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

var linkStarsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "link_stars",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/link_stars.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"star_id_a": map[string]interface{}{"type": "integer", "description": "The first star_id."},
				"star_id_b": map[string]interface{}{"type": "integer", "description": "The second star_id."},
				"reasoning": map[string]interface{}{"type": "string", "description": "Why these two distinct stars relate — a specific reason, not a vague one."},
			},
			"required": []string{"star_id_a", "star_id_b", "reasoning"},
		},
	},
}

func init() { Register("link_stars", handleLinkStars) }

func handleLinkStars(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		StarIDA   int64  `json:"star_id_a"`
		StarIDB   int64  `json:"star_id_b"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "link_stars", nil, "error: "+err.Error(), callID)
	}
	args.Reasoning = strings.TrimSpace(args.Reasoning)
	if args.StarIDA == 0 || args.StarIDB == 0 || args.Reasoning == "" {
		return emitToolError(ctx, "link_stars", map[string]interface{}{"star_id_a": args.StarIDA, "star_id_b": args.StarIDB}, "error: star_id_a, star_id_b, and reasoning are required", callID)
	}
	if args.StarIDA == args.StarIDB {
		return emitToolError(ctx, "link_stars", map[string]interface{}{"star_id_a": args.StarIDA, "star_id_b": args.StarIDB}, "error: star_id_a and star_id_b must be different stars", callID)
	}
	if ctx.WeaverLinkStars == nil {
		return emitToolError(ctx, "link_stars", map[string]interface{}{"star_id_a": args.StarIDA, "star_id_b": args.StarIDB}, "error: link_stars is not available in this context", callID)
	}

	callArgs := map[string]interface{}{"star_id_a": args.StarIDA, "star_id_b": args.StarIDB, "reasoning": args.Reasoning}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "link_stars", "args": callArgs, "call_id": callID})

	if err := ctx.WeaverLinkStars(args.StarIDA, args.StarIDB, args.Reasoning); err != nil {
		return emitToolError(ctx, "link_stars", callArgs, "error: "+err.Error(), callID)
	}

	result := "linked"

	ctx.Emit("tool_result", map[string]interface{}{"tool": "link_stars", "result": result, "call_id": callID})
	return result
}
