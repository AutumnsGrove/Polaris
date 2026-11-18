// image_search returns real photos for a query — thumbnail, source page,
// title, source domain — a different result shape entirely from
// web_search's snippet-plus-link, and the only tool that populates
// Card.Kind "image" (see registry.go's Card doc comment).
//
// Brave's Image Search endpoint is the primary, SearXNG's "images" category
// only a fallback — the reverse of web_search's order, deliberately. Measured
// live 2026-11-18: SearXNG's image engines are mostly *suspended* (brave.images
// "too many requests", startpage "CAPTCHA", duckduckgo "parsing error") so a
// query is answered from whatever scrapers survive, a third to a half of the
// returned thumbnails are hotlinks that 403 or time out, and titles are often
// empty (one haircut query returned a lucide icon). Worse, SearXNG can never
// report itself Degraded for a non-"general" category (search/searxng.go's
// Degraded computation only counts engines for category ""), so the old
// SearXNG-first order could not escalate away from that: Brave only fired if a
// *web* search had already tripped the shared cooldown. Brave's thumbnails come
// from its own CDN instead of the source page's hotlink, and it is the one
// provider here with a real image product — no third tier, since Parallel has
// none and Tavily's include_images only returns images already embedded on a
// text search's pages. See docs/plans/search-provider-tiers.md.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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

	// Brave first, SearXNG only as a fallback — see this file's package doc
	// for why that order is deliberately the reverse of web_search's.
	if formatted, ok := braveImageSearch(ctx, args.Query, args.Count, args.AttachGallery, callID); ok {
		return formatted
	}
	return searxngImageSearch(ctx, args.Query, args.Count, args.AttachGallery, callID)
}

// braveImageSearch is image_search's primary path: Brave's separate Image
// Search endpoint, cap-checked before the call and usage-recorded only on
// success, same shape as web_search's own braveFallback. Images may draw on
// Brave's whole MonthlyCap; web-shaped callers stop at brave.WebSearchCeiling,
// which is what reserves the last slice of a busy month for exactly this tool.
//
// Returns ok=false — never an error string — for every "Brave can't help this
// call" case (no client wired, usage check failed, cap reached, request error,
// empty result) so the caller can fall through to SearXNG. The reason is
// logged, not reported: the model only sees whichever provider's tag actually
// answered, which is the honest signal for a turn.
func braveImageSearch(ctx *Context, query string, count int, attachGallery bool, callID string) (result string, ok bool) {
	if ctx.Brave == nil || ctx.BraveUsageThisMonth == nil {
		return "", false
	}
	used, err := ctx.BraveUsageThisMonth()
	if err != nil {
		log.Warn("image_search: checking brave usage failed, falling back to searxng", "query", query, "err", err)
		return "", false
	}
	if used >= brave.MonthlyCap {
		log.Warn("image_search: brave monthly cap reached, falling back to searxng", "query", query, "used", used, "cap", brave.MonthlyCap)
		return "", false
	}

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
		log.Warn("image_search: brave failed, falling back to searxng", "query", query, "err", err)
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
	if len(found) == 0 {
		log.Info("image_search: brave returned no usable results, falling back to searxng", "query", query)
		return "", false
	}
	// Deliberately no dead-thumbnail filter here: Brave serves every thumbnail
	// from its own CDN (imgs.search.brave.com), which is exactly the hotlink
	// problem searxngImageSearch has to defend against instead.
	return finishImageSearch(ctx, "Brave", query, found, attachGallery, callID), true
}

