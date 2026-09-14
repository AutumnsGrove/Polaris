// fetch_url downloads a URL the model has already been shown (a
// web_search/web_read citation, or an image_search card) into this
// thread's code_exec workspace, so code_exec can process real files
// (images, CSVs, Parquet, SQLite, ...) it otherwise has no way to reach
// — see docs/plans/fetch-and-workspace-tools.md. The sandbox itself
// stays --network none permanently (code-execution.md's "Network access
// from executed code"); this tool is the one and only host-side,
// content-validated path bytes ever cross into it, and the fetch and
// the later execution deliberately never share a trust boundary — even
// a genuinely malicious downloaded file only ever gets opened inside
// the already-sealed, non-root, resource-capped sandbox.
//
// Provenance is checked, not merely encouraged: url must exactly match
// a URL already in ctx.CitationsSnapshot() — something a prior
// web_search/web_read/other citing tool actually surfaced this
// conversation, not a string the model invented or a lookalike domain
// it guessed at. card_index is the mutually-exclusive alternative
// view_image.go already established for the image_search case, where
// there's no raw URL exposed to check against at all (see
// image_search.go's finishImageSearch) — resolved server-side to the
// real FullImageURL/ImageURL already sitting in that Card, so there's
// no string for the model to mistype or invent in the first place.
//
// Gated the same "docker_only" way as code_exec (see catalog.go) since
// this tool has no purpose without the workspace code_exec creates.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"polaris/llm"
)

// maxFetchURLBytes bounds a fetched file's size — larger than
// maxViewImageBytes (view_image.go) since this also covers real
// datasets (CSV/Parquet) code_exec might reasonably load, but still a
// hard, server-enforced cap rather than trusting a remote
// Content-Length header, same "never trust the header" reasoning as
// web_read.go's own maxResponseBytes (which this matches exactly).
const maxFetchURLBytes = 20 << 20 // 20MB

// fetchURLAllowedMIME is the content-type allowlist — deliberately
// narrow (real data/image formats only), independent of how trusted the
// source URL already is, so a URL that legitimately appeared in a
// search result still can't smuggle an arbitrary executable into the
// workspace by mislabeling it. Checked against both the response's
// declared Content-Type and (when that's empty) a sniff of the actual
// bytes via http.DetectContentType — see fetchURLBytes.
var fetchURLAllowedMIME = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"text/csv": true, "text/plain": true, "application/json": true,
	"text/xml": true, "application/xml": true,
}

// fetchURLAllowedExt is consulted only when the content-type check
// above lands on the generic application/octet-stream — a handful of
// real data formats (Parquet, SQLite) http.DetectContentType can't
// distinguish from arbitrary binary at all, so the model-chosen
// filename's own extension is the only signal available for those.
// Still bounded by the same size cap and same sandbox containment as
// every other fetch.
var fetchURLAllowedExt = map[string]bool{
	".parquet": true, ".sqlite": true, ".sqlite3": true, ".db": true,
}

var fetchURLDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "fetch_url",
		// Description is populated at call time from
		// tools/descriptions/fetch_url.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type": "string",
					"description": "A URL already shown to you as a citation this conversation (from web_search, web_read, or another " +
						"citing tool) — must match one exactly. Mutually exclusive with card_index — pass exactly one of the two.",
				},
				"card_index": map[string]interface{}{
					"type": "integer",
					"description": "Which image_search result to fetch, by its number from a prior result (1-indexed). " +
						"Mutually exclusive with url — pass exactly one of the two.",
				},
				"filename": map[string]interface{}{
					"type":        "string",
					"description": "What to name the file in your workspace, e.g. \"data.csv\" or \"photo.jpg\" — a plain filename, no directories.",
				},
			},
			"required": []string{"filename"},
		},
	},
}

func init() { Register("fetch_url", handleFetchURL) }

