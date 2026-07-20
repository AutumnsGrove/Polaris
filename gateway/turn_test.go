package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/config"
	"polaris/llm"
	"polaris/llm/llmtest"
)

func TestCompactThread(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := h.db.AddMessage("t1", "user", "hello", "[]", "[]", 0)
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

func TestLoadHistory_SubstitutesCompactedSummary(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "old question", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	oldAnswerID, err := h.db.AddMessage("t1", "assistant", "old answer", "[]", "[]", 0)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "new question after compaction", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := h.db.CompactThread("t1", "summary of old exchange", oldAnswerID, 0, 10); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	s := &Server{db: h.db}
	history, err := s.loadHistory("t1")
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

func TestGenerateSuggestions(t *testing.T) {
	srv := fakeLLMServer(t, "follow-up questions", "What is X?\nWhat is Y?\n1. What is Z?")
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL))
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

func TestGenerateTitle(t *testing.T) {
	srv := fakeLLMServer(t, "thread title", `"France's Capital City."`)
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	title, _, err := s.generateTitle(cfg, modelCfg, "what is the capital of france", "Paris")
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
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := config.ModelConfig{ID: "test-model", Model: "test/model", Provider: []string{"test"}}

	s := &Server{}
	title, _, err := s.generateTitle(cfg, modelCfg, "q", "a")
	if err != nil {
		t.Fatalf("generateTitle returned error: %v", err)
	}
	if len(title) > maxTitleLen {
		t.Errorf("len(title) = %d, want capped at maxTitleLen (%d)", len(title), maxTitleLen)
	}
}
