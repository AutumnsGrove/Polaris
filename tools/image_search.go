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
	"strings"

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
				"attach_gallery": map[string]interface{}{"type": "boolean",
					"description": "Almost always false. False (the norm) returns a numbered list of candidates " +
						"the user has NOT seen yet — you then look at them with view_image and put just the good ones " +
						"on screen with show (image_indices). True skips that judging step and dumps EVERY result " +
						"onto the user's screen as a gallery: use it only when the user's whole request is just " +
						"\"show me pictures of X\" with nothing for you to pick between. If you might drop, " +
						"compare or filter any of the results, pass false."},
			},
			"required": []string{"query", "attach_gallery"},
		},
	},
}

func init() { Register("image_search", handleImageSearch) }

func handleImageSearch(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
		// A model that omits the (schema-required) field gets the safe
		// judge-first behavior — the gallery dump is the opt-in.
		AttachGallery bool `json:"attach_gallery"`
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
			} else if formatted, ok := braveImageFallback(ctx, args.Query, args.Count, args.AttachGallery, callID); ok {
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

	var found []Card
	for _, r := range resp.Results {
		if r.Thumbnail == "" {
			continue
		}
		found = append(found, Card{
			Title: r.Title, Subtitle: hostnameOf(r.URL), ImageURL: r.Thumbnail, FullImageURL: r.FullImageURL, URL: r.URL, Kind: "image",
		})
	}
	return finishImageSearch(ctx, "SearXNG", args.Query, found, args.AttachGallery, callID)
}

// braveImageFallback tries Brave's Image Search API once SearXNG has
// confirmed itself degraded and the caller has already checked the
// shared brave usage cap (see handleImageSearch) — same
// checked-before-call, incremented-only-on-success shape as web_search's
// braveFallback. Returns ok=false on any failure or empty result so the
// caller falls through to the plain "degraded" message.
func braveImageFallback(ctx *Context, query string, count int, attachGallery bool, callID string) (result string, ok bool) {
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

	var found []Card
	for _, r := range resp.Results {
		if r.ImageSrc == "" {
			continue
		}
		source := r.Source
		if source == "" {
			source = hostnameOf(r.URL)
		}
		found = append(found, Card{
			Title: r.Title, Subtitle: source, ImageURL: r.ImageSrc, FullImageURL: r.FullImageURL, URL: r.URL, Kind: "image",
		})
	}
	return finishImageSearch(ctx, "Brave (SearXNG degraded)", query, found, attachGallery, callID), true
}

// finishImageSearch records this call's results in the turn's image
// candidate pool (ctx.ImageCandidates) and tells the model what it found.
// The pool is the numbering space view_image, fetch_url and show's
// image_indices all resolve against, so the numbers reported here are the
// ones the model passes to them — stable across calls (a re-surfaced image
// keeps its earlier number, see AddImageCandidate).
//
// By default nothing is rendered: the model is handed a numbered list
// (title + source domain, enough to weed out the obvious junk without a
// vision call) and told the user hasn't seen any of it, so it has to judge
// and then show only what earned it (issue #124 — the old behavior dumped
// every result onto the user's screen before the model had looked at any).
// attachGallery is the explicit opt-out for the plain "show me pictures of
// X" request: it additionally adds every result to ctx.Cards, which the
// frontend renders as an end-of-turn gallery.
func finishImageSearch(ctx *Context, provider, query string, found []Card, attachGallery bool, callID string) string {
	numbers := make([]int, len(found))
	for i, card := range found {
		numbers[i] = ctx.AddImageCandidate(card)
		if attachGallery {
			ctx.AddCard(card)
		}
	}

	var result string
	switch {
	case len(found) == 0:
		result = fmt.Sprintf("[via %s] found no usable images for %q.", provider, query)
	case attachGallery:
		result = fmt.Sprintf("[via %s] found %d image(s) for %q — all of them are attached to this turn's answer as a "+
			"gallery, no need to describe them individually in prose unless asked. They're images %s if you want to "+
			"look at one with view_image.", provider, len(found), query, imageNumberRange(numbers))
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "[via %s] found %d image(s) for %q. The user has NOT seen any of them yet — nothing is displayed "+
			"until you call show. Candidates:\n", provider, len(found), query)
		for i, card := range found {
			fmt.Fprintf(&b, "%d. %s (%s)\n", numbers[i], card.Title, card.Subtitle)
		}
		b.WriteString("Titles alone are weak evidence: view_image (image_index, mode \"describe\") to check the ones " +
			"that matter before you pick. Then call show with image_indices listing only the good ones — you may show " +
			"any subset, or none if nothing fits.")
		result = b.String()
	}

	log.Info("image_search", "provider", provider, "query", query, "results", len(found), "attach_gallery", attachGallery)
	payload := map[string]interface{}{"tool": "image_search", "result": result, "call_id": callID}
	if attachGallery {
		payload["cards"] = ctx.CardsSnapshot()
	}
	ctx.Emit("tool_result", payload)
	return result
}

// imageNumberRange renders candidate numbers compactly for the model, e.g.
// "3-7" for a contiguous run, or a comma list when a re-surfaced image
// broke the run.
func imageNumberRange(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	contiguous := true
	for i := 1; i < len(numbers); i++ {
		if numbers[i] != numbers[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous && len(numbers) > 1 {
		return fmt.Sprintf("%d-%d", numbers[0], numbers[len(numbers)-1])
	}
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
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
