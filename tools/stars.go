// stars is the main assistant's own read-only search over Constellation's
// library (issue #56) — "what has Weaver synthesized about the person
// across past conversations," distinct from memory's point-in-time notes.
// Combines search and read in one tool since star content is short enough
// that returning full bodies is fine cost-wise — see docs/plans/stars-tool.md.
// Always offered (no Requires beyond the StarsSearch/StarsRead closures
// being wired), same "no external API spend" reasoning as memory; both
// closures are nil for a ghost turn, same as memory/search_chats.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/store"
)

var starsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "stars",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/stars.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search the person's Constellation library for stars matching this text.",
				},
				"star_id": map[string]interface{}{
					"type":        "integer",
					"description": "Read one specific star in full by ID instead of searching — usually the ID of a result from a prior stars search.",
				},
			},
		},
	},
}

func init() { Register("stars", handleStars) }

func handleStars(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Query  string `json:"query"`
		StarID int64  `json:"star_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "stars", nil, "error: "+err.Error(), callID)
	}

	if args.StarID != 0 {
		return handleStarsRead(ctx, args.StarID, callID)
	}
	return handleStarsSearch(ctx, args.Query, callID)
}

func handleStarsSearch(ctx *Context, query string, callID string) string {
	logArgs := map[string]interface{}{"query": query}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "stars", "args": logArgs, "call_id": callID})

	if strings.TrimSpace(query) == "" {
		return emitToolError(ctx, "stars", logArgs, "error: query is required when star_id is omitted", callID)
	}
	if ctx.StarsSearch == nil {
		return emitToolError(ctx, "stars", logArgs, "error: stars is not available in this context", callID)
	}

	results, err := ctx.StarsSearch(query, 10)
	if err != nil {
		return emitToolError(ctx, "stars", logArgs, "error: "+err.Error(), callID)
	}

	var result string
	if len(results) == 0 {
		result = "no matching stars found"
	} else {
		var b strings.Builder
		for _, r := range results {
			fmt.Fprintf(&b, "star_id=%d [%s] %s — %s\n", r.ID, r.Category, r.Title, r.Summary)
		}
		result = strings.TrimRight(b.String(), "\n")
	}

	ctx.Emit("tool_result", map[string]interface{}{"tool": "stars", "result": result, "call_id": callID})
	return result
}

func handleStarsRead(ctx *Context, starID int64, callID string) string {
	logArgs := map[string]interface{}{"star_id": starID}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "stars", "args": logArgs, "call_id": callID})

	if ctx.StarsRead == nil {
		return emitToolError(ctx, "stars", logArgs, "error: stars is not available in this context", callID)
	}

	star, err := ctx.StarsRead(starID)
	if err != nil {
		errText := "error: " + err.Error()
		if err == store.ErrStarNotFound {
			errText = fmt.Sprintf("error: no star with id %d", starID)
		}
		return emitToolError(ctx, "stars", logArgs, errText, callID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "star_id=%d\ntitle: %s\ncategory: %s\ntags: %s\n\nsummary: %s\n\nbody:\n%s",
		star.ID, star.Title, star.Category, strings.Join(star.Tags, ", "), star.Summary, star.Body)
	result := b.String()

	ctx.Emit("tool_result", map[string]interface{}{"tool": "stars", "result": result, "call_id": callID})
	return result
}
