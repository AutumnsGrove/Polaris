// web_read implements the "double RAG" behavior: fetch a URL, strip it
// to clean text for free (goquery, no paid extraction API), and — only
// when the model gives a specific instruction like "just the prices" —
// run one extra small LLM pass over that text to pull out just what was
// asked for. Plain reads never pay the second LLM call.
//
// Three failure modes need more than the free path: a dead link, a
// paywall, or a JS-rendered page whose <body> is empty until client-side
// JS runs (goquery only ever sees the raw HTML shell). Each gets a
// progressively more expensive fallback: archive.org's Wayback Machine
// first (free, and often has a pre-paywall or pre-deletion snapshot),
// then Tavily's paid Extract API last (the only one of the three that
// can actually execute JS, since Tavily runs that rendering on their own
// infrastructure — see tavily.Client.Extract).
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/ledongthuc/pdf"

	"polaris/llm"
	"polaris/prompts"
	"polaris/search"
)

var webReadDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "web_read",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/web_read.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The full URL to read (must include https://).",
				},
				"instructions": map[string]interface{}{
					"type":        "string",
					"description": "Optional: what specifically to extract from the page, instead of the full text.",
				},
				"offset": map[string]interface{}{
					"type": "integer",
					"description": "Optional, for non-PDF pages only: character offset to resume reading from " +
						"— use the offset given in a previous truncated result to read the next chunk of a long page.",
				},
				"page": map[string]interface{}{
					"type": "integer",
					"description": "Optional, for PDFs only: 1-indexed page number to read " +
						"— use the page number given in a previous result to keep reading a multi-page PDF.",
				},
			},
			"required": []string{"url"},
		},
	},
}

func init() { Register("web_read", handleWebRead) }

