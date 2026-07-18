// web_read implements the "double RAG" behavior: fetch a URL, strip it
// to clean text for free (goquery, no paid extraction API), and — only
// when the model gives a specific instruction like "just the prices" —
// run one extra small LLM pass over that text to pull out just what was
// asked for. Plain reads never pay the second LLM call.
package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

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

	title, text, err := fetchAndExtract(args.URL)
	if err != nil {
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_read", "result": "error: " + err.Error()})
		return "error: " + err.Error()
	}

	result := text
	if args.Instructions != "" && ctx.LLM != nil {
		if filtered, ferr := filterExtractedText(ctx.LLM, text, args.Instructions); ferr == nil {
			result = filtered
		}
		// On filter failure, silently fall back to the full extracted
		// text rather than failing the whole tool call — the free path
		// already succeeded, no reason to throw that away.
	}

	ctx.AddCitation(Citation{Title: title, URL: args.URL})
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "web_read",
		"result":    result,
		"citations": ctx.Citations,
	})

	return result
}

var whitespaceRe = regexp.MustCompile(`[ \t]+`)
var blankLinesRe = regexp.MustCompile(`\n{3,}`)

const maxExtractedChars = 12000

// fetchAndExtract downloads a page and reduces it to readable text: drop
// script/style/nav/chrome elements, prefer <article>/<main> over the full
// <body>, and collapse whitespace. This is a plain readability heuristic,
// not a full Readability.js port — good enough for article/listing pages,
// weaker on heavily JS-rendered SPAs (those need a headless browser, which
// we're deliberately not running to keep this light on the potato).
func fetchAndExtract(rawURL string) (title, text string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", rawURL, nil)
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

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("parsing html: %w", err)
	}

	title = strings.TrimSpace(doc.Find("title").First().Text())

	doc.Find("script, style, nav, footer, header, noscript, iframe, svg, form, aside").Remove()

	body := doc.Find("article")
	if body.Length() == 0 {
		body = doc.Find("main")
	}
	if body.Length() == 0 {
		body = doc.Find("body")
	}

	raw := body.Text()
	cleaned := whitespaceRe.ReplaceAllString(raw, " ")
	lines := strings.Split(cleaned, "\n")
	var kept []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	text = blankLinesRe.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")

	if len(text) > maxExtractedChars {
		text = text[:maxExtractedChars] + "\n\n... [truncated]"
	}

	return title, text, nil
}

// filterExtractedText runs a small, cheap LLM pass over already-extracted
// page text to pull out only what the caller asked for — the "double RAG"
// step. Reuses the thread's selected model/client rather than spinning up
// a separate one, since the provider pin (and its prompt-cache pricing)
// is already configured on it.
func filterExtractedText(client *llm.Client, pageText, instructions string) (string, error) {
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

	resp, err := client.ChatCompletionStreaming(messages, func(string) {})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
