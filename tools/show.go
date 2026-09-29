// show is a big, inline artifact viewer — one step above highlight. It
// displays one or more images (or a workspace file) large and inline, right
// where the call happened in the conversation, instead of queued into an
// end-of-message bucket the way highlight/visualize's cards are. See
// docs/plans/show.md for the full design discussion and why a dedicated
// tool was chosen over reusing highlight's card machinery or the
// (nonexistent) attachment-rendering path.
//
// Exactly one source per call:
//   - path: a file already sitting in this thread's code_exec workspace
//     (a code_exec-generated chart, a file fetch_url landed). The original
//     and only source until issue #124.
//   - url: a remote http(s) image, displayed by the user's own browser —
//     no server-side fetch, so no SSRF surface; the blocklist still applies.
//   - image_indices: a hand-picked subset of this turn's image_search
//     candidates (ctx.ImageCandidates). This is the "judge, then show" half
//     of issue #124: image_search no longer dumps every result onto the
//     screen, so this is how the model puts just the good ones there —
//     one index renders large, several render as a gallery.
//
// Purely a display action: unlike view_image's "see" mode, show never
// touches the model's own context — it only ever emits a tool_call/
// tool_result pair whose data carries a URL (or image cards) for the
// frontend to render. If the model needs to actually judge an image
// itself, it calls view_image separately; the two tools deliberately don't
// share a calling convention beyond both resolving a workspace path the
// same way (resolveWorkspaceFilePath, in view_image.go).
//
// No cap on calls per turn — each show call is a distinct, deliberate
// artifact, not one of several interchangeable candidates the way
// highlight's top-N framing is. A single call's image_indices is capped
// (showMaxImages), though: past that it's a wall of photos, not a curated
// pick.
package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"polaris/llm"
)

// showMaxImages bounds one show call's image_indices. Reject-don't-truncate,
// same shape as highlightMaxItems: silently dropping the tail would show
// the user something other than what the model chose.
const showMaxImages = 8

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
						"e.g. a chart code_exec just generated. Pass exactly one of path, url, or image_indices.",
				},
				"url": map[string]interface{}{
					"type": "string",
					"description": "A remote http(s) image URL to display, e.g. one you found on a page you read. " +
						"Pass exactly one of path, url, or image_indices.",
				},
				"image_indices": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "integer"},
					"description": fmt.Sprintf("Numbers of image_search results to display (up to %d) — only the ones worth showing. One renders large, several as a gallery. Pass exactly one of path, url, or image_indices.", showMaxImages),
				},
				"caption": map[string]interface{}{
					"type":        "string",
					"description": "Optional short caption to show under the artifact.",
				},
			},
		},
	},
}

func init() { Register("show", handleShow) }

