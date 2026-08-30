package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"polaris/config"
	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/models"
)

func TestCompactThread(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := h.db.AddMessage("t1", "user", "hello", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "a concise summary of the exchange", CostUSD: 0.002}}},
	}

	s := &Server{db: h.db}
	summary, cost, err := s.compactThread(mock, "t1", msgID)
	if err != nil {
		t.Fatalf("compactThread returned error: %v", err)
	}
	if summary != "a concise summary of the exchange" {
		t.Errorf("summary = %q", summary)
	}
	if cost != 0.002 {
		t.Errorf("cost = %v, want 0.002", cost)
	}

	thread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CompactedSummary != summary || thread.CompactedThroughID != msgID {
		t.Errorf("thread = %+v, want the compaction persisted", thread)
	}
}

// TestCompactThread_EmptySummaryIsAnError guards against a real bug: an
// empty summary used to be persisted as-is, silently and permanently
// replacing everything through msgID with "" — a reasoning-exhaustion
// failure mode (see generateTitle's doc comment) is far worse here than
// in a title/suggestions call, since CompactThread marks that empty
// string as covering the thread's whole prior history.
func TestCompactThread_EmptySummaryIsAnError(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := h.db.AddMessage("t1", "user", "hello", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "   ", CostUSD: 0.002}}},
	}

	s := &Server{db: h.db}
	if _, _, err := s.compactThread(mock, "t1", msgID); err == nil {
		t.Fatal("compactThread returned no error for a blank summary, want an error")
	}

	thread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CompactedSummary != "" || thread.CompactedThroughID != 0 {
		t.Errorf("thread = %+v, want compaction to have never been persisted", thread)
	}
}

func TestLoadHistory_SubstitutesCompactedSummary(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "old question", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	oldAnswerID, err := h.db.AddMessage("t1", "assistant", "old answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "new question after compaction", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := h.db.CompactThread("t1", "summary of old exchange", oldAnswerID, 0, 10); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	s := &Server{db: h.db}
	history, err := s.loadHistory("t1", 0)
	if err != nil {
		t.Fatalf("loadHistory returned error: %v", err)
	}

	if len(history) != 2 {
		t.Fatalf("got %d history messages, want 2 (summary + the post-compaction question): %+v", len(history), history)
	}
	if !strings.Contains(history[0].Content, "summary of old exchange") {
		t.Errorf("history[0] = %+v, want it to contain the compacted summary", history[0])
	}
	if history[1].Content != "new question after compaction" {
		t.Errorf("history[1] = %+v, want the message after compaction, uncompacted", history[1])
	}
}

// fakeLLMServer serves one canned SSE response, so
// generateSuggestions/generateTitle (which each build their own client
// from cfg rather than taking one as a parameter) can be tested
// end-to-end against something other than real OpenRouter. systemPrompt
// is unused (kept as a parameter for readability at call sites — a
// reminder of which system prompt this fake stands in for) since each
// test only ever needs one canned answer.
func fakeLLMServer(t *testing.T, systemPrompt, answer string) *httptest.Server {
	t.Helper()
	_ = systemPrompt
	chunk, err := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": answer}}},
	})
	if err != nil {
		t.Fatalf("marshaling fake SSE chunk: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// handleTurn measures elapsed time at millisecond resolution
		// (time.Since(...).Milliseconds()) and TestHandleAsk_..._
		// PersistsSameAsChatTurn asserts DurationMs > 0 — a real
		// guarantee API callers depend on, not a testing artifact. Against
		// this in-memory localhost server, the whole turn can otherwise
		// complete inside the same millisecond it started, making that
		// assertion flaky rather than the feature actually being broken.
		// This sleep is enough to guarantee real elapsed time to measure
		// without meaningfully slowing any test that uses this helper.
		time.Sleep(2 * time.Millisecond)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", chunk)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnswerEndsInQuestion(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"Here's a muscle-building routine.", false},
		{"What are you actually training for?", true},
		{"What are you actually training for?\n", true},
		{"**What are you actually training for?**", true},
		{"Does the love of the craft feel most alive in the process itself, or in the result?", true},
		{"是的，这样可以吗？", true},
		{"", false},
	}
	for _, c := range cases {
		if got := answerEndsInQuestion(c.answer); got != c.want {
			t.Errorf("answerEndsInQuestion(%q) = %v, want %v", c.answer, got, c.want)
		}
	}
}