func handleWebRead(argsJSON string, ctx *Context) string {
	var args struct {
		URL          string `json:"url"`
		Instructions string `json:"instructions"`
		Offset       int    `json:"offset"`
		Page         int    `json:"page"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "web_read", nil, "error: "+err.Error())
	}
	if args.URL == "" {
		return emitToolError(ctx, "web_read", map[string]interface{}{"url": args.URL}, "error: url is required")
	}
	if ctx.Blocklist.Blocked(args.URL) {
		return emitToolError(ctx, "web_read", map[string]interface{}{"url": args.URL},
			"error: this source is blocked and cannot be read")
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "web_read",
		"args": map[string]interface{}{"url": args.URL, "instructions": args.Instructions, "offset": args.Offset, "page": args.Page},
	})

	title, siteName, imageURL, text, totalPages, err := fetchAndExtract(ctx.Ctx, args.URL, ctx.Blocklist, args.Page)

	// fallbackUsed is purely for logging — it doesn't change control flow,
	// just helps tell "the free path worked" apart from "it took a paid
	// API to get this" when reading logs later.
	fallbackUsed := ""

	// A non-nil err here covers both a dead link and most paywalls (many
	// paywall servers respond 402/403 rather than a real 200) — archive.org
	// is worth trying first since it's free and frequently has a snapshot
	// from before a paywall went up or a page got taken down.
	if err != nil || looksLikePaywall(text) {
		if wbTitle, wbSiteName, wbImageURL, wbText, wbTotalPages, wbErr := fetchFromWayback(ctx.Ctx, args.URL, ctx.Blocklist, args.Page); wbErr == nil {
			title, siteName, imageURL, text, totalPages, err = wbTitle, wbSiteName, wbImageURL, wbText, wbTotalPages, nil
			fallbackUsed = "archive.org"
		} else {
			log.Warn("web_read: wayback fallback failed", "url", args.URL, "err", wbErr)
		}
	}

	// Whatever's left unresolved — still erroring, still reading like a
	// paywall, or empty because the page is JS-rendered and goquery only
	// ever saw the pre-render HTML shell — gets one last shot via Tavily,
	// which actually executes the page's JS on its own infrastructure.
	// This is the only branch archive.org can't substitute for: a
	// snapshot of a JS-rendered SPA is just as empty as the live page was.
	if ctx.Tavily != nil && (err != nil || looksLikePaywall(text) || looksEmpty(text)) {
		if tavilyText, tErr := ctx.Tavily.Extract(ctx.Ctx, args.URL, true); tErr == nil && !looksEmpty(tavilyText) {
			text = tavilyText
			totalPages = 0
			err = nil
			fallbackUsed = "tavily"
			if title == "" {
				title = args.URL
			}
		} else if tErr != nil {
			log.Warn("web_read: tavily fallback failed", "url", args.URL, "err", tErr)
		}
	}

	if err != nil {
		log.Warn("web_read failed", "url", args.URL, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_read", "result": "error: " + err.Error()})
		return "error: " + err.Error()
	}
	if fallbackUsed != "" {
		log.Info("web_read: used fallback", "url", args.URL, "fallback", fallbackUsed)
	}

	// PDFs already came back as a single, page-selected (and page-capped)
	// chunk of text from fetchAndExtract, with its own "page X of Y" note
	// baked in by ExtractPDFPage — offset pagination only applies to
	// everything else, where there's no natural page boundary to select by.
	isPDF := totalPages > 0

	result := text
	if args.Instructions != "" && ctx.LLM != nil {
		filterInput := text
		if !isPDF {
			if args.Offset > 0 && args.Offset < len(filterInput) {
				filterInput = filterInput[args.Offset:]
			}
			// Bounded well above the display window (maxExtractedChars) —
			// the whole point of a filter pass is to let the model ask for
			// something from deep in a page it'll never see unfiltered, so
			// capping it at the display window would defeat that. Still
			// bounded, not unlimited, to keep the filter LLM call's cost
			// and latency predictable on pathological pages.
			if len(filterInput) > maxFilterInputChars {
				filterInput = filterInput[:maxFilterInputChars]
			}
		}
		if filtered, ferr := filterExtractedText(ctx.Ctx, ctx.LLM, filterInput, args.Instructions); ferr == nil {
			result = filtered
		} else {
			log.Warn("web_read: filter pass failed, using full extracted text", "url", args.URL, "err", ferr)
			// On filter failure, silently fall back to the extracted text
			// rather than failing the whole tool call — the free path
			// already succeeded, no reason to throw that away.
			if !isPDF {
				result = windowText(text, args.Offset)
			}
		}
	} else if !isPDF {
		result = windowText(text, args.Offset)
	}

	log.Info("web_read", "url", args.URL, "title", title, "extracted_chars", len(result), "instructions", args.Instructions)
	ctx.AddCitation(Citation{Title: title, URL: args.URL, SiteName: siteName, ImageURL: imageURL})
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_read",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})

	return result
}

var whitespaceRe = regexp.MustCompile(`[ \t]+`)
var blankLinesRe = regexp.MustCompile(`\n{3,}`)

const maxExtractedChars = 12000

// minViableExtractedChars is the length below which extracted text is
// treated as "the page didn't really render" rather than "the page is
// just short" — the common signature of a JS-rendered SPA whose <body>
// is still nearly empty in the raw HTML goquery sees.
const minViableExtractedChars = 200

// fetchAndExtract downloads a page and reduces it to readable text. HTML
// pages: drop script/style/nav/chrome elements, prefer <article>/<main>
// over the full <body>, and collapse whitespace — a plain readability
// heuristic, not a full Readability.js port. PDFs (by Content-Type or
// .pdf extension) get the same whitespace cleanup applied to text pulled
// via ledongthuc/pdf instead of goquery.
//
// This function alone can't do anything about a JS-rendered SPA — that
// needs a real browser executing the page's JS, which is what
// handleWebRead's Tavily fallback is for (see its doc comment). Deliberately
// not running a headless browser locally to keep this light on the potato.
//
// blocklist is checked on every hop of a redirect chain, not just rawURL
// itself — handleWebRead only checks the URL the model asked for before
// calling this, so without this, a non-blocked URL (a shortener, an old
// domain that now 302s elsewhere) that happens to redirect to a blocked
// domain would sail straight through and fetch it anyway. May be nil.
//
// page selects which PDF page to extract (ignored for HTML). totalPages is
// nonzero only for a PDF result — that's how callers tell "paginate this by
// page" (PDF) apart from "paginate this by character offset" (everything
// else, which has no natural page boundary).
func fetchAndExtract(ctx context.Context, rawURL string, blocklist *search.Blocklist, page int) (title, siteName, imageURL, text string, totalPages int, err error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if blocklist.Blocked(req.URL.String()) {
				return fmt.Errorf("redirected to a blocked source (%s)", req.URL.Hostname())
			}
			// Go's own CheckRedirect, replicated: providing a custom func
			// overrides the default 10-redirect cap entirely, not just adds
			// to it — without this, a redirect loop would spin forever
			// instead of failing.
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", "", "", "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Polaris/1.0; +https://github.com/AutumnsGrove/Polaris)")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "", 0, fmt.Errorf("fetching url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", 0, fmt.Errorf("url returned status %d", resp.StatusCode)
	}

	data, err := readLimited(resp.Body)
	if err != nil {
		return "", "", "", "", 0, err
	}

	if isPDF(resp, rawURL) {
		title, text, totalPages, err = ExtractPDFPage(data, page)
		return title, "", "", text, totalPages, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return "", "", "", "", 0, fmt.Errorf("parsing html: %w", err)
	}

	title = strings.TrimSpace(doc.Find("title").First().Text())
	// The publisher-facing name a site sets for its own social-share
	// cards — virtually universal on news/blog platforms, and a much
	// better citation label than the article's own <title> (too long) or
	// its hostname (rarely legible: "hollywoodreporter.com").
	siteName = strings.TrimSpace(doc.Find(`meta[property="og:site_name"]`).First().AttrOr("content", ""))

	// og:image is the same social-share metadata as og:site_name above,
	// just the lead image instead of the publisher name — virtually as
	// universal on real articles. Only kept when it's a genuine absolute
	// URL: the Open Graph spec expects one, but a malformed/relative value
	// would otherwise render as a broken image in the frontend's citation
	// thumbnail rather than just cleanly having no thumbnail at all.
	imageURL = strings.TrimSpace(doc.Find(`meta[property="og:image"]`).First().AttrOr("content", ""))
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		imageURL = ""
	}

	// #wm-ipp-base/#wm-ipp-print are the Wayback Machine's own injected
	// toolbar — only ever present when fetchFromWayback below hands this
	// function an archive.org snapshot URL, but harmless to always strip.
	doc.Find("script, style, nav, footer, header, noscript, iframe, svg, form, aside, #wm-ipp-base, #wm-ipp-print").Remove()

	body := doc.Find("article")
	if body.Length() == 0 {
		body = doc.Find("main")
	}
	if body.Length() == 0 {
		body = doc.Find("body")
	}

	text = collapseWhitespace(body.Text())
	return title, siteName, imageURL, text, 0, nil
}

// maxResponseBytes bounds how much of a fetched page/PDF is ever buffered
// into memory, independent of maxExtractedChars (which only trims the final
// *text*, after goquery/pdf have already parsed the whole thing) — without
// this, a URL that streams a large-but-not-slow response within the 15s
// client timeout gets fully read into memory before any truncation happens.
const maxResponseBytes = 20 << 20 // 20MB

// readLimited reads at most maxResponseBytes+1 from r and errors out if the
// response was still going at that point, rather than silently truncating
// mid-document (which goquery would do with no error if just handed a
// LimitReader directly).
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d byte limit", maxResponseBytes)
	}
	return data, nil
}

// dialContext is fetchAndExtract's http.Transport.DialContext, pointed at
// safeDialContext by default. It's a var (not called directly) so tests can
// point it at a plain, unrestricted dialer — real web_read traffic always
// targets a public host, but tests fetch from httptest servers, which bind
// to loopback on purpose.
var dialContext = safeDialContext

// safeDialContext is fetchAndExtract's http.Transport.DialContext: it
// resolves the host itself and refuses to connect to any private/loopback/
// link-local/unspecified address before dialing. web_read fetches whatever
// URL the model asks for — including URLs surfaced by web_search results,
// which an attacker fully controls if they run the page — and
// search.Blocklist only screens a curated list of unreliable domains, not
// internal network access. Resolving and checking here (rather than just
// rejecting obviously-internal hostnames like "localhost" up front) also
// closes the DNS-rebinding gap: the IP actually dialed is the one checked,
// not one that could change between a pre-check and the real connection.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var dialIP net.IP
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return nil, fmt.Errorf("refusing to connect to internal address %s", ip)
		}
		if dialIP == nil {
			dialIP = ip
		}
	}
	if dialIP == nil {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
}

// isDisallowedIP flags loopback, RFC1918/RFC4193 private, link-local, and
// unspecified addresses — the ranges that reach something other than a
// genuine public web server (a cloud metadata endpoint, an internal admin
// panel, the host Polaris itself runs on).
func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// collapseWhitespace turns raw extracted text (HTML or PDF) into clean
// reading text: collapse runs of spaces/tabs, drop empty lines, and
// collapse 3+ blank lines down to one. Deliberately does no length
// capping — fetchAndExtract's callers need the full text to run paywall/
// empty-page detection and (when instructions are given) the filter LLM
// pass over content beyond what's ever displayed; length limits are
// applied separately by windowText (HTML/PDF-whole-doc) or ExtractPDFPage
// (single PDF page), each with its own pagination story.
func collapseWhitespace(raw string) string {
	cleaned := whitespaceRe.ReplaceAllString(raw, " ")
	lines := strings.Split(cleaned, "\n")
	var kept []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return blankLinesRe.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")
}

// maxFilterInputChars bounds how much already-extracted text is handed to
// the instructions filter pass — much larger than maxExtractedChars (the
// plain-read display cap) since the whole point of that pass is to surface
// something from deep in a page the caller would otherwise never see
// unfiltered. Still bounded, not the full (possibly ~20MB-sourced) text, to
// keep that LLM call's cost and latency predictable on pathological pages.
const maxFilterInputChars = 100000

// windowText slices already-clean text starting at offset and caps it at
// maxExtractedChars, appending a continuation hint with the next offset to
// use when there's more left. Without this, a page bigger than
// maxExtractedChars could only ever be read up to that cap, with no way
// for the model to ask for what comes after it.
func windowText(text string, offset int) string {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	windowed := text[offset:]
	if len(windowed) <= maxExtractedChars {
		return windowed
	}
	next := offset + maxExtractedChars
	return windowed[:maxExtractedChars] + fmt.Sprintf(
		"\n\n... [showing chars %d-%d of %d — call web_read again with the same url and offset: %d to continue reading]",
		offset, next, len(text), next)
}

// isPDF checks Content-Type first (authoritative when present) and falls
// back to a .pdf URL suffix — arXiv abstract pages redirect to PDF URLs
// with no extension at all, so Content-Type is the one that actually
// matters there; the suffix check just catches plain .pdf links whose
// server response is missing or generic (application/octet-stream).
func isPDF(resp *http.Response, rawURL string) bool {
	if strings.Contains(resp.Header.Get("Content-Type"), "application/pdf") {
		return true
	}
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path
	}
	return strings.HasSuffix(strings.ToLower(path), ".pdf")
}

// ExtractPDFText pulls plain text out of a fully-buffered PDF via
// ledongthuc/pdf (pure Go, no cgo, no system dependency — see the doc
// comment on tavily.Client for why that constraint matters here). PDFs
// have no <title> tag; the first short line of extracted text is usually
// the paper's actual title for arXiv-style academic PDFs, so that's used
// as a best-effort title rather than leaving it blank. Exported — also
// used directly by gateway's attachment handling for an uploaded PDF,
// not just PDFs reached via web_read's URL fetch. Whole-document and
// capped at maxExtractedChars — there's no pagination story for an
// attachment the way there is for web_read's URL fetch (see
// ExtractPDFPage), so it just returns as much as fits.
func ExtractPDFText(data []byte) (title, text string, err error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("opening pdf: %w", err)
	}

	contentReader, err := r.GetPlainText()
	if err != nil {
		return "", "", fmt.Errorf("extracting pdf text: %w", err)
	}

	raw, err := io.ReadAll(contentReader)
	if err != nil {
		return "", "", fmt.Errorf("reading pdf text: %w", err)
	}

	text = collapseWhitespace(string(raw))
	if lines := strings.SplitN(text, "\n", 2); len(lines) > 0 && len(lines[0]) < 150 {
		title = lines[0]
	}
	if len(text) > maxExtractedChars {
		text = text[:maxExtractedChars] + "\n\n... [truncated]"
	}
	return title, text, nil
}

// ExtractPDFPage extracts a single page's text from a fully-buffered PDF,
// for web_read's page-based PDF pagination — unlike HTML, a PDF has real
// page boundaries, so "read the next chunk" means "read the next page"
// rather than an arbitrary character offset. page is 1-indexed and clamped
// into range; 0 (unset) defaults to page 1. totalPages lets the caller
// know whether there's anything left to page through.
func ExtractPDFPage(data []byte, page int) (title, text string, totalPages int, err error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", 0, fmt.Errorf("opening pdf: %w", err)
	}

	totalPages = r.NumPage()
	if totalPages == 0 {
		return "", "", 0, fmt.Errorf("pdf has no pages")
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	p := r.Page(page)
	fonts := make(map[string]*pdf.Font)
	for _, name := range p.Fonts() {
		f := p.Font(name)
		fonts[name] = &f
	}
	raw, err := p.GetPlainText(fonts)
	if err != nil {
		return "", "", totalPages, fmt.Errorf("extracting pdf page %d text: %w", page, err)
	}

	text = collapseWhitespace(raw)
	if page == 1 {
		if lines := strings.SplitN(text, "\n", 2); len(lines) > 0 && len(lines[0]) < 150 {
			title = lines[0]
		}
	}
	if len(text) > maxExtractedChars {
		text = text[:maxExtractedChars] + "\n\n... [page truncated]"
	}

	if totalPages > 1 {
		if page < totalPages {
			text += fmt.Sprintf("\n\n[page %d of %d — call web_read again with the same url and page: %d to continue reading]", page, totalPages, page+1)
		} else {
			text += fmt.Sprintf("\n\n[page %d of %d — last page]", page, totalPages)
		}
	}
	return title, text, totalPages, nil
}

// fetchFromWayback checks archive.org's Wayback Machine for the most
// recent snapshot of rawURL and, if one exists, extracts it via the same
// fetchAndExtract path as a live page. Free and often has a copy from
// before a paywall went up or a page was taken down — but it archives
// whatever HTML the page originally served, so it's no help at all for a
// JS-rendered SPA (the snapshot is just as empty as the live page).
// waybackAvailabilityAPI is a var (not a const) so tests can point it at
// an httptest server instead of the real archive.org.
var waybackAvailabilityAPI = "https://archive.org/wayback/available"

func fetchFromWayback(ctx context.Context, rawURL string, blocklist *search.Blocklist, page int) (title, siteName, imageURL, text string, totalPages int, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	availabilityURL := waybackAvailabilityAPI + "?url=" + url.QueryEscape(rawURL)

	req, err := http.NewRequestWithContext(ctx, "GET", availabilityURL, nil)
	if err != nil {
		return "", "", "", "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "", 0, fmt.Errorf("wayback availability check: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", "", 0, fmt.Errorf("reading wayback response: %w", err)
	}

	var avail struct {
		ArchivedSnapshots struct {
			Closest struct {
				Available bool   `json:"available"`
				URL       string `json:"url"`
			} `json:"closest"`
		} `json:"archived_snapshots"`
	}
	if err := json.Unmarshal(body, &avail); err != nil {
		return "", "", "", "", 0, fmt.Errorf("parsing wayback response: %w", err)
	}
	if !avail.ArchivedSnapshots.Closest.Available || avail.ArchivedSnapshots.Closest.URL == "" {
		return "", "", "", "", 0, fmt.Errorf("no archived snapshot available for %s", rawURL)
	}

	return fetchAndExtract(ctx, avail.ArchivedSnapshots.Closest.URL, blocklist, page)
}

// paywallMarkers are phrases that show up in the *raw HTML shell* of
// paywalled pages even before any subscription check runs client-side —
// deliberately narrow (real phrases, not generic words like "subscribe")
// to avoid misfiring on newsletter signup CTAs on otherwise-free pages.
var paywallMarkers = []string{
	"subscribe to continue reading",
	"subscribe to read",
	"this content is for subscribers",
	"this article is for subscribers",
	"create a free account to continue",
	"already a subscriber? sign in",
	"you've reached your free article limit",
	"to continue reading this article",
	"sign in to continue reading",
	"register to continue reading",
}

func looksLikePaywall(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range paywallMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// looksEmpty flags the classic JS-rendered-SPA signature: goquery got a
// 200 and parsed valid HTML, but the meaningful content isn't there
// because it only exists after client-side JS runs.
func looksEmpty(text string) bool {
	return len(strings.TrimSpace(text)) < minViableExtractedChars
}

// filterExtractedText runs a small, cheap LLM pass over already-extracted
// page text to pull out only what the caller asked for — the "double RAG"
// step. Reuses the thread's selected model/client rather than spinning up
// a separate one, since the provider pin (and its prompt-cache pricing)
// is already configured on it.
func filterExtractedText(ctx context.Context, client llm.ChatClient, pageText, instructions string) (string, error) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Tools.WebReadFilterSystem},
		{Role: "user", Content: fmt.Sprintf("Instruction: %s\n\nPage content:\n%s", instructions, pageText)},
	}

	resp, err := client.ChatCompletionStreaming(ctx, messages, func(string) {}, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
