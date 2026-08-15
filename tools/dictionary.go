// dictionary looks up a word's definition, part of speech, and (when
// available) an example sentence — precise and directly citable, unlike
// hoping a general web_search surfaces a dictionary page or trusting the
// model's own training data for something a stable, sourced reference is
// a better authority on. Backed by two independent Wiktionary-derived
// JSON APIs, both free and keyless (the same "no API key required" bar
// weather.go/reference_lookup.go hold to): dictionaryapi.dev as primary,
// freedictionaryapi.com as a fallback if the primary errors or has no
// entry — same free-path-then-fallback shape as web_read.go's fetch
// chain. Deliberately two independently-maintained sources, not one with
// a retry: dictionaryapi.dev is a single-maintainer project that has
// publicly flagged funding/capacity strain, so a second source is real
// insurance, not just belt-and-suspenders against a transient blip.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"polaris/llm"
)

var dictionaryDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "dictionary",
		// Description is set in init() from tools/descriptions/dictionary.yaml.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"word": map[string]interface{}{
					"type":        "string",
					"description": "The word (or short set phrase) to define.",
				},
			},
			"required": []string{"word"},
		},
	},
}

func init() {
	Register("dictionary", handleDictionary)
	dictionaryDef.Function.Description = catalogDescription("dictionary")
}

// dictionaryAPIDevBaseURL/freeDictionaryAPIBaseURL are vars (not consts)
// so tests can point them at a fake server, same pattern as
// openMeteoBaseURL/wikipediaAPIBaseURL.
var dictionaryAPIDevBaseURL = "https://api.dictionaryapi.dev/api/v2/entries/en"
var freeDictionaryAPIBaseURL = "https://freedictionaryapi.com/api/v1/entries/en"

// maxDictionaryDefinitionsPerSense caps how many definitions get listed
// per part of speech — a common word like "run" can have dozens; the
// first handful cover what a lookup actually needs, same rationale as
// reference_lookup's arXiv max_results cap.
const maxDictionaryDefinitionsPerSense = 3

