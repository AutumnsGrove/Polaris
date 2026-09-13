package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/store"
)

func seedWeaverThread(t *testing.T, db *store.Store, messages ...string) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.CreateThread(id, "Test thread", "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i, content := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := db.AddMessage(id, role, content, "[]", "[]", 0, ""); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}
	return id
}

func createStarToolCall(title, category, summary, confidenceClass string) *llm.ChatResponse {
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "create_star",
			Arguments: `{"title":"` + title + `","category":"` + category + `","summary":"` + summary + `","body":"body text","confidence_class":"` + confidenceClass + `"}`,
		},
	}}}
}

func TestRunShootingStar_FirstPass_CreatesStarAndLinksSource(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "What's the deal with Cloudflare Workers?", "They run JS at the edge.")

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: createStarToolCall("Cloudflare Workers", "technology", "Edge compute platform", "obvious")},
		{Resp: &llm.ChatResponse{Content: "Noted a new interest in Cloudflare Workers."}},
	}}

	if err := RunShootingStar(context.Background(), db, mock, threadID); err != nil {
		t.Fatalf("RunShootingStar: %v", err)
	}

	stars, err := db.ListStars(store.StarFilter{Statuses: []string{"auto"}})
	if err != nil {
		t.Fatalf("ListStars: %v", err)
	}
	if len(stars) != 1 || stars[0].Title != "Cloudflare Workers" {
		t.Fatalf("ListStars = %+v, want the one star Weaver created", stars)
	}

	sources, err := db.StarSources(stars[0].ID)
	if err != nil {
		t.Fatalf("StarSources: %v", err)
	}
	if len(sources) != 1 || sources[0].ThreadID != threadID {
		t.Errorf("StarSources = %+v, want the originating thread linked", sources)
	}

	run, err := db.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if run == nil || run.NeedsRetry || run.Error != "" {
		t.Fatalf("LastShootingStarRun = %+v, want a clean successful run", run)
	}
	if run.Summary == "" {
		t.Error("run.Summary is empty, want Weaver's closing wrap-up sentence")
	}

	candidates, err := db.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if candidates.StarCountsByStatus["auto"] != 1 {
		t.Errorf("StarCountsByStatus = %+v, want auto=1", candidates.StarCountsByStatus)
	}
}

func TestRunShootingStar_Revisit_FeedsFilteredDeltaNotRawThread(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "What's the deal with Cloudflare Workers?", "They run JS at the edge.")

	// Seed a completed first pass so the next call takes the revisit path.
	starID, err := db.CreateStar(store.Star{Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute", Status: "auto"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	if err := db.LinkStarSource(starID, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	runID, err := db.StartShootingStarRun(threadID, 2)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := db.FinishShootingStarRun(runID, "First pass done.", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	// New messages since the last run — the delta the revisit path should
	// filter and feed to Weaver, never the raw full thread.
	if _, err := db.AddMessage(threadID, "user", "Also, is it faster than Lambda@Edge?", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := db.AddMessage(threadID, "assistant", "Generally yes, due to V8 isolates over containers.", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "V8 isolates make Workers faster than Lambda@Edge's containers."}}, // filter pass
		{Resp: &llm.ChatResponse{Content: "No new star needed, just an addendum noted."}},                   // Weaver's own turn
	}}

	if err := RunShootingStar(context.Background(), db, mock, threadID); err != nil {
		t.Fatalf("RunShootingStar: %v", err)
	}

	if len(mock.Calls) < 2 {
		t.Fatalf("mock.Calls = %d, want at least 2 (filter pass + Weaver's own turn)", len(mock.Calls))
	}
	// The Weaver turn's own messages must carry the filtered delta content,
	// not the original question from the first pass (which the revisit
	// path must never re-read).
	lastCall := mock.Calls[len(mock.Calls)-1]
	var sawFilteredContent, sawOriginalQuestion bool
	for _, m := range lastCall.Messages {
		if strings.Contains(m.Content, "V8 isolates make Workers faster") {
			sawFilteredContent = true
		}
		if strings.Contains(m.Content, "What's the deal with Cloudflare Workers?") {
			sawOriginalQuestion = true
		}
	}
	if !sawFilteredContent {
		t.Errorf("Weaver's turn didn't receive the filtered delta content: %+v", lastCall.Messages)
	}
	if sawOriginalQuestion {
		t.Errorf("Weaver's turn re-read the original first-pass question — revisit should only see the new delta: %+v", lastCall.Messages)
	}
}

