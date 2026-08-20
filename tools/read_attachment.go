// read_attachment lets the model page through or search a PDF the user
// attached to this turn, beyond the short preview resolveAttachment
// (gateway/attachments.go) already folded into the message up front. Same
// "double RAG" shape as web_read: a free path (page-based pagination, or a
// literal-substring "grep in disguise" search across every page to find
// which page to read) plus an optional LLM filter pass over a single
// page's text once the model knows which page it wants. Only ever offered
// when ctx.AttachmentData is non-empty for this turn — see catalog.go's
// "attachment" Requires case — since the raw bytes only survive for the
// one turn the attachment was uploaded on (never persisted to disk or the
// thread's history).
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"

	"polaris/llm"
)

var readAttachmentDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "read_attachment",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/read_attachment.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"page": map[string]interface{}{
					"type":        "integer",
					"description": "1-indexed page number to read. Defaults to page 1. Ignored when 'query' is given.",
				},
				"query": map[string]interface{}{
					"type": "string",
					"description": "Search the whole attachment for this text and return which pages it appears on, with a " +
						"snippet of surrounding context — use this to find the right page before reading it in full, instead " +
						"of paging through blindly. Takes priority over 'page' when both are given.",
				},
				"instructions": map[string]interface{}{
					"type": "string",
					"description": "Optional: what specifically to extract from the read page, instead of the full page text. " +
						"Only applies when reading a page (has no effect when 'query' is given).",
				},
			},
		},
	},
}

func init() { Register("read_attachment", handleReadAttachment) }

func handleReadAttachment(argsJSON string, ctx *Context) string {
	var args struct {
		Page         int    `json:"page"`
		Query        string `json:"query"`
		Instructions string `json:"instructions"`
	}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return emitToolError(ctx, "read_attachment", nil, "error: "+err.Error())
		}
	}
	if len(ctx.AttachmentData) == 0 {
		return emitToolError(ctx, "read_attachment", nil, "error: no attachment is available to read this turn")
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "read_attachment",
		"args": map[string]interface{}{
			"page": args.Page, "query": args.Query, "instructions": args.Instructions, "filename": ctx.AttachmentFilename,
		},
	})

	var result string
	if args.Query != "" {
		matches, totalPages, err := searchPDFPages(ctx.AttachmentData, args.Query)
		if err != nil {
			result = "error: " + err.Error()
		} else {
			result = formatPDFSearchResult(args.Query, matches, totalPages)
		}
		ctx.Emit("tool_result", map[string]interface{}{"tool": "read_attachment", "result": result})
		return result
	}

	_, text, totalPages, err := pdfPageText(ctx.AttachmentData, args.Page)
	if err != nil {
		result = "error: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "read_attachment", "result": result})
		return result
	}

	result = text
	if args.Instructions != "" && ctx.LLM != nil {
		if filtered, ferr := filterExtractedText(ctx.Ctx, ctx.LLM, text, args.Instructions); ferr == nil {
			result = filtered
		} else {
			log.Warn("read_attachment: filter pass failed, using full page text", "err", ferr)
		}
	}

	page := args.Page
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	if totalPages > 1 {
		if page < totalPages {
			result += fmt.Sprintf("\n\n[page %d of %d — call read_attachment again with page: %d to continue reading, "+
				"or use 'query' to search the whole document]", page, totalPages, page+1)
		} else {
			result += fmt.Sprintf("\n\n[page %d of %d — last page]", page, totalPages)
		}
	}

	ctx.Emit("tool_result", map[string]interface{}{"tool": "read_attachment", "result": result})
	return result
}

// maxPDFSearchMatches bounds how many pages searchPDFPages reports —
// enough to actually be useful (a term appearing on 3-4 pages is a normal
// result) without the tool result ballooning on a term that appears on
// nearly every page of a long document, where a page-range narrower query
// is what the model actually needs to ask for instead.
const maxPDFSearchMatches = 15

// pdfSearchSnippetContext is how many characters of surrounding text ride
// along on each side of a match — enough to tell the model whether a hit
// is actually relevant before spending a whole extra tool call reading the
// page in full.
const pdfSearchSnippetContext = 100

type pdfSearchMatch struct {
	Page    int
	Snippet string
}

// searchPDFPages does a literal, case-insensitive substring search across
// every page of a fully-buffered PDF — the "grep calls in disguise" half
// of read_attachment, deliberately not an LLM pass: cheap, deterministic,
// and good enough for "which page mentions the termination clause" without
// spending a model call per page. Stops early once maxPDFSearchMatches is
// reached rather than scanning the rest of a very long document for no
// added benefit. A page whose text fails to extract is skipped rather than
// failing the whole search — one malformed page shouldn't hide matches on
// every other page.
func searchPDFPages(data []byte, query string) (matches []pdfSearchMatch, totalPages int, err error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, fmt.Errorf("opening pdf: %w", err)
	}
	totalPages = r.NumPage()
	lowerQuery := strings.ToLower(query)

	for page := 1; page <= totalPages && len(matches) < maxPDFSearchMatches; page++ {
		text, perr := pdfPageRawText(r.Page(page))
		if perr != nil {
			continue
		}
		lowerText := strings.ToLower(text)
		idx := strings.Index(lowerText, lowerQuery)
		if idx == -1 {
			continue
		}
		start := idx - pdfSearchSnippetContext
		if start < 0 {
			start = 0
		}
		end := idx + len(query) + pdfSearchSnippetContext
		if end > len(text) {
			end = len(text)
		}
		matches = append(matches, pdfSearchMatch{Page: page, Snippet: text[start:end]})
	}
	return matches, totalPages, nil
}

func formatPDFSearchResult(query string, matches []pdfSearchMatch, totalPages int) string {
	if len(matches) == 0 {
		return fmt.Sprintf("no matches for %q across all %d pages", query, totalPages)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d match(es) for %q:\n", len(matches), query)
	for _, m := range matches {
		fmt.Fprintf(&sb, "\n[page %d] ...%s...", m.Page, m.Snippet)
	}
	if len(matches) == maxPDFSearchMatches {
		fmt.Fprintf(&sb, "\n\n(showing the first %d matches — narrow your query for more precise results)", maxPDFSearchMatches)
	}
	sb.WriteString("\n\nCall read_attachment with a page number to read a match in full.")
	return sb.String()
}
