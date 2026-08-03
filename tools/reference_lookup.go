// reference_lookup queries a specific reference source directly —
// Wikipedia's own search+extract API, or arXiv's Atom API for papers —
// instead of going through SearXNG. Useful when the model already knows
// which kind of source it wants (an encyclopedia summary, a paper
// abstract) rather than a general web search, giving a cleaner, more
// citable result for that specific case.
package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"polaris/llm"
)

var referenceLookupDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "reference_lookup",
		Description: "Look up a topic directly in a specific reference source — Wikipedia for an " +
			"encyclopedia summary, or arXiv for academic paper abstracts. Prefer this over web_search " +
			"when you specifically want an encyclopedic overview or a paper's abstract, since it's more " +
			"precise and more directly citable than a general search.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"source": map[string]interface{}{
					"type":        "string",
					"description": "Which reference source to query.",
					"enum":        []string{"wikipedia", "arxiv"},
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The topic (wikipedia) or search terms (arxiv) to look up.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "arXiv only: max papers to return (default 3, max 10). Ignored for wikipedia.",
				},
			},
			"required": []string{"source", "query"},
		},
	},
}

func init() { Register("reference_lookup", handleReferenceLookup) }

func handleReferenceLookup(argsJSON string, ctx *Context) string {
	var args struct {
		Source     string `json:"source"`
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "error: " + err.Error()
	}
	if args.Query == "" {
		return "error: query is required"
	}
	if args.MaxResults <= 0 || args.MaxResults > 10 {
		args.MaxResults = 3
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "reference_lookup",
		"args": map[string]interface{}{"source": args.Source, "query": args.Query},
	})

	var result string
	var err error
	switch args.Source {
	case "wikipedia":
		result, err = lookupWikipedia(ctx, args.Query)
	case "arxiv":
		result, err = lookupArxiv(ctx, args.Query, args.MaxResults)
	default:
		err = fmt.Errorf("unknown source %q (must be \"wikipedia\" or \"arxiv\")", args.Source)
	}

	if err != nil {
		result = "error: " + err.Error()
		log.Warn("reference_lookup failed", "source", args.Source, "query", args.Query, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "reference_lookup", "result": result})
		return result
	}

	log.Info("reference_lookup", "source", args.Source, "query", args.Query)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "reference_lookup",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

// wikipediaAPIBaseURL is a var (not a const) so tests can point it at a
// fake server, same pattern as places.nominatimBaseURL.
var wikipediaAPIBaseURL = "https://en.wikipedia.org/w/api.php"

// wikiSearchExtractResponse is the subset of MediaWiki's query API this
// needs. Combining generator=search with prop=extracts|info in one
// request returns the best-matching page's plain-text intro and
// canonical URL without a separate search-then-fetch round trip.
type wikiSearchExtractResponse struct {
	Query struct {
		Pages map[string]struct {
			Title   string `json:"title"`
			Extract string `json:"extract"`
			FullURL string `json:"fullurl"`
		} `json:"pages"`
	} `json:"query"`
}

func lookupWikipedia(ctx *Context, query string) (string, error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("format", "json")
	q.Set("generator", "search")
	q.Set("gsrsearch", query)
	q.Set("gsrlimit", "1")
	q.Set("prop", "extracts|info")
	q.Set("exintro", "1")
	q.Set("explaintext", "1")
	q.Set("inprop", "url")

	body, err := referenceHTTPGet(ctx.Ctx, wikipediaAPIBaseURL+"?"+q.Encode())
	if err != nil {
		return "", fmt.Errorf("fetching wikipedia: %w", err)
	}

	var resp wikiSearchExtractResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing wikipedia response: %w", err)
	}
	if len(resp.Query.Pages) == 0 {
		return "", fmt.Errorf("no wikipedia article found for %q", query)
	}

	// A generator=search result set always has exactly one page for
	// gsrlimit=1; map iteration order doesn't matter with one entry.
	for _, page := range resp.Query.Pages {
		if page.Extract == "" {
			return "", fmt.Errorf("no wikipedia article found for %q", query)
		}
		ctx.AddCitation(Citation{Title: page.Title, URL: page.FullURL})
		return fmt.Sprintf("%s (Wikipedia)\n\n%s", page.Title, page.Extract), nil
	}
	return "", fmt.Errorf("no wikipedia article found for %q", query)
}

var arxivAPIBaseURL = "https://export.arxiv.org/api/query"

// arxivFeed is the subset of arXiv's Atom API response this needs.
// encoding/xml matches elements by local name regardless of the feed's
// default Atom namespace, so no xmlns handling is needed here.
type arxivFeed struct {
	Entries []struct {
		Title   string `xml:"title"`
		Summary string `xml:"summary"`
		ID      string `xml:"id"` // arXiv abstract page URL
	} `xml:"entry"`
}

func lookupArxiv(ctx *Context, query string, maxResults int) (string, error) {
	q := url.Values{}
	q.Set("search_query", "all:"+query)
	q.Set("start", "0")
	q.Set("max_results", fmt.Sprintf("%d", maxResults))

	body, err := referenceHTTPGet(ctx.Ctx, arxivAPIBaseURL+"?"+q.Encode())
	if err != nil {
		return "", fmt.Errorf("fetching arxiv: %w", err)
	}

	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return "", fmt.Errorf("parsing arxiv response: %w", err)
	}
	if len(feed.Entries) == 0 {
		return "", fmt.Errorf("no arxiv papers found for %q", query)
	}

	var sb strings.Builder
	for i, e := range feed.Entries {
		title := collapseWhitespace(e.Title)
		summary := collapseWhitespace(e.Summary)
		id := strings.TrimSpace(e.ID)
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, title, id, summary)
		ctx.AddCitation(Citation{Title: title, URL: id})
	}
	return sb.String(), nil
}

// referenceHTTPGet is a small shared GET helper for the two lookups
// above, mirroring youtube_transcript.go's httpGetBody.
func referenceHTTPGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")
	req.Header.Set("Accept", "application/json, application/atom+xml")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
