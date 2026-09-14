// view_image lets the model actually reason about a specific image it
// found via image_search, instead of only being told a gallery exists —
// see docs/plans/view-image.md for the full design discussion. Two modes:
// "describe" (always available) asks a vision-capable model to describe
// the image and folds that text back as the tool result, reusing the same
// DescribeImage mechanism gateway's resolveAttachment already uses for
// uploaded images. "see" goes further: it inserts the actual image into
// the live conversation as a synthetic follow-up user turn (see
// llm.ChatMessage.ImageURLs), so a genuinely vision-capable model looks at
// real pixels instead of reading a description of them — but only when
// this thread's own model is multimodal (ctx.Multimodal).
//
// "see" stays in the tool's parameter schema unconditionally rather than
// being hidden when the model isn't multimodal — catalog.go's Requires
// mechanism only gates whole-tool availability, not one parameter's enum,
// and building that machinery for a single parameter wasn't worth it for
// v1. handleViewImage enforces the gate instead, with a clear error
// pointing the model back at "describe" — same "fail with a helpful
// message, not silently" shape every other tool error in this package
// already uses.
//
// card_index is a 1-based, absolute position into ctx.CardsSnapshot() —
// the same numbering image_search's own result text now hands the model
// (see finishImageSearch in image_search.go) — deliberately not a raw URL:
// an index is strictly less for the model to juggle, and it structurally
// can't reference anything that didn't come from a genuine search result.
//
// path is the second image source, now that code_exec's per-thread
// workspace exists (issue #42 shipped): a file already sitting in
// <CodeExecWorkspaceDir>/<ThreadID>/<path> — a code_exec-generated chart,
// or (once fetch-and-workspace-tools.md's fetch_url ships) a fetched file.
// Resolved the same defensive way as any other user-influenced path join
// in this codebase: filepath.Join then a filepath.Rel check that the
// result didn't escape the thread's own workspace root via "..".
// card_index and path are mutually exclusive — exactly one is required.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"polaris/llm"
)

var viewImageDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "view_image",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/view_image.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"card_index": map[string]interface{}{
					"type": "integer",
					"description": "Which image to view, by its number from a prior image_search result " +
						"(e.g. \"images 4-6\" means pass 4, 5, or 6). 1-indexed. Mutually exclusive with path — " +
						"pass exactly one of the two.",
				},
				"path": map[string]interface{}{
					"type": "string",
					"description": "Path (relative to this conversation's workspace) to an image file already " +
						"there — e.g. a chart code_exec just generated. Mutually exclusive with card_index — " +
						"pass exactly one of the two.",
				},
				"mode": map[string]interface{}{
					"type": "string",
					"enum": []string{"describe", "see"},
					"description": "\"describe\" (default) gets back a thorough text description — always available. " +
						"\"see\" actually shows you the image directly in this conversation so you can judge it " +
						"yourself — only available when you are a multimodal (vision-capable) model; use it when you " +
						"need to genuinely look at something (compare visual details, judge whether a generated chart " +
						"looks right) rather than just confirm a routine fact a description would already answer.",
				},
				"instructions": map[string]interface{}{
					"type": "string",
					"description": "Optional, \"describe\" mode only: what specifically to focus on (e.g. \"the fabric " +
						"texture and color of the sleeves\") instead of a full general description.",
				},
			},
		},
	},
}

func init() { Register("view_image", handleViewImage) }

// maxViewImageBytes bounds how much of a fetched image is ever buffered
// into memory — same reasoning and same order of magnitude as web_read's
// maxResponseBytes, just tighter (an image worth describing/viewing has no
// business being tens of megabytes).
const maxViewImageBytes = 15 << 20 // 15MB