func TestRunShootingStar_HitsTurnCap_SetsNeedsRetryAndError(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "Tell me about Rust's borrow checker.")

	var responses []llmtest.Response
	// weaverMaxTurns tool-call responses, never producing a plain-text
	// answer, plus one more for the forced wrap-up agent.Run issues after
	// the cap — see agent/driver.go's "Ran out of turns" branch.
	for i := 0; i < weaverMaxTurns; i++ {
		responses = append(responses, llmtest.Response{
			Resp: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
				ID: "call", Type: "function",
				Function: llm.FunctionCall{Name: "search_stars", Arguments: `{"query":"rust"}`},
			}}},
		})
	}
	responses = append(responses, llmtest.Response{Resp: &llm.ChatResponse{Content: "forced wrap-up text"}})
	mock := &llmtest.MockClient{Responses: responses}

	err := RunShootingStar(context.Background(), db, mock, threadID)
	if err == nil {
		t.Fatal("RunShootingStar should return an error when the turn cap is hit")
	}

	run, err := db.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if run == nil || !run.NeedsRetry || run.Error != "max_turns_exceeded" {
		t.Fatalf("LastShootingStarRun = %+v, want needs_retry=true, error=max_turns_exceeded", run)
	}
}

// TestRunShootingStar_LinkStarsRejectsUnseenStarID is a defense-in-depth
// regression test for the star_id provenance check in
// newWeaverToolContext: update_star/link_stars may only target a star_id
// this exact run has already surfaced via search_stars/read_star (or just
// created itself), never one that appears out of nowhere in a tool call.
// The plan doc's own "Weaver's tools" section already states read_star is
// "Mandatory before update_star or link_stars, never optional" — this
// backstops that as an enforced invariant rather than only a prompt
// instruction a sufficiently-adversarial thread's content could talk
// Weaver out of (see newWeaverToolContext's own doc comment on why this
// matters more than it might look for a single-operator tool: the thread
// content Weaver reads can itself contain text originally fetched from the
// open web by a normal chat turn's web_search/web_read).
func TestRunShootingStar_LinkStarsRejectsUnseenStarID(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "Tell me more about Rust's borrow checker.")

	// A pre-existing star from some unrelated earlier run — never searched
	// or read during *this* run, so id=1.
	existingID, err := db.CreateStar(store.Star{Title: "Existing star", Category: "technology", Summary: "s", Status: "auto"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		// create_star's own closure marks its new id (2) as seen, but
		// existingID (1) was never looked up in this run at all.
		{Resp: createStarToolCall("Rust", "technology", "Systems language", "obvious")},
		{Resp: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
			ID: "call_link", Type: "function",
			Function: llm.FunctionCall{
				Name:      "link_stars",
				Arguments: fmt.Sprintf(`{"star_id_a":%d,"star_id_b":2,"reasoning":"both about programming"}`, existingID),
			},
		}}}},
		{Resp: &llm.ChatResponse{Content: "done"}},
	}}

	if err := RunShootingStar(context.Background(), db, mock, threadID); err != nil {
		t.Fatalf("RunShootingStar: %v", err)
	}

	edges, err := db.StarEdges(existingID)
	if err != nil {
		t.Fatalf("StarEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("StarEdges(existingID) = %+v, want no edge — link_stars must reject a star_id never looked up this run", edges)
	}
}

// openTestStoreForConstellation mirrors store.openTestStore, duplicated
// here since that helper is unexported to the store package.
func openTestStoreForConstellation(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