func handleShow(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Path         string `json:"path"`
		URL          string `json:"url"`
		ImageIndices []int  `json:"image_indices"`
		Caption      string `json:"caption"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "show", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "show",
		"args":    map[string]interface{}{"path": args.Path, "url": args.URL, "image_indices": args.ImageIndices, "caption": args.Caption},
		"call_id": callID,
	})

	sources := 0
	for _, set := range []bool{args.Path != "", args.URL != "", len(args.ImageIndices) > 0} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return showError(ctx, "pass exactly one of path, url, or image_indices", callID)
	}

	switch {
	case args.Path != "":
		return showWorkspaceFile(ctx, args.Path, args.Caption, callID)
	case args.URL != "":
		return showRemoteImage(ctx, args.URL, args.Caption, callID)
	default:
		return showCandidateImages(ctx, args.ImageIndices, args.Caption, callID)
	}
}

// showError reports a failure after handleShow has already emitted its
// tool_call — result only, unlike emitToolError, which would emit a second
// tool_call and leave a stray never-finished timeline item on the client.
func showError(ctx *Context, msg, callID string) string {
	result := "error: " + msg
	ctx.Emit("tool_result", map[string]interface{}{"tool": "show", "result": result, "call_id": callID})
	return result
}

func showWorkspaceFile(ctx *Context, path, caption, callID string) string {
	if _, err := resolveWorkspaceFilePath(ctx, path); err != nil {
		result := "error: " + err.Error()
		log.Warn("show: workspace resolve failed", "path", path, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "show", "result": result, "call_id": callID})
		return result
	}

	url := fmt.Sprintf("/api/workspace/%s/%s", ctx.ThreadID, path)
	result := fmt.Sprintf("now showing %q inline in the conversation", path)
	log.Info("show", "path", path, "thread_id", ctx.ThreadID)
	// SetShow alongside Emit, not instead of it — a live chat client reads
	// this off the streamed event, but Pulsar Daily's tool contexts use a
	// no-op Emit (see gateway/pulsar_daily.go's newDailyToolContext) and
	// need to read it back after agent.Run returns instead. See
	// Context.ShowSnapshot's doc comment.
	ctx.SetShow(url, caption)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool": "show", "result": result, "call_id": callID,
		"url": url, "caption": caption, "path": path,
	})
	return result
}

func showRemoteImage(ctx *Context, rawURL, caption, callID string) string {
	fail := func(msg string) string {
		return showError(ctx, msg, callID)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fail("url must be an absolute http(s) URL")
	}
	if ctx.Blocklist.Blocked(rawURL) {
		return fail("this image's source is blocked and cannot be shown")
	}

	result := "now showing that image inline in the conversation"
	log.Info("show", "url", rawURL)
	ctx.SetShow(rawURL, caption)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool": "show", "result": result, "call_id": callID,
		"url": rawURL, "caption": caption,
	})
	return result
}

func showCandidateImages(ctx *Context, indices []int, caption, callID string) string {
	fail := func(msg string) string {
		return showError(ctx, msg, callID)
	}
	if len(indices) > showMaxImages {
		return fail(fmt.Sprintf("too many images (%d) — show supports at most %d per call. Pick the strongest "+
			"and call again with fewer.", len(indices), showMaxImages))
	}

	// The model's own order is kept (first occurrence wins on a repeat): it
	// often writes a caption that refers to position ("left", "the last
	// one"), which a re-sort would silently break — seen live in the first
	// real run of this tool.
	var images []Card
	var shown []string
	seen := map[int]bool{}
	for _, n := range indices {
		if seen[n] {
			continue
		}
		seen[n] = true
		card, ok := ctx.ImageCandidate(n)
		if !ok {
			return fail(fmt.Sprintf("image_index %d is out of range — only %d image(s) found by image_search this turn",
				n, len(ctx.ImageCandidatesSnapshot())))
		}
		if ctx.Blocklist.Blocked(card.URL) || ctx.Blocklist.Blocked(card.FullImageURL) {
			return fail(fmt.Sprintf("image %d comes from a blocked source and cannot be shown", n))
		}
		images = append(images, card)
		shown = append(shown, fmt.Sprint(n))
	}

	first := images[0].FullImageURL
	if first == "" {
		first = images[0].ImageURL
	}
	// The model can't see what it just displayed, so the result has to say
	// what the screen looks like — otherwise it guesses at order/layout in
	// its caption (seen live: it agonized over which image was "left").
	var b strings.Builder
	if len(images) == 1 {
		fmt.Fprintf(&b, "now showing image %s (%q) inline, large, in the conversation.", shown[0], images[0].Title)
	} else {
		fmt.Fprintf(&b, "now showing %d images inline as one photo gallery, in this order:\n", len(images))
		for i, card := range images {
			fmt.Fprintf(&b, "%d. image %s — %s (%s)\n", i+1, shown[i], card.Title, card.Subtitle)
		}
		b.WriteString("The gallery is a two-column masonry layout that fills down each column, so on-screen " +
			"position (left/right/top/bottom/first/last) is NOT reliable — never describe images by position. " +
			"Refer to them by what they show or their title.")
	}
	b.WriteString(" The user sees exactly these, so don't re-list them in prose beyond what adds something.")
	result := b.String()
	log.Info("show", "images", len(images))
	ctx.SetShow(first, caption)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool": "show", "result": result, "call_id": callID,
		"images": images, "caption": caption,
	})
	return result
}

// SetShow records the most recent show call's resolved URL/caption —
// called from show.go's handleShow alongside its normal ctx.Emit, not
// instead of it. Safe to call concurrently.
func (c *Context) SetShow(url, caption string) {
	c.showMuLock.Lock()
	defer c.showMuLock.Unlock()
	c.showURL = url
	c.showCaption = caption
}

// ShowSnapshot returns the most recent show call's URL/caption, or ""
// for both if show was never called this run.
func (c *Context) ShowSnapshot() (url, caption string) {
	c.showMuLock.Lock()
	defer c.showMuLock.Unlock()
	return c.showURL, c.showCaption
}