func handleViewImage(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		CardIndex    int    `json:"card_index"`
		Path         string `json:"path"`
		Mode         string `json:"mode"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "view_image", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "view_image",
		"args":    map[string]interface{}{"card_index": args.CardIndex, "path": args.Path, "mode": args.Mode, "instructions": args.Instructions},
		"call_id": callID,
	})

	if (args.CardIndex >= 1) == (args.Path != "") {
		return emitToolError(ctx, "view_image", map[string]interface{}{"card_index": args.CardIndex, "path": args.Path},
			"error: pass exactly one of card_index or path", callID)
	}

	mode := args.Mode
	if mode == "" {
		mode = "describe"
	}
	if mode != "describe" && mode != "see" {
		return emitToolError(ctx, "view_image", map[string]interface{}{"mode": args.Mode},
			fmt.Sprintf("error: unknown mode %q — must be \"describe\" or \"see\"", args.Mode), callID)
	}
	if mode == "see" && !ctx.Multimodal {
		return emitToolError(ctx, "view_image", map[string]interface{}{"mode": args.Mode},
			"error: \"see\" mode is only available to a multimodal model — you are not one for this thread, "+
				"use mode: \"describe\" instead", callID)
	}

	var data []byte
	var mimeType string
	var source string // for the "see" caption and logs, e.g. `card 3 ("sunset")` or `workspace file "chart.png"`

	if args.Path != "" {
		var err error
		data, mimeType, err = readWorkspaceImageBytes(ctx, args.Path)
		if err != nil {
			result := "error: " + err.Error()
			log.Warn("view_image: workspace read failed", "path", args.Path, "err", err)
			ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": result, "call_id": callID})
			return result
		}
		source = fmt.Sprintf("workspace file %q", args.Path)
	} else {
		cards := ctx.CardsSnapshot()
		if args.CardIndex > len(cards) {
			return emitToolError(ctx, "view_image", map[string]interface{}{"card_index": args.CardIndex},
				fmt.Sprintf("error: card_index %d is out of range — only %d card(s) exist this turn", args.CardIndex, len(cards)), callID)
		}
		card := cards[args.CardIndex-1]
		imageURL := card.FullImageURL
		if imageURL == "" {
			imageURL = card.ImageURL
		}
		if imageURL == "" {
			return emitToolError(ctx, "view_image", map[string]interface{}{"card_index": args.CardIndex},
				fmt.Sprintf("error: card %d has no image to view", args.CardIndex), callID)
		}
		if ctx.Blocklist.Blocked(imageURL) {
			// Same check web_read.go applies before fetching any model-directed
			// URL — a card's image can come from anywhere a search engine
			// indexed, including a source the operator has explicitly
			// blocklisted, and view_image fetching it anyway (then describing
			// it or inserting it straight into the live conversation in "see"
			// mode) would silently bypass that policy for this one path while
			// web_read still enforces it for the same domain.
			return emitToolError(ctx, "view_image", map[string]interface{}{"card_index": args.CardIndex},
				"error: this image's source is blocked and cannot be viewed", callID)
		}

		var err error
		data, mimeType, err = fetchImageBytes(ctx.Ctx, imageURL)
		if err != nil {
			result := "error: fetching image: " + err.Error()
			log.Warn("view_image: fetch failed", "url", imageURL, "err", err)
			ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": result, "call_id": callID})
			return result
		}
		source = fmt.Sprintf("card %d (%q)", args.CardIndex, card.Title)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(data)

	if mode == "see" {
		caption := fmt.Sprintf("Here's the image from %s.", source)
		ctx.AddPendingImageMessage(llm.ChatMessage{
			Role:      "user",
			Content:   caption,
			ImageURLs: []string{fmt.Sprintf("data:%s;base64,%s", mimeType, imageBase64)},
		})
		result := fmt.Sprintf("now viewing %s directly — it'll appear as your next message", source)
		log.Info("view_image", "mode", "see", "source", source)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": result, "call_id": callID})
		return result
	}

	if ctx.DescribeImage == nil {
		result := "error: no multimodal model is configured to describe images"
		ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": result, "call_id": callID})
		return result
	}
	description, cost, err := ctx.DescribeImage(ctx.Ctx, imageBase64, mimeType, args.Instructions)
	if err != nil {
		result := "error: describing image: " + err.Error()
		log.Warn("view_image: describe failed", "source", source, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": result, "call_id": callID})
		return result
	}
	ctx.AddCost(cost)
	log.Info("view_image", "mode", "describe", "source", source)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "view_image", "result": description, "call_id": callID})
	return description
}

// fetchImageBytes downloads an image URL through the same SSRF/DNS-
// rebinding-safe dialer web_read.go's own fetches use (SafeDialContext) —
// a card's image URL ultimately traces back to a search result, which is
// no more trustworthy than any other URL a search engine returned. Returns
// the raw bytes and a best-effort mime type (the response's own
// Content-Type if it's a real image type, otherwise sniffed from the
// content itself via http.DetectContentType).
func fetchImageBytes(ctx context.Context, rawURL string) (data []byte, mimeType string, err error) {
	client := &http.Client{Transport: &http.Transport{DialContext: dialContext}}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", httpUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("url returned status %d", resp.StatusCode)
	}

	data, err = io.ReadAll(io.LimitReader(resp.Body, maxViewImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading response body: %w", err)
	}
	if len(data) > maxViewImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d byte limit", maxViewImageBytes)
	}

	mimeType = resp.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}

// resolveWorkspaceFilePath validates relPath against ctx's per-thread
// code_exec workspace (<CodeExecWorkspaceDir>/<ThreadID>/<relPath>) and
// returns its real absolute path on disk — shared by view_image's "path"
// source and show.go, the two tools that read a workspace file by
// model-supplied relative path (a future fetch_url,
// docs/plans/fetch-and-workspace-tools.md, writes into this same
// directory). relPath is model-supplied, so it's resolved with
// filepath.Join then checked via filepath.Rel that the result didn't
// escape the thread's own workspace root via ".." — the standard defense
// against a path-traversal read of an unrelated thread's files or the
// host filesystem beyond the workspace root.
func resolveWorkspaceFilePath(ctx *Context, relPath string) (string, error) {
	if ctx.CodeExecWorkspaceDir == "" || ctx.ThreadID == "" {
		return "", fmt.Errorf("this deployment has no code-execution workspace configured")
	}
	base := filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID)
	target := filepath.Join(base, relPath)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace directory")
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no file %q in this conversation's workspace", relPath)
		}
		return "", fmt.Errorf("opening workspace file: %w", err)
	}
	return target, nil
}

// readWorkspaceImageBytes reads an image file out of the current thread's
// code_exec workspace via resolveWorkspaceFilePath above.
func readWorkspaceImageBytes(ctx *Context, relPath string) (data []byte, mimeType string, err error) {
	target, err := resolveWorkspaceFilePath(ctx, relPath)
	if err != nil {
		return nil, "", err
	}

	f, err := os.Open(target)
	if err != nil {
		return nil, "", fmt.Errorf("opening workspace file: %w", err)
	}
	defer f.Close()

	data, err = io.ReadAll(io.LimitReader(f, maxViewImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading workspace file: %w", err)
	}
	if len(data) > maxViewImageBytes {
		return nil, "", fmt.Errorf("image exceeds %d byte limit", maxViewImageBytes)
	}

	mimeType = http.DetectContentType(data)
	return data, mimeType, nil
}
