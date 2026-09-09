// search_chats lets the model search the user's own prior conversation
// history and hand back a real, clickable pointer into it — implements
// docs/plans/search-chats.md (issue #26). One tool, two actions, same
// "action enum instead of N tools" shape as memory (tools/memory.go):
// search (FTS5 keyword matching, or — with query omitted — plain recency
// listing) and read (a past thread's full content, filtered through the
// same "double RAG" LLM pass web_read's instructions param already
// established, or raw as a fallback).
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
)

var searchChatsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "search_chats",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/search_chats.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{"search", "read"},
					"description": "search: find past threads matching a query. read: fetch one past " +
						"thread's full content by the thread_id a search result returned.",
				},
				"query": map[string]interface{}{
					"type": "string",
					"description": "Optional for search. Free-text description of what you're looking for. " +
						"Omit entirely to list your most recent threads instead, newest first — use this " +
						"for \"what have I been asking about\" style questions instead of guessing keywords.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Optional for search: max results to return (default 5, max 15).",
				},
				"cursor": map[string]interface{}{
					"type": "string",
					"description": "Optional for search: pass back the cursor a prior search call returned " +
						"to fetch the next page — only meaningful for recency mode (no query); a keyword " +
						"search's results aren't paged.",
				},
				"thread_id": map[string]interface{}{
					"type":        "string",
					"description": "Required for read: the thread_id from a prior search result.",
				},
				"instructions": map[string]interface{}{
					"type": "string",
					"description": "Optional for read: what specifically to pull out of the thread " +
						"(\"what did we decide about X\", \"the exact wording of the plan we settled on\"), " +
						"instead of the full transcript. Strongly preferred over omitting this — see the " +
						"read action's own notes below.",
				},
			},
			"required": []string{"action"},
		},
	},
}

func init() { Register("search_chats", handleSearchChats) }

// searchChatsDefaultLimit/searchChatsMaxLimit bound keyword search's
// model-suppliable limit — recency mode ignores both in favor of
// store.RecencyPageSize's own fixed page size (see docs/plans/
// search-chats.md's "Recency mode" section for why the two modes'
// pagination stories are deliberately different).
const searchChatsDefaultLimit = 5
const searchChatsMaxLimit = 15

// rawReadMaxChars bounds search_chats' read action's raw (no instructions)
// mode — separate from web_read's own maxExtractedChars (12000): v1
// deliberately doesn't paginate raw thread reads (see the plan's "Raw
// mode" section), so this cap exists purely to keep one unfiltered call
// from dumping an unbounded transcript into the turn, not as the first
// page of a multi-page read.
const rawReadMaxChars = 8000