// searxngImageSearch is image_search's fallback: the instance's own "images"
// category, used only when Brave is unconfigured, capped, failing, or returned
// nothing usable. It has no "is SearXNG degraded?" test of its own to escalate
// with (there is nothing below it), only the empty-vs-outage distinction the
// degraded message keeps honest — see this file's package doc for why a
// "images" query can't self-detect an outage anyway.
func searxngImageSearch(ctx *Context, query string, count int, attachGallery bool, callID string) string {
	if ctx.SearXNG == nil {
		log.Warn("image_search: no provider available", "query", query)
		return emitImageDegraded(ctx, query, callID, "no image provider is configured — Brave isn't wired up and there's no SearXNG "+
			"instance to fall back to")
	}

	dedupKey := searchDedupKey("searxng-images", query, "images", 1, count)
	resp, _, err := dedupedCall(ctx, dedupKey, func() (*search.SearchResponse, error) {
		return ctx.SearXNG.Search(ctx.Ctx, query, count, "images", 1)
	})
	if err != nil {
		log.Warn("image_search: searxng failed", "query", query, "err", err)
		return emitImageDegraded(ctx, query, callID, "Brave couldn't answer and SearXNG's fallback failed too ("+err.Error()+")")
	}
	if len(resp.Results) == 0 && resp.Degraded {
		log.Warn("image_search: searxng degraded", "query", query)
		return emitImageDegraded(ctx, query, callID, "Brave couldn't answer and SearXNG's image engines are "+
			"being rate-limited or blocked")
	}
	if len(resp.Results) == 0 {
		log.Info("image_search: no results", "query", query)
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
	// Drop thumbnails that don't actually load before the model ever sees
	// them. SearXNG hotlinks the source page's own thumbnail, and measured
	// live on 2026-11-18 roughly a third to a half of them 403 or time out
	// (the engines still answering are Bing's and Flickr's CDNs plus
	// Pinterest's hotlink protection). A dead tile is worse than an absent
	// one: it still costs the model a view_image call to find out.
	found = filterDeadThumbnails(ctx, found, ctx.ProbeThumbnail)
	if len(found) == 0 {
		log.Info("image_search: no reachable thumbnails", "query", query)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": "no images found", "call_id": callID})
		return "no images found"
	}
	return finishImageSearch(ctx, "SearXNG", query, found, attachGallery, callID)
}

// emitImageDegraded is the one honest way to report "no image search right
// now": it says what failed, and tells the model not to paper the gap over
// from memory. Every no-provider path above goes through it rather than
// returning a bare error string, which a model can mistake for a bad query.
func emitImageDegraded(ctx *Context, query, callID, reason string) string {
	msg := "image search is degraded and unavailable right now — " + reason +
		". Say plainly that image search is down right now rather than describing images from memory."
	ctx.Emit("tool_result", map[string]interface{}{"tool": "image_search", "result": msg, "call_id": callID})
	return msg
}

// Thumbnail reachability probe — bounded so a slow or dead image host can't
// stall the tool: eight at a time, each with its own short deadline.
const (
	thumbnailProbeWorkers = 8
	thumbnailProbeTimeout = 3 * time.Second
)

// probeThumbnail is a package-level var purely so tests can stub it — the
// fixture URLs in the tests point at example.com and must not be dialed.
// Context.ProbeThumbnail is the per-call replacement (see its comment); this
// is the default.
var probeThumbnail = reachableThumbnail

// reachableThumbnail reports whether raw loads as an image. The User-Agent is
// the package's ordinary identifying one: measured against the CDNs SearXNG
// actually surfaces (Bing, Flickr, Pinterest) it performs the same as a
// browser string, so there is no reason to impersonate one here.
//
// The transport is dialContext (safeDialContext), not a bare http.Client:
// these URLs are scraper output, so a page can set og:image to
// http://127.0.0.1:8899/... or a metadata endpoint, and once an image engine
// indexes it this probe would GET it from the host network — the identical
// data flow web_read.go's SafeDialContext doc comment calls out for Pulsar
// Daily's Picture of the Day. Redirects make a plain client worse still,
// since it follows them by default. Callers also refuse to probe a
// blocklisted source at all (see filterDeadThumbnails).
func reachableThumbnail(ctx context.Context, raw string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", httpUserAgent)
	// Range: bytes=0-0 asks for one byte, so a large image costs a single
	// packet rather than however many MB the host decides to stream.
	req.Header.Set("Range", "bytes=0-0")

	client := &http.Client{Timeout: thumbnailProbeTimeout, Transport: &http.Transport{DialContext: dialContext}}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	// A 200 serving an HTML error/interstitial page is the other common way
	// these hotlinks die; hosts that omit the type entirely are given the
	// benefit of the doubt rather than being filtered on a header.
	ct := resp.Header.Get("Content-Type")
	return ct == "" || strings.HasPrefix(ct, "image/") || ct == "application/octet-stream"
}

// filterDeadThumbnails keeps the candidates whose thumbnails actually load,
// preserving order (and therefore the model-visible numbering). Runs the
// probes concurrently; each one is independently bounded. probe may be nil
// (production), meaning the package's real reachability check.
//
// A thumbnail on a blocklisted domain is dropped without ever being dialed —
// the one check that matters more than reachability, since probing it is
// itself the request the blocklist exists to prevent (same reasoning as
// view_image's Blocked check before fetching a candidate).
func filterDeadThumbnails(tctx *Context, cards []Card, probe func(context.Context, string) bool) []Card {
	if probe == nil {
		probe = probeThumbnail
	}
	if len(cards) == 0 {
		return cards
	}
	ctx := tctx.Ctx
	alive := make([]bool, len(cards))
	sem := make(chan struct{}, thumbnailProbeWorkers)
	var wg sync.WaitGroup
	for i, card := range cards {
		if tctx.Blocklist.Blocked(card.ImageURL) {
			log.Info("image_search: not probing a blocklisted thumbnail", "url", card.ImageURL)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			alive[i] = probe(ctx, card.ImageURL)
		}()
	}
	wg.Wait()

	kept := make([]Card, 0, len(cards))
	dropped := 0
	for i, card := range cards {
		if alive[i] {
			kept = append(kept, card)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		log.Info("image_search: dropped unreachable thumbnails", "dropped", dropped, "kept", len(kept))
	}
	return kept
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
