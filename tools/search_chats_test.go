package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/store"
)

func TestHandleSearchChats_KeywordSearchFormatsResultsAndAddsCitation(t *testing.T) {
	ctx := newTestContext()
	ctx.SearchThreads = func(query string, limit int) ([]store.MessageSearchResult, error) {
		if query != "graphics card" {
			t.Errorf("query = %q, want %q", query, "graphics card")
		}
		return []store.MessageSearchResult{
			{ThreadID: "a1b2c3", ThreadTitle: "Should I get a 4070 or wait?", Role: "user",
				Snippet: "comparing the RTX \x024070\x03 Super", CreatedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)},
		}, nil
	}

	result := handleSearchChats(`{"action":"search","query":"graphics card"}`, ctx, "test-call")

	if !strings.Contains(result, "Should I get a 4070 or wait?") {
		t.Errorf("result = %q, want it to include the thread title", result)
	}
	if !strings.Contains(result, "/t/a1b2c3") {
		t.Errorf("result = %q, want it to include the thread link", result)
	}
	if strings.ContainsAny(result, "\x02\x03") {
		t.Errorf("result = %q, want the STX/ETX highlight markers stripped", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "/t/a1b2c3" {
		t.Errorf("Citations = %+v, want one citation pointing at /t/a1b2c3", ctx.Citations)
	}
}

func TestHandleSearchChats_KeywordSearchNoResults(t *testing.T) {
	ctx := newTestContext()
	ctx.SearchThreads = func(query string, limit int) ([]store.MessageSearchResult, error) {
		return nil, nil
	}
	result := handleSearchChats(`{"action":"search","query":"nothing matches this"}`, ctx, "test-call")
	if !strings.Contains(result, "no past threads matched") {
		t.Errorf("result = %q, want a no-match message", result)
	}
	if len(ctx.Citations) != 0 {
		t.Errorf("Citations = %+v, want none for a no-match search", ctx.Citations)
	}
}

func TestHandleSearchChats_RecencyModeNoQueryListsRecentThreads(t *testing.T) {
	ctx := newTestContext()
	var gotCursor string
	ctx.ListRecentThreads = func(cursor string) ([]store.ThreadSummary, string, error) {
		gotCursor = cursor
		return []store.ThreadSummary{
			{ID: "f9e8d7", Title: "refinancing the car loan", UpdatedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Preview: "should I refinance now or wait"},
		}, "next-page-token", nil
	}

	result := handleSearchChats(`{"action":"search"}`, ctx, "test-call")

	if gotCursor != "" {
		t.Errorf("cursor passed to ListRecentThreads = %q, want empty for a first page", gotCursor)
	}
	if !strings.Contains(result, "refinancing the car loan") {
		t.Errorf("result = %q, want the thread title", result)
	}
	if !strings.Contains(result, "/t/f9e8d7") {
		t.Errorf("result = %q, want the thread link", result)
	}
	if !strings.Contains(result, "next-page-token") {
		t.Errorf("result = %q, want the next-page cursor surfaced so the model can page further", result)
	}
	if len(ctx.Citations) != 1 {
		t.Errorf("Citations = %+v, want one citation for the one thread listed", ctx.Citations)
	}
}

func TestHandleSearchChats_RecencyModePassesCursorThrough(t *testing.T) {
	ctx := newTestContext()
	var gotCursor string
	ctx.ListRecentThreads = func(cursor string) ([]store.ThreadSummary, string, error) {
		gotCursor = cursor
		return nil, "", nil
	}
	handleSearchChats(`{"action":"search","cursor":"abc123"}`, ctx, "test-call")
	if gotCursor != "abc123" {
		t.Errorf("cursor passed to ListRecentThreads = %q, want %q", gotCursor, "abc123")
	}
}

func TestHandleSearchChats_ReadWithoutInstructionsReturnsRawTranscript(t *testing.T) {
	ctx := newTestContext()
	ctx.ReadThread = func(threadID string) (*store.ThreadReadResult, error) {
		return &store.ThreadReadResult{ThreadID: threadID, Title: "old thread", Content: "User: hello\n\nAssistant: hi there"}, nil
	}

	result := handleSearchChats(`{"action":"read","thread_id":"abc"}`, ctx, "test-call")
	if result != "User: hello\n\nAssistant: hi there" {
		t.Errorf("result = %q, want the raw transcript unchanged (no LLM configured)", result)
	}
}

func TestHandleSearchChats_ReadRawModeTruncatesLongTranscripts(t *testing.T) {
	ctx := newTestContext()
	long := strings.Repeat("x", rawReadMaxChars+500)
	ctx.ReadThread = func(threadID string) (*store.ThreadReadResult, error) {
		return &store.ThreadReadResult{ThreadID: threadID, Content: long}, nil
	}
	result := handleSearchChats(`{"action":"read","thread_id":"abc"}`, ctx, "test-call")
	if len(result) <= rawReadMaxChars || len(result) >= len(long) {
		t.Errorf("result length = %d, want truncated somewhere between %d and %d", len(result), rawReadMaxChars, len(long))
	}
	if !strings.Contains(result, "truncated") {
		t.Errorf("result = %q, want a truncation note", result)
	}
}

// TestHandleSearchChats_ReadFilterPassCostReachesContext is search_chats'
// version of TestHandleWebRead_FilterPassCostReachesContext — the same
// regression this project already fixed once for web_read/read_attachment
// (see Context.ExtraCostUSD's doc comment): a filter pass's real LLM spend
// must reach ctx.AddCost, not be silently discarded.
func TestHandleSearchChats_ReadFilterPassCostReachesContext(t *testing.T) {
	ctx := newTestContext()
	ctx.ReadThread = func(threadID string) (*store.ThreadReadResult, error) {
		return &store.ThreadReadResult{ThreadID: threadID, Content: "User: did I ask about GPUs?\n\nAssistant: yes, a 4070."}, nil
	}
	ctx.LLM = &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "You decided on a 4070.", CostUSD: 0.002}}},
	}

	result := handleSearchChats(`{"action":"read","thread_id":"abc","instructions":"what did we decide"}`, ctx, "test-call")
	if result != "You decided on a 4070." {
		t.Errorf("result = %q, want the filtered answer", result)
	}
	if ctx.ExtraCostUSD != 0.002 {
		t.Errorf("ctx.ExtraCostUSD = %v, want 0.002 — the filter pass's cost never reached the turn's cost tracking", ctx.ExtraCostUSD)
	}
}

