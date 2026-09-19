// create_star writes a new Constellation star — see docs/plans/
// constellation.md's "Weaver's tools" and "Calibration". Only ever offered
// inside a Weaver shooting-star run.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
)

var createStarDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "create_star",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/create_star.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":    map[string]interface{}{"type": "string", "description": "This star's title."},
				"category": map[string]interface{}{"type": "string", "description": "Free-text category, e.g. \"technology\" — reuse an existing one rather than inventing a near-duplicate."},
				"summary":  map[string]interface{}{"type": "string", "description": "The Library card's one-line summary."},
				"body":     map[string]interface{}{"type": "string", "description": "The full Markdown star body."},
				"tags": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Short topic tags, shown as chips.",
				},
				"confidence_class": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"obvious", "fuzzy"},
					"description": "obvious: a fact/topic actually, substantively discussed. fuzzy: real but genuinely uncertain.",
				},
				"is_personal": map[string]interface{}{
					"type":        "boolean",
					"description": "True only if this is an inference about who the person IS, not a topic they discussed. Should be true for essentially every star this agent creates.",
				},
				"reasoning": map[string]interface{}{
					"type":        "string",
					"description": "One sentence: why this belongs in the library. Shown to the person reviewing this star as \"Why this needs a look\" — a vague reason is itself a signal this probably shouldn't be a star.",
				},
			},
			"required": []string{"title", "category", "summary", "confidence_class"},
		},
	},
}

// weaverStarBodyMaxLen enforces the "concise, personal, not a topic
// explainer" bar as a hard, mechanically-checked ceiling rather than relying
// on prompt wording alone — soft "keep it short" guidance already in the
// system prompt was measured not holding on unusually rich conversations (a
// live body over 11,000 characters was observed before this existed).
// Rejecting via emitToolError lets Weaver retry with a trimmed body instead
// of the length limit silently truncating real content. Shared with
// update_star.go (same package). Raised from 800 to 1000 after a full
// backfill audit (2026-09-19) showed the cap working well at scale (avg
// 576 chars, max 796 across 127 real stars) — a small amount of headroom
// for the genuinely rich cases, not a sign 800 wasn't working.
const weaverStarBodyMaxLen = 1000

func init() { Register("create_star", handleCreateStar) }

func handleCreateStar(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Title           string   `json:"title"`
		Category        string   `json:"category"`
		Summary         string   `json:"summary"`
		Body            string   `json:"body"`
		Tags            []string `json:"tags"`
		ConfidenceClass string   `json:"confidence_class"`
		IsPersonal      bool     `json:"is_personal"`
		Reasoning       string   `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "create_star", nil, "error: "+err.Error(), callID)
	}
	args.Title = strings.TrimSpace(args.Title)
	args.Category = strings.TrimSpace(args.Category)
	args.Summary = strings.TrimSpace(args.Summary)
	if args.Title == "" || args.Category == "" || args.Summary == "" {
		return emitToolError(ctx, "create_star", map[string]interface{}{"title": args.Title, "category": args.Category}, "error: title, category, and summary are required", callID)
	}
	if args.ConfidenceClass != "obvious" && args.ConfidenceClass != "fuzzy" {
		return emitToolError(ctx, "create_star", map[string]interface{}{"confidence_class": args.ConfidenceClass}, "error: confidence_class must be \"obvious\" or \"fuzzy\"", callID)
	}
	if len(args.Body) > weaverStarBodyMaxLen {
		return emitToolError(ctx, "create_star", map[string]interface{}{"title": args.Title, "body_len": len(args.Body)},
			fmt.Sprintf("error: body is %d characters, over the %d-character limit — this reads like a topic "+
				"explainer or reference dump rather than a personal fact. Cut it down to the evergreen personal "+
				"takeaway and call create_star again.", len(args.Body), weaverStarBodyMaxLen), callID)
	}
	if ctx.WeaverCreateStar == nil {
		return emitToolError(ctx, "create_star", map[string]interface{}{"title": args.Title}, "error: create_star is not available in this context", callID)
	}

	callArgs := map[string]interface{}{
		"title": args.Title, "category": args.Category, "summary": args.Summary,
		"tags": args.Tags, "confidence_class": args.ConfidenceClass, "is_personal": args.IsPersonal,
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "create_star", "args": callArgs, "call_id": callID})

	id, err := ctx.WeaverCreateStar(args.Title, args.Category, args.Summary, args.Body, args.Tags, args.ConfidenceClass, args.IsPersonal, strings.TrimSpace(args.Reasoning))
	if err != nil {
		return emitToolError(ctx, "create_star", callArgs, "error: "+err.Error(), callID)
	}

	result := fmt.Sprintf("created star_id=%d, status=auto", id)

	ctx.Emit("tool_result", map[string]interface{}{"tool": "create_star", "result": result, "call_id": callID})
	return result
}
