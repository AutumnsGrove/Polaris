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
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/ledongthuc/pdf"

	"polaris/llm"
)

var webReadDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "web_read",
		Description: "Fetch a URL and extract its clean text content. Use when the user shares a link, or " +
			"a web_search result needs deeper investigation. Optionally pass 'instructions' to extract only " +
			"specific information (e.g. 'just the prices', 'just the release date') instead of the full page.",
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
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "error: " + err.Error()
	}
	if args.URL == "" {
		return "error: url is required"
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "web_read",
		"args": map[string]interface{}{"url": args.URL, "instructions": args.Instructions},
	})

	title, text, err := fetchAndExtract(ctx.Ctx, args.URL)

	// fallbackUsed is purely for logging — it doesn't change control flow,
	// just helps tell "the free path worked" apart from "it took a paid
	// API to get this" when reading logs later.
	fallbackUsed := ""

	// A non-nil err here covers both a dead link and most paywalls (many
	// paywall servers respond 402/403 rather than a real 200) — archive.org
	// is worth trying first since it's free and frequently has a snapshot
	// from before a paywall went up or a page got taken down.
	if err != nil || looksLikePaywall(text) {
		if wbTitle, wbText, wbErr := fetchFromWayback(ctx.Ctx, args.URL); wbErr == nil {
			title, text, err = wbTitle, wbText, nil
			fallbackUsed = "archive.org"
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

	result := text
	if args.Instructions != "" && ctx.LLM != nil {
		if filtered, ferr := filterExtractedText(ctx.Ctx, ctx.LLM, text, args.Instructions); ferr == nil {
			result = filtered
		} else {
			log.Warn("web_read: filter pass failed, using full extracted text", "url", args.URL, "err", ferr)
		}
		// On filter failure, silently fall back to the full extracted
		// text rather than failing the whole tool call — the free path
		// already succeeded, no reason to throw that away.
	}

	log.Info("web_read", "url", args.URL, "title", title, "extracted_chars", len(text), "instructions", args.Instructions)
	ctx.AddCitation(Citation{Title: title, URL: args.URL})
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
func fetchAndExtract(ctx context.Context, rawURL string) (title, text string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Polaris/1.0; +https://github.com/AutumnsGrove/Polaris)")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetching url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("url returned status %d", resp.StatusCode)
	}

	if isPDF(resp, rawURL) {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", "", fmt.Errorf("reading pdf body: %w", err)
		}
		return ExtractPDFText(data)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("parsing html: %w", err)
	}

	title = strings.TrimSpace(doc.Find("title").First().Text())

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
	return title, text, nil
}

// collapseWhitespace turns raw extracted text (HTML or PDF) into clean,
// truncated reading text: collapse runs of spaces/tabs, drop empty
// lines, collapse 3+ blank lines down to one, and cap length.
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
	text := blankLinesRe.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")

	if len(text) > maxExtractedChars {
		text = text[:maxExtractedChars] + "\n\n... [truncated]"
	}
	return text
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
// not just PDFs reached via web_read's URL fetch.
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
	return title, text, nil
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

func fetchFromWayback(ctx context.Context, rawURL string) (title, text string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	availabilityURL := waybackAvailabilityAPI + "?url=" + url.QueryEscape(rawURL)

	req, err := http.NewRequestWithContext(ctx, "GET", availabilityURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("wayback availability check: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("reading wayback response: %w", err)
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
		return "", "", fmt.Errorf("parsing wayback response: %w", err)
	}
	if !avail.ArchivedSnapshots.Closest.Available || avail.ArchivedSnapshots.Closest.URL == "" {
		return "", "", fmt.Errorf("no archived snapshot available for %s", rawURL)
	}

	return fetchAndExtract(ctx, avail.ArchivedSnapshots.Closest.URL)
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
		{
			Role: "system",
			Content: "You extract specific information from web page text. Given the page content and an " +
				"instruction, return ONLY what was asked for — no commentary, no restating the instruction. " +
				"If the requested information isn't present, say so in one short sentence.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Instruction: %s\n\nPage content:\n%s", instructions, pageText),
		},
	}

	resp, err := client.ChatCompletionStreaming(ctx, messages, func(string) {}, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