// TestHandleSearchChats_ReadFilterFailureFallsBackToRaw mirrors web_read's
// own filter-failure fallback (TestHandleWebRead_FilterFailureFallsBackToFullText)
// — a filter LLM error shouldn't fail the whole read, just degrade to the
// raw transcript.
func TestHandleSearchChats_ReadFilterFailureFallsBackToRaw(t *testing.T) {
	ctx := newTestContext()
	ctx.ReadThread = func(threadID string) (*store.ThreadReadResult, error) {
		return &store.ThreadReadResult{ThreadID: threadID, Content: "User: hello\n\nAssistant: hi there"}, nil
	}
	ctx.LLM = &llmtest.MockClient{
		Responses: []llmtest.Response{{Err: context.DeadlineExceeded}},
	}

	result := handleSearchChats(`{"action":"read","thread_id":"abc","instructions":"anything"}`, ctx, "test-call")
	if result != "User: hello\n\nAssistant: hi there" {
		t.Errorf("result = %q, want the raw transcript as a fallback", result)
	}
	if ctx.ExtraCostUSD != 0 {
		t.Errorf("ctx.ExtraCostUSD = %v, want 0 — a failed filter call shouldn't record any cost", ctx.ExtraCostUSD)
	}
}

func TestHandleSearchChats_ReadUnknownThreadReturnsError(t *testing.T) {
	ctx := newTestContext()
	ctx.ReadThread = func(threadID string) (*store.ThreadReadResult, error) {
		return nil, errors.New("no rows")
	}
	result := handleSearchChats(`{"action":"read","thread_id":"does-not-exist"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for an unknown thread_id", result)
	}
}

func TestHandleSearchChats_NotAvailableWithoutClosures(t *testing.T) {
	ctx := newTestContext() // no SearchThreads/ListRecentThreads/ReadThread wired

	if r := handleSearchChats(`{"action":"search","query":"anything"}`, ctx, "id1"); !strings.Contains(r, "not available") {
		t.Errorf("keyword search result = %q, want a not-available error", r)
	}
	if r := handleSearchChats(`{"action":"search"}`, ctx, "id2"); !strings.Contains(r, "not available") {
		t.Errorf("recency search result = %q, want a not-available error", r)
	}
	if r := handleSearchChats(`{"action":"read","thread_id":"abc"}`, ctx, "id3"); !strings.Contains(r, "not available") {
		t.Errorf("read result = %q, want a not-available error", r)
	}
}

func TestHandleSearchChats_UnknownAction(t *testing.T) {
	ctx := newTestContext()
	result := handleSearchChats(`{"action":"delete"}`, ctx, "test-call")
	if !strings.Contains(result, "unknown action") {
		t.Errorf("result = %q, want an unknown-action error", result)
	}
}
