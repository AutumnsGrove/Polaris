// update_star merges new content into an existing Constellation star — see
// docs/plans/constellation.md's "Weaver's tools" and "Personal stars".
// Only ever offered inside a Weaver shooting-star run.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
	"polaris/store"
)

var updateStarDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "update_star",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/update_star.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"star_id": map[string]interface{}{"type": "integer", "description": "The star_id to merge into, from search_stars/read_star."},
				"summary": map[string]interface{}{"type": "string", "description": "The rewritten one-line summary, reflecting the star as it now stands."},
				"body":    map[string]interface{}{"type": "string", "description": "The rewritten full Markdown body."},
				"tags": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "The star's tags after this update.",
				},
				"confidence_class": map[string]interface{}{
					"type": "string", "enum": []string{"obvious", "fuzzy"},
					"description": "obvious: a fact/topic actually, substantively discussed. fuzzy: real but genuinely uncertain.",
				},
				"is_personal": map[string]interface{}{
					"type":        "boolean",
					"description": "True only if this star is an inference about who the person IS. Should be true for essentially every star this agent updates.",
				},
				"reasoning": map[string]interface{}{
					"type":        "string",
					"description": "One sentence: why this update was made. Shown to the person reviewing this star as \"Why this needs a look\" — a vague reason is itself a signal this probably shouldn't be a star.",
				},
			},
			"required": []string{"star_id", "summary"},
		},
	},
}

func init() { Register("update_star", handleUpdateStar) }

func handleUpdateStar(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		StarID          int64    `json:"star_id"`
		Summary         string   `json:"summary"`
		Body            string   `json:"body"`
		Tags            []string `json:"tags"`
		ConfidenceClass string   `json:"confidence_class"`
		// IsPersonal is a *bool, not bool: omitted in the JSON call means
		// "leave the star's existing is_personal as-is" (store.UpdateStar's
		// contract), distinct from the model explicitly stating false.
		IsPersonal *bool  `json:"is_personal"`
		Reasoning  string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "update_star", nil, "error: "+err.Error(), callID)
	}
	args.Summary = strings.TrimSpace(args.Summary)
	if args.Summary == "" {
		return emitToolError(ctx, "update_star", map[string]interface{}{"star_id": args.StarID}, "error: summary is required", callID)
	}
	if ctx.WeaverUpdateStar == nil {
		return emitToolError(ctx, "update_star", map[string]interface{}{"star_id": args.StarID}, "error: update_star is not available in this context", callID)
	}

	callArgs := map[string]interface{}{
		"star_id": args.StarID, "summary": args.Summary, "tags": args.Tags,
		"confidence_class": args.ConfidenceClass, "is_personal": args.IsPersonal,
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "update_star", "args": callArgs, "call_id": callID})

	if err := ctx.WeaverUpdateStar(args.StarID, args.Summary, args.Body, args.Tags, args.ConfidenceClass, args.IsPersonal, strings.TrimSpace(args.Reasoning)); err != nil {
		errText := "error: " + err.Error()
		if err == store.ErrStarNotFound {
			errText = "error: no star with that id — call search_stars/read_star to find the right one first"
		}
		return emitToolError(ctx, "update_star", callArgs, errText, callID)
	}

	result := "star updated"

	ctx.Emit("tool_result", map[string]interface{}{"tool": "update_star", "result": result, "call_id": callID})
	return result
}