func TestGenerateSuggestions(t *testing.T) {
	srv := fakeLLMServer(t, "follow-up questions", "What is X?\nWhat is Y?\n1. What is Z?")
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	suggestions, cost, err := s.generateSuggestions(cfg, modelCfg, "question", "answer")
	if err != nil {
		t.Fatalf("generateSuggestions returned error: %v", err)
	}
	if len(suggestions) != 3 {
		t.Fatalf("got %d suggestions, want 3: %+v", len(suggestions), suggestions)
	}
	if suggestions[2] != "What is Z?" {
		t.Errorf("suggestions[2] = %q, want the numbered prefix stripped", suggestions[2])
	}
	if cost != 0.0001 {
		t.Errorf("cost = %v, want 0.0001", cost)
	}
}

// TestGenerateSuggestions_RejectsContinuationSentences reproduces a real
// production failure: asked to suggest follow-ups after a PaLM/dense-LLM
// answer, the model instead returned a single sentence continuing the
// answer itself ("The model was never open-sourced, but the architecture
// and results were published.") — no question mark, not a question at
// all. The parse loop's trailing-"?" filter must drop it rather than
// surface it as a "suggestion".
func TestGenerateSuggestions_RejectsContinuationSentences(t *testing.T) {
	srv := fakeLLMServer(t, "follow-up questions",
		"The model was never open-sourced, but the architecture and results were published.")
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	suggestions, _, err := s.generateSuggestions(cfg, modelCfg, "question", "answer")
	if err != nil {
		t.Fatalf("generateSuggestions returned error: %v", err)
	}
	if len(suggestions) != 0 {
		t.Errorf("suggestions = %+v, want none — a non-question continuation sentence must be dropped, not shown as a suggestion", suggestions)
	}
}

func TestGenerateTitle(t *testing.T) {
	srv := fakeLLMServer(t, "thread title", `"France's Capital City."`)
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	title, _, err := s.generateTitle(cfg, modelCfg, "what is the capital of france")
	if err != nil {
		t.Fatalf("generateTitle returned error: %v", err)
	}
	if title != "France's Capital City" {
		t.Errorf("title = %q, want surrounding quotes and trailing period stripped", title)
	}
}

func TestGenerateTitle_TruncatesOverlongTitle(t *testing.T) {
	huge := strings.Repeat("word ", 30)
	srv := fakeLLMServer(t, "thread title", huge)
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	title, _, err := s.generateTitle(cfg, modelCfg, "q")
	if err != nil {
		t.Fatalf("generateTitle returned error: %v", err)
	}
	if len(title) > maxTitleLen {
		t.Errorf("len(title) = %d, want capped at maxTitleLen (%d)", len(title), maxTitleLen)
	}
}

func TestRegenerateTitle(t *testing.T) {
	srv := fakeLLMServer(t, "thread title (full context)", `"Trip Planning and Budget"`)
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	// A multi-turn history — the whole point of regenerateTitle is that
	// this reflects the later follow-up too, not just the opening message.
	history := []llm.ChatMessage{
		{Role: "user", Content: "where should I go on vacation"},
		{Role: "assistant", Content: "Japan is a great choice."},
		{Role: "user", Content: "what's a reasonable budget for two weeks there"},
	}

	s := &Server{}
	title, _, err := s.regenerateTitle(cfg, modelCfg, history)
	if err != nil {
		t.Fatalf("regenerateTitle returned error: %v", err)
	}
	if title != "Trip Planning and Budget" {
		t.Errorf("title = %q, want surrounding quotes stripped", title)
	}
}

func TestRegenerateTitle_NoHistoryIsAnError(t *testing.T) {
	s := &Server{}
	cfg := &config.Config{}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model"}
	if _, _, err := s.regenerateTitle(cfg, modelCfg, nil); err == nil {
		t.Error("expected an error for empty history, got nil")
	}
}
