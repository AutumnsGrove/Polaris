// read_star is Weaver's full-card lookup — mandatory before update_star or
// link_stars, see docs/plans/constellation.md's "Weaver's tools". Only
// ever offered inside a Weaver shooting-star run.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/store"
)

var readStarDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "read_star",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/read_star.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"star_id": map[string]interface{}{
					"type":        "integer",
					"description": "The star_id from a search_stars result.",
				},
			},
			"required": []string{"star_id"},
		},
	},
}

func init() { Register("read_star", handleReadStar) }

func handleReadStar(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		StarID int64 `json:"star_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "read_star", nil, "error: "+err.Error(), callID)
	}
	if ctx.WeaverReadStar == nil {
		return emitToolError(ctx, "read_star", map[string]interface{}{"star_id": args.StarID}, "error: read_star is not available in this context", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "read_star", "args": map[string]interface{}{"star_id": args.StarID}, "call_id": callID})

	star, err := ctx.WeaverReadStar(args.StarID)
	if err != nil {
		errText := "error: " + err.Error()
		if err == store.ErrStarNotFound {
			errText = fmt.Sprintf("error: no star with id %d", args.StarID)
		}
		return emitToolError(ctx, "read_star", map[string]interface{}{"star_id": args.StarID}, errText, callID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "star_id=%d\ntitle: %s\ncategory: %s\nstatus: %s\nconfidence: %s\nis_personal: %v\ndisabled: %v\ntags: %s\n\nsummary: %s\n\nbody:\n%s",
		star.ID, star.Title, star.Category, star.Status, star.Confidence, star.IsPersonal, star.Disabled, strings.Join(star.Tags, ", "), star.Summary, star.Body)
	if star.Disabled {
		// disabled is a person explicitly removing this star from their
		// library via the overflow menu's Disable action — distinct from
		// status='rejected', but the same "don't quietly resurrect this"
		// signal SearchStars already backs out of by excluding disabled
		// stars from its own results. A star reached here anyway (a stale
		// search result cached earlier in this same run, or a link
		// discovered elsewhere) should be treated the same way Weaver
		// already treats a rejected one.
		b.WriteString("\n\nNOTE: this star is disabled — the person removed it from their library. Treat it like a rejected star: don't update it or link to it.")
	}
	result := b.String()

	ctx.Emit("tool_result", map[string]interface{}{"tool": "read_star", "result": result, "call_id": callID})
	return result
}
