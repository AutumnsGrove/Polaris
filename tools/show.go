// show is a big, inline artifact viewer — one step above highlight. It
// displays a file already sitting in this thread's code_exec workspace
// (today, always an image — a code_exec-generated chart, or a file
// fetch_url landed) large and inline, right where the call happened in
// the conversation, instead of queued into an end-of-message bucket the
// way image_search/highlight/visualize's cards are. See
// docs/plans/show.md for the full design discussion and why a dedicated
// tool was chosen over reusing highlight's card machinery or the
// (nonexistent) attachment-rendering path.
//
// Purely a display action: unlike view_image's "see" mode, show never
// touches the model's own context — it only ever emits a tool_call/
// tool_result pair whose data carries a URL for the frontend to render.
// If the model needs to actually judge the artifact itself, it calls
// view_image on the same path separately; the two tools deliberately
// don't share a calling convention beyond both resolving a workspace
// path the same way (resolveWorkspaceFilePath, in view_image.go).
//
// No cap on calls per turn — each show call is a distinct, deliberate
// artifact, not one of several interchangeable candidates the way
// highlight's top-N framing is.
package tools

import (
	"encoding/json"
	"fmt"

	"polaris/llm"
)

var showDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "show",
		// Description is populated at call time from
		// tools/descriptions/show.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type": "string",
					"description": "Path (relative to this conversation's workspace) to the file to display — " +
						"e.g. a chart code_exec just generated.",
				},
				"caption": map[string]interface{}{
					"type":        "string",
					"description": "Optional short caption to show under the artifact.",
				},
			},
			"required": []string{"path"},
		},
	},
}

func init() { Register("show", handleShow) }

func handleShow(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Path    string `json:"path"`
		Caption string `json:"caption"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "show", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "show",
		"args":    map[string]interface{}{"path": args.Path, "caption": args.Caption},
		"call_id": callID,
	})

	if args.Path == "" {
		return emitToolError(ctx, "show", nil, "error: path is required", callID)
	}

	if _, err := resolveWorkspaceFilePath(ctx, args.Path); err != nil {
		result := "error: " + err.Error()
		log.Warn("show: workspace resolve failed", "path", args.Path, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "show", "result": result, "call_id": callID})
		return result
	}

	url := fmt.Sprintf("/api/workspace/%s/%s", ctx.ThreadID, args.Path)
	result := fmt.Sprintf("now showing %q inline in the conversation", args.Path)
	log.Info("show", "path", args.Path, "thread_id", ctx.ThreadID)
	// SetShow alongside Emit, not instead of it — a live chat client reads
	// this off the streamed event, but Pulsar Daily's tool contexts use a
	// no-op Emit (see gateway/pulsar_daily.go's newDailyToolContext) and
	// need to read it back after agent.Run returns instead. See
	// Context.ShowSnapshot's doc comment.
	ctx.SetShow(url, args.Caption)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool": "show", "result": result, "call_id": callID,
		"url": url, "caption": args.Caption, "path": args.Path,
	})
	return result
}
