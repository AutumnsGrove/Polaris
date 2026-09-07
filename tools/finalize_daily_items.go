// finalize_daily_items lets a Pulsar Daily list-block generation
// (headlines/trending/custom blocks — see tools.Context.PulsarDailyItems)
// end with a structured list of distinct stories instead of one merged
// prose blob. Only ever offered on that specific generation (see
// catalog.go's "pulsar_daily_items" Requires case) — never appears in a
// normal chat or pulse turn. Mirrors finalize_pulsar_prompt.go's shape:
// calling this ends the turn immediately, same as that tool.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

var finalizeDailyItemsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "finalize_daily_items",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/finalize_daily_items.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"items": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"title":   map[string]interface{}{"type": "string", "description": "This story's own short headline."},
							"summary": map[string]interface{}{"type": "string", "description": "1-2 sentences on this story alone — never mention any other item."},
							"source":  map[string]interface{}{"type": "string", "description": "Short outlet/site name, e.g. \"The Verge\" — not the URL."},
							"url":     map[string]interface{}{"type": "string", "description": "The source link for this story, if you have one."},
						},
						"required": []string{"title", "summary"},
					},
					"description": "The 3-5 most significant distinct stories found, most significant first — never merge two stories into one item, and don't pad the list with minor stories just to fill it out.",
				},
			},
			"required": []string{"items"},
		},
	},
}

func init() { Register("finalize_daily_items", handleFinalizeDailyItems) }

func handleFinalizeDailyItems(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Items []DailyItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "finalize_daily_items", nil, "error: "+err.Error(), callID)
	}
	var cleaned []DailyItem
	for _, it := range args.Items {
		it.Title = strings.TrimSpace(it.Title)
		it.Summary = strings.TrimSpace(it.Summary)
		it.Source = strings.TrimSpace(it.Source)
		it.URL = strings.TrimSpace(it.URL)
		if it.Title == "" || it.Summary == "" {
			continue
		}
		cleaned = append(cleaned, it)
	}
	if len(cleaned) == 0 {
		return emitToolError(ctx, "finalize_daily_items", map[string]interface{}{"items": args.Items},
			"error: at least one item with a title and summary is required", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "finalize_daily_items",
		"args":    map[string]interface{}{"items": cleaned},
		"call_id": callID,
	})

	ctx.SetDailyItemsFinal(&DailyItemsFinal{Items: cleaned})

	result := "(turn paused — the drafted items have been handed back to the Daily pipeline)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "finalize_daily_items", "result": result, "call_id": callID})
	return result
}