func handleFetchURL(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		URL       string `json:"url"`
		CardIndex int    `json:"card_index"`
		Filename  string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "fetch_url", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "fetch_url",
		"args":    map[string]interface{}{"url": args.URL, "card_index": args.CardIndex, "filename": args.Filename},
		"call_id": callID,
	})

	if args.Filename == "" {
		return emitToolError(ctx, "fetch_url", nil, "error: filename is required", callID)
	}
	if strings.ContainsAny(args.Filename, "/\\") || args.Filename == "." || args.Filename == ".." {
		return emitToolError(ctx, "fetch_url", map[string]interface{}{"filename": args.Filename},
			"error: filename must be a plain name with no directories", callID)
	}
	if (args.URL != "") == (args.CardIndex >= 1) {
		return emitToolError(ctx, "fetch_url", map[string]interface{}{"url": args.URL, "card_index": args.CardIndex},
			"error: pass exactly one of url or card_index", callID)
	}
	if ctx.CodeExecWorkspaceDir == "" || ctx.ThreadID == "" {
		result := "error: this deployment has no code-execution workspace configured"
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}

	var targetURL string
	if args.URL != "" {
		found := false
		for _, cit := range ctx.CitationsSnapshot() {
			if cit.URL == args.URL {
				found = true
				break
			}
		}
		if !found {
			return emitToolError(ctx, "fetch_url", map[string]interface{}{"url": args.URL},
				"error: this URL hasn't appeared as a citation in this conversation — fetch_url only accepts a URL "+
					"you were actually shown, not one you're recalling or guessing at", callID)
		}
		targetURL = args.URL
	} else {
		cards := ctx.CardsSnapshot()
		if args.CardIndex > len(cards) {
			return emitToolError(ctx, "fetch_url", map[string]interface{}{"card_index": args.CardIndex},
				fmt.Sprintf("error: card_index %d is out of range — only %d card(s) exist this turn", args.CardIndex, len(cards)), callID)
		}
		card := cards[args.CardIndex-1]
		targetURL = card.FullImageURL
		if targetURL == "" {
			targetURL = card.ImageURL
		}
		if targetURL == "" {
			return emitToolError(ctx, "fetch_url", map[string]interface{}{"card_index": args.CardIndex},
				fmt.Sprintf("error: card %d has no fetchable URL", args.CardIndex), callID)
		}
	}

	if ctx.Blocklist.Blocked(targetURL) {
		return emitToolError(ctx, "fetch_url", map[string]interface{}{"url": targetURL},
			"error: this source is blocked and cannot be fetched", callID)
	}

	data, mimeType, err := fetchURLBytes(ctx.Ctx, targetURL)
	if err != nil {
		result := "error: fetching url: " + err.Error()
		log.Warn("fetch_url: fetch failed", "url", targetURL, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}

	if !fetchURLContentAllowed(mimeType, args.Filename) {
		result := fmt.Sprintf("error: content type %q isn't in the allowed set for fetch_url "+
			"(images, csv/json/text/xml, or a .parquet/.sqlite file)", mimeType)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}

	// 0o777 + explicit Chmod, not just MkdirAll's requested mode — same
	// cross-UID-namespace reasoning as code_exec.go's identical dance:
	// the process umask silently strips the write bit back down
	// otherwise, and codeexec.sh's later container process runs as a
	// different uid than whatever created this directory.
	workspaceDir := filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID)
	if err := os.MkdirAll(workspaceDir, 0o777); err != nil {
		result := "error: couldn't prepare the workspace directory: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}
	if err := os.Chmod(workspaceDir, 0o777); err != nil {
		result := "error: couldn't set workspace directory permissions: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}

	targetPath := filepath.Join(workspaceDir, args.Filename)
	if err := os.WriteFile(targetPath, data, 0o666); err != nil {
		result := "error: writing fetched file: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
		return result
	}

	result := fmt.Sprintf("fetched %d bytes (%s) into your workspace as %q", len(data), mimeType, args.Filename)
	log.Info("fetch_url", "url", targetURL, "filename", args.Filename, "bytes", len(data), "mime_type", mimeType)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "fetch_url", "result": result, "call_id": callID})
	return result
}

// fetchURLContentAllowed checks mimeType (possibly carrying a
// "; charset=..." suffix from a real Content-Type header) against
// fetchURLAllowedMIME, falling back to fetchURLAllowedExt only for the
// generic application/octet-stream — see those maps' doc comments.
func fetchURLContentAllowed(mimeType, filename string) bool {
	base := strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	if fetchURLAllowedMIME[base] {
		return true
	}
	if base == "application/octet-stream" {
		return fetchURLAllowedExt[strings.ToLower(filepath.Ext(filename))]
	}
	return false
}

// fetchURLBytes downloads rawURL through the same SSRF/DNS-rebinding-safe
// dialer web_read.go/view_image.go already use (dialContext ->
// safeDialContext). Returns the raw bytes (capped at maxFetchURLBytes,
// never trusting the remote Content-Length) and a best-effort content
// type: the response's own Content-Type header when present, otherwise
// a sniff of the bytes via http.DetectContentType.
func fetchURLBytes(ctx context.Context, rawURL string) (data []byte, mimeType string, err error) {
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

	data, err = io.ReadAll(io.LimitReader(resp.Body, maxFetchURLBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading response body: %w", err)
	}
	if len(data) > maxFetchURLBytes {
		return nil, "", fmt.Errorf("file exceeds %d byte limit", maxFetchURLBytes)
	}

	mimeType = resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}