func handleDictionary(argsJSON string, ctx *Context) string {
	var args struct {
		Word string `json:"word"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "dictionary", nil, "error: "+err.Error())
	}
	word := strings.TrimSpace(args.Word)
	if word == "" {
		return emitToolError(ctx, "dictionary", map[string]interface{}{"word": args.Word}, "error: word is required")
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "dictionary",
		"args": map[string]interface{}{"word": word},
	})

	result, err := lookupDictionaryAPIDev(ctx, word)
	if err != nil {
		log.Warn("dictionary: primary source failed, trying fallback", "word", word, "err", err)
		result, err = lookupFreeDictionaryAPI(ctx, word)
	}
	if err != nil {
		result = "error: " + err.Error()
		log.Warn("dictionary failed", "word", word, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "dictionary", "result": result})
		return result
	}

	log.Info("dictionary", "word", word)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "dictionary",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

// --- dictionaryapi.dev (primary) ---

// dictionaryAPIDevEntry is the subset of dictionaryapi.dev's response
// this needs — a definition's own "example" field is present in the API
// but sparsely populated in practice (many real words return definitions
// with no example at all), so formatDictionaryAPIDev must treat it as
// optional, not assume every definition has one.
type dictionaryAPIDevEntry struct {
	Word     string `json:"word"`
	Phonetic string `json:"phonetic"`
	Meanings []struct {
		PartOfSpeech string `json:"partOfSpeech"`
		Definitions  []struct {
			Definition string `json:"definition"`
			Example    string `json:"example"`
		} `json:"definitions"`
	} `json:"meanings"`
	// SourceURLs points at the canonical Wiktionary page — used as the
	// citation URL. Falls back to a constructed Wiktionary URL if somehow
	// empty (defensive; the live API always populates this).
	SourceURLs []string `json:"sourceUrls"`
}

func lookupDictionaryAPIDev(ctx *Context, word string) (string, error) {
	body, status, err := dictionaryHTTPGet(ctx.Ctx, dictionaryAPIDevBaseURL+"/"+url.PathEscape(word))
	if err != nil {
		return "", fmt.Errorf("fetching dictionaryapi.dev: %w", err)
	}
	// A word with no entry is a normal, textual 404 here — not a
	// transport failure — so it's handled explicitly rather than falling
	// into the generic non-200 branch below.
	if status == http.StatusNotFound {
		return "", fmt.Errorf("no dictionary entry found for %q", word)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("dictionaryapi.dev status %d", status)
	}

	var entries []dictionaryAPIDevEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return "", fmt.Errorf("parsing dictionaryapi.dev response: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no dictionary entry found for %q", word)
	}

	return formatDictionaryAPIDev(ctx, entries[0], word), nil
}

func formatDictionaryAPIDev(ctx *Context, e dictionaryAPIDevEntry, word string) string {
	sourceURL := fmt.Sprintf("https://en.wiktionary.org/wiki/%s", url.PathEscape(word))
	if len(e.SourceURLs) > 0 {
		sourceURL = e.SourceURLs[0]
	}
	ctx.AddCitation(Citation{Title: "Wiktionary: " + e.Word, URL: sourceURL})

	var sb strings.Builder
	sb.WriteString(e.Word)
	if e.Phonetic != "" {
		fmt.Fprintf(&sb, " %s", e.Phonetic)
	}
	sb.WriteString("\n\n")
	for _, m := range e.Meanings {
		fmt.Fprintf(&sb, "%s:\n", m.PartOfSpeech)
		for i, d := range m.Definitions {
			if i >= maxDictionaryDefinitionsPerSense {
				break
			}
			fmt.Fprintf(&sb, "%d. %s\n", i+1, d.Definition)
			if d.Example != "" {
				fmt.Fprintf(&sb, "   e.g. \"%s\"\n", d.Example)
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// --- freedictionaryapi.com (fallback) ---

// freeDictionaryAPIResponse is the subset of freedictionaryapi.com's
// response this needs. Unlike dictionaryapi.dev, a not-found word here is
// still HTTP 200 with an empty Entries slice — see lookupFreeDictionaryAPI.
type freeDictionaryAPIResponse struct {
	Word    string `json:"word"`
	Entries []struct {
		PartOfSpeech   string `json:"partOfSpeech"`
		Pronunciations []struct {
			Text string `json:"text"`
		} `json:"pronunciations"`
		Senses []struct {
			Definition string   `json:"definition"`
			Examples   []string `json:"examples"`
		} `json:"senses"`
	} `json:"entries"`
	Source struct {
		URL string `json:"url"`
	} `json:"source"`
}

func lookupFreeDictionaryAPI(ctx *Context, word string) (string, error) {
	body, status, err := dictionaryHTTPGet(ctx.Ctx, freeDictionaryAPIBaseURL+"/"+url.PathEscape(word))
	if err != nil {
		return "", fmt.Errorf("fetching freedictionaryapi.com: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("freedictionaryapi.com status %d", status)
	}

	var resp freeDictionaryAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing freedictionaryapi.com response: %w", err)
	}
	if len(resp.Entries) == 0 {
		return "", fmt.Errorf("no dictionary entry found for %q", word)
	}

	return formatFreeDictionaryAPI(ctx, resp, word), nil
}

func formatFreeDictionaryAPI(ctx *Context, resp freeDictionaryAPIResponse, word string) string {
	sourceURL := resp.Source.URL
	if sourceURL == "" {
		sourceURL = fmt.Sprintf("https://en.wiktionary.org/wiki/%s", url.PathEscape(word))
	}
	ctx.AddCitation(Citation{Title: "Wiktionary: " + resp.Word, URL: sourceURL})

	var sb strings.Builder
	sb.WriteString(resp.Word)
	if len(resp.Entries[0].Pronunciations) > 0 {
		fmt.Fprintf(&sb, " /%s/", strings.Trim(resp.Entries[0].Pronunciations[0].Text, "/"))
	}
	sb.WriteString("\n\n")
	for _, e := range resp.Entries {
		fmt.Fprintf(&sb, "%s:\n", e.PartOfSpeech)
		for i, s := range e.Senses {
			if i >= maxDictionaryDefinitionsPerSense {
				break
			}
			fmt.Fprintf(&sb, "%d. %s\n", i+1, s.Definition)
			if len(s.Examples) > 0 {
				fmt.Fprintf(&sb, "   e.g. \"%s\"\n", s.Examples[0])
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// dictionaryHTTPGet is a small shared GET helper mirroring
// reference_lookup.go's referenceHTTPGet, but also returns the status
// code rather than erroring on non-200 — dictionaryapi.dev's 404 is a
// normal "no entry for this word" response, not a transport failure, and
// callers need to tell those two cases apart.
func dictionaryHTTPGet(ctx context.Context, rawURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}
