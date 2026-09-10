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
					"description": "True only if this is an inference about who the person IS, not a topic they discussed. Always starts the star as proposed, regardless of confidence_class.",
				},
			},
			"required": []string{"title", "category", "summary", "confidence_class"},
		},
	},
}

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
	if ctx.WeaverCreateStar == nil {
		return emitToolError(ctx, "create_star", map[string]interface{}{"title": args.Title}, "error: create_star is not available in this context", callID)
	}

	callArgs := map[string]interface{}{
		"title": args.Title, "category": args.Category, "summary": args.Summary,
		"tags": args.Tags, "confidence_class": args.ConfidenceClass, "is_personal": args.IsPersonal,
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "create_star", "args": callArgs, "call_id": callID})

	id, err := ctx.WeaverCreateStar(args.Title, args.Category, args.Summary, args.Body, args.Tags, args.ConfidenceClass, args.IsPersonal)
	if err != nil {
		return emitToolError(ctx, "create_star", callArgs, "error: "+err.Error(), callID)
	}

	status := "auto"
	if args.IsPersonal {
		status = "proposed"
	}
	result := fmt.Sprintf("created star_id=%d, status=%s", id, status)

	ctx.Emit("tool_result", map[string]interface{}{"tool": "create_star", "result": result, "call_id": callID})
	return result
}