func handleSearchChats(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Action       string `json:"action"`
		Query        string `json:"query"`
		Limit        int    `json:"limit"`
		Cursor       string `json:"cursor"`
		ThreadID     string `json:"thread_id"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "search_chats", nil, "error: "+err.Error(), callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "search_chats",
		"args":    map[string]interface{}{"action": args.Action, "query": args.Query, "thread_id": args.ThreadID, "cursor": args.Cursor},
		"call_id": callID,
	})

	var result string
	switch args.Action {
	case "search":
		if args.Query == "" {
			result = handleSearchChatsRecency(ctx, args.Cursor)
		} else {
			result = handleSearchChatsKeyword(ctx, args.Query, args.Limit)
		}
	case "read":
		result = handleSearchChatsRead(ctx, args.ThreadID, args.Instructions)
	default:
		result = "error: unknown action " + args.Action + " — must be one of search, read"
	}

	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "search_chats",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
		"call_id":   callID,
	})
	return result
}

// stxEtxReplacer strips store.SearchMessages' \x02/\x03 (STX/ETX) hit
// markers from a snippet — those exist so the *frontend's* sidebar search
// box can render real highlight spans without an {@html} injection surface
// (see MessageSearchResult's doc comment); a plain-text tool result read by
// the model has no such rendering step, so the markers would just show up
// as stray control characters in what the model sees.
var stxEtxReplacer = strings.NewReplacer("\x02", "", "\x03", "")

func handleSearchChatsKeyword(ctx *Context, query string, limit int) string {
	if ctx.SearchThreads == nil {
		return "error: search_chats is not available in this context"
	}
	if limit <= 0 {
		limit = searchChatsDefaultLimit
	}
	if limit > searchChatsMaxLimit {
		limit = searchChatsMaxLimit
	}
	results, err := ctx.SearchThreads(query, limit)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(results) == 0 {
		return fmt.Sprintf("no past threads matched %q", query)
	}

	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		title := r.ThreadTitle
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&sb, "%d. %q (thread %s, %s)\n   [%s](/t/%s)\n   \"%s\"",
			i+1, title, r.ThreadID, r.CreatedAt.Format("2006-01-02"), title, r.ThreadID, stxEtxReplacer.Replace(r.Snippet))
		ctx.AddCitation(Citation{Title: title, URL: "/t/" + r.ThreadID})
	}
	return sb.String()
}

func handleSearchChatsRecency(ctx *Context, cursor string) string {
	if ctx.ListRecentThreads == nil {
		return "error: search_chats is not available in this context"
	}
	threads, nextCursor, err := ctx.ListRecentThreads(cursor)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(threads) == 0 {
		if cursor != "" {
			return "no more threads"
		}
		return "no past threads yet"
	}

	var sb strings.Builder
	for i, t := range threads {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		title := t.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&sb, "%d. %q (thread %s, %s)\n   [%s](/t/%s)", i+1, title, t.ID, t.UpdatedAt.Format("2006-01-02"), title, t.ID)
		if t.Preview != "" {
			fmt.Fprintf(&sb, "\n   \"%s\"", t.Preview)
		}
		ctx.AddCitation(Citation{Title: title, URL: "/t/" + t.ID})
	}
	if nextCursor != "" {
		fmt.Fprintf(&sb, "\n\n%d more results — pass cursor=%q to see the next page", store.RecencyPageSize, nextCursor)
	}
	return sb.String()
}

func handleSearchChatsRead(ctx *Context, threadID, instructions string) string {
	if threadID == "" {
		return "error: thread_id is required for read"
	}
	if ctx.ReadThread == nil {
		return "error: search_chats is not available in this context"
	}
	thread, err := ctx.ReadThread(threadID)
	if err != nil {
		return "error: no thread found with id " + threadID
	}
	if thread.Content == "" {
		return "that thread has no messages"
	}

	// !ctx.QuickMode: same trade-off web_read's own filter gate makes (see
	// its use of this field) — Atlas's Quick Answer mode trades a filter
	// pass's precision for one fewer sequential LLM round-trip.
	if instructions != "" && ctx.LLM != nil && !ctx.QuickMode {
		filterInput := thread.Content
		if len(filterInput) > maxFilterInputChars {
			filterInput = filterInput[:maxFilterInputChars]
		}
		if filtered, filterCost, ferr := filterExtractedText(ctx.Ctx, ctx.LLM, prompts.Get().Tools.ThreadReadFilterSystem, filterInput, instructions); ferr == nil {
			ctx.AddCost(filterCost)
			return filtered
		} else {
			log.Warn("search_chats: filter pass failed, using raw transcript", "thread_id", threadID, "err", ferr)
			// Falls through to raw mode below, same non-fatal-degradation
			// choice web_read's own filterExtractedText failure path makes.
		}
	}

	return truncateRawRead(thread.Content)
}

func truncateRawRead(text string) string {
	if len(text) <= rawReadMaxChars {
		return text
	}
	return text[:rawReadMaxChars] + fmt.Sprintf(
		"\n\n... [truncated at %d characters — call search_chats again with action=read and instructions "+
			"describing what you're looking for, instead of a raw dump, to get the rest]", rawReadMaxChars)
}
