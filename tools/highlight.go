// highlight is a generic, reusable card tool: it takes items the model
// already found this turn ({title, url, price?, image_url?}) and promotes
// them into a masonry grid of link-out cards instead of prose. It has no
// idea what a "product" is — shopping is its first real caller (via the
// Shopper focus mode, see agent/driver.go's FocusModeShopper), not the only
// reason it exists. No network calls of its own: every url/title/price must
// come from something the model actually read this turn (a web_search
// result or a web_read page), never recalled from training data — see
// tools/descriptions/highlight.yaml for the prompt-level instruction that
// enforces this, since the schema itself can't. See
// docs/plans/shopping-mode.md for the full design.
package tools

import (
	"encoding/json"
	"fmt"

	"polaris/llm"
)

// highlightMaxItems mirrors visualize's reject-don't-truncate pattern
// (tools/visualize.go) — five is the actual ask ("top 3 or 5"), not a soft
// target with headroom above it, so exceeding it is a tool error the model
// can correct (pick the strongest candidates), not a silent drop.
const highlightMaxItems = 5

var highlightDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "highlight",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/highlight.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"items": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					"description": "1-5 items to render as cards. Every url/title/price must come from " +
						"something you actually read this turn — never recalled from memory.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"title": map[string]interface{}{"type": "string"},
							"url":   map[string]interface{}{"type": "string"},
							"price": map[string]interface{}{"type": "string",
								"description": "Optional free text, e.g. \"$129.99\" or \"~$40, limited stock\" — not a structured amount."},
							"image_url": map[string]interface{}{"type": "string"},
						},
						"required": []string{"title", "url"},
					},
				},
			},
			"required": []string{"items"},
		},
	},
}

func init() { Register("highlight", handleHighlight) }

func handleHighlight(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Items []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Price    string `json:"price"`
			ImageURL string `json:"image_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "highlight", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "highlight", "args": map[string]interface{}{"count": len(args.Items)}, "call_id": callID})

	fail := func(msg string) string {
		result := "error: " + msg
		ctx.Emit("tool_result", map[string]interface{}{"tool": "highlight", "result": result, "call_id": callID})
		return result
	}

	if len(args.Items) == 0 {
		return fail("items is required and must have at least one entry.")
	}
	if len(args.Items) > highlightMaxItems {
		return fail(fmt.Sprintf("too many items (%d) — highlight supports at most %d. Pick the strongest "+
			"candidates and call again with fewer.", len(args.Items), highlightMaxItems))
	}

	for _, item := range args.Items {
		if item.Title == "" || item.URL == "" {
			return fail("every item requires a title and url.")
		}
		if ctx.Blocklist.Blocked(item.URL) {
			return fail(fmt.Sprintf("%s is a blocked source and can't be highlighted.", item.URL))
		}
	}

	for _, item := range args.Items {
		ctx.AddCard(Card{Title: item.Title, Price: item.Price, ImageURL: item.ImageURL, URL: item.URL, Kind: "highlight"})
	}

	result := fmt.Sprintf("%d item(s) are now attached to this turn's answer as cards — no need to also "+
		"list them in prose.", len(args.Items))
	log.Info("highlight", "count", len(args.Items))
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":    "highlight",
		"result":  result,
		"cards":   ctx.CardsSnapshot(),
		"call_id": callID,
	})
	return result
}
