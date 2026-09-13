// image_search returns real photos for a query — thumbnail, source page,
// title, source domain — a different result shape entirely from
// web_search's snippet-plus-link, and the only tool that populates
// Card.Kind "image" (see registry.go's Card doc comment). SearXNG's
// "images" category first (free), Brave's separate Image Search endpoint
// on a degraded SearXNG (see search.SearXNGClient's shared cooldown —
// once web_search's own outage detection trips it, every category
// including "images" reports Degraded too, not just "general"). No third
// tier: Parallel has no image product, and Tavily's include_images
// piggyback only returns images already embedded on a text search's
// pages, not "find me photos of X" in its own right — see
// docs/plans/visualize-and-image-search.md.
package tools

import (
	"encoding/json"
	"fmt"
	"net/url"

	"polaris/brave"
	"polaris/llm"
	"polaris/search"
)

// imageSearchDefaultCount/imageSearchMaxCount bound the tool's own
// "count" argument — a small, fixed request size (10 is enough headroom
// above the ~5 tiles actually rendered to survive a little client-side
// filtering) rather than reusing brave.MaxCount (20, web_search's
// per-request ceiling, tuned for a different product/response size).
const (
	imageSearchDefaultCount = 10
	imageSearchMaxCount     = 20
)

var imageSearchDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "image_search",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/image_search.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "What to find photos of."},
				"count": map[string]interface{}{"type": "integer",
					"description": "How many images to return (default 10, max 20)."},
			},
			"required": []string{"query"},
		},
	},
}

func init() { Register("image_search", handleImageSearch) }

func handleImageSearch(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "image_search", nil, "error: "+err.Error(), callID)
	}
	if args.Query == "" {
		return emitToolError(ctx, "image_search", map[string]interface{}{"query": args.Query}, "error: query is required", callID)
	}
	if args.Count <= 0 || args.Count > imageSearchMaxCount {
		args.Count = imageSearchDefaultCount
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "image_search", "args": map[string]interface{}{"query": args.Query}, "call_id": callID})

	if ctx.SearXNG == nil {
		result := "error: image search is not configured"
		log.Warn("image_search called with no SearXNG client configured", "query", args.Query)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": result, "call_id": callID})
		return result
	}

	dedupKey := searchDedupKey("searxng-images", args.Query, "images", 1, args.Count)
	resp, _, err := dedupedCall(ctx, dedupKey, func() (*search.SearchResponse, error) {
		return ctx.SearXNG.Search(ctx.Ctx, args.Query, args.Count, "images", 1)
	})
	if err != nil {
		log.Warn("image_search: searxng failed", "query", args.Query, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": "error: " + err.Error(), "call_id": callID})
		return "error: " + err.Error()
	}

	if len(resp.Results) == 0 && resp.Degraded {
		// Same "confirmed outage, not an ordinary empty result" distinction
		// web_search's own degraded branch makes — see its doc comment.
		if ctx.Brave != nil && ctx.BraveUsageThisMonth != nil {
			if used, uErr := ctx.BraveUsageThisMonth(); uErr != nil {
				log.Warn("image_search: checking brave usage failed, skipping fallback", "query", args.Query, "err", uErr)
			} else if used >= brave.MonthlyCap {
				log.Warn("image_search: brave monthly cap reached, skipping fallback", "query", args.Query, "used", used, "cap", brave.MonthlyCap)
			} else if formatted, ok := braveImageFallback(ctx, args.Query, args.Count, callID); ok {
				return formatted
			}
		}

		log.Warn("image_search: searxng degraded, no fallback available or it failed too", "query", args.Query)
		msg := "image search is degraded and unavailable right now — SearXNG's image engines are being " +
			"rate-limited or blocked, and Brave's image fallback isn't configured or couldn't help either. " +
			"Say plainly that image search is down right now rather than describing images from memory."
		ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": msg, "call_id": callID})
		return msg
	}

	if len(resp.Results) == 0 {
		log.Info("image_search: no results", "query", args.Query)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": "no images found", "call_id": callID})
		return "no images found"
	}

	startIndex := len(ctx.CardsSnapshot())
	for _, r := range resp.Results {
		if r.Thumbnail == "" {
			continue
		}
		ctx.AddCard(Card{
			Title: r.Title, Subtitle: hostnameOf(r.URL), ImageURL: r.Thumbnail, FullImageURL: r.FullImageURL, URL: r.URL, Kind: "image",
		})
	}
	return finishImageSearch(ctx, "SearXNG", args.Query, startIndex, callID)
}

// braveImageFallback tries Brave's Image Search API once SearXNG has
// confirmed itself degraded and the caller has already checked the
// shared brave usage cap (see handleImageSearch) — same
// checked-before-call, incremented-only-on-success shape as web_search's
// braveFallback. Returns ok=false on any failure or empty result so the
// caller falls through to the plain "degraded" message.
func braveImageFallback(ctx *Context, query string, count int, callID string) (result string, ok bool) {
	dedupKey := searchDedupKey("brave-images", query, "images", 1, count)
	resp, _, err := dedupedCall(ctx, dedupKey, func() (*brave.ImageSearchResponse, error) {
		r, e := ctx.Brave.SearchImages(ctx.Ctx, query, count)
		if e == nil && ctx.IncrementBraveUsage != nil {
			if incErr := ctx.IncrementBraveUsage(); incErr != nil {
				log.Warn("image_search: recording brave usage failed", "query", query, "err", incErr)
			}
		}
		return r, e
	})
	if err != nil {
		log.Warn("image_search: brave fallback failed", "query", query, "err", err)
		return "", false
	}
	if len(resp.Results) == 0 {
		log.Warn("image_search: brave fallback returned no results", "query", query)
		return "", false
	}

	startIndex := len(ctx.CardsSnapshot())
	for _, r := range resp.Results {
		if r.ImageSrc == "" {
			continue
		}
		source := r.Source
		if source == "" {
			source = hostnameOf(r.URL)
		}
		ctx.AddCard(Card{
			Title: r.Title, Subtitle: source, ImageURL: r.ImageSrc, FullImageURL: r.FullImageURL, URL: r.URL, Kind: "image",
		})
	}
	return finishImageSearch(ctx, "Brave (SearXNG degraded)", query, startIndex, callID), true
}

// finishImageSearch reports how many images this call added, and — the
// part that used to be missing entirely — which numbered positions they
// landed at in this turn's card gallery, so the model can actually
// reference one via view_image's card_index parameter (tools/view_image.go)
// instead of only being told images exist without any way to point at a
// specific one. Indices are 1-based, absolute positions in
// ctx.CardsSnapshot() (not a separate "image-only" numbering space) since
// that's the exact slice view_image indexes into — startIndex is the
// snapshot length taken by the caller before this call's own AddCard loop
// ran, so [startIndex, len(cards)) is exactly the range this call added,
// even if earlier tool calls this turn already added other cards.
func finishImageSearch(ctx *Context, provider, query string, startIndex int, callID string) string {
	cards := ctx.CardsSnapshot()
	imageCount := 0
	for _, c := range cards[startIndex:] {
		if c.Kind == "image" {
			imageCount++
		}
	}

	indexHint := "no images were added"
	if imageCount == 1 {
		indexHint = fmt.Sprintf("call it image %d with view_image if you need to actually look at it or describe it", startIndex+1)
	} else if imageCount > 1 {
		indexHint = fmt.Sprintf("call them images %d-%d with view_image if you need to actually look at or describe any of them",
			startIndex+1, len(cards))
	}
	result := fmt.Sprintf("[via %s] found %d image(s) for %q — they're attached to this turn's answer as a gallery, "+
		"no need to describe them individually in prose unless asked; %s.", provider, imageCount, query, indexHint)
	log.Info("image_search", "provider", provider, "query", query, "results", imageCount)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":    "image_search",
		"result":  result,
		"cards":   cards,
		"call_id": callID,
	})
	return result
}

// hostnameOf returns url's host, or the raw string unchanged if it
// doesn't parse — a display fallback, not a correctness-critical path, so
// a malformed URL degrades to showing it verbatim rather than an error.
func hostnameOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}
	return parsed.Hostname()
}
