package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/config"
	"polaris/llm"
)

// A caller can hold an empty model id (a Weaver config with no model chosen
// means "the default"). agent.CompleteOneShot registers a catalog entry under
// the id and panics on "", which took down the star-reconcile handler in tests
// before routedComplete learned to degrade to the direct client. Reverting the
// guard makes this panic.
func TestRoutedComplete_EmptyModelIDFallsBackToDirectInsteadOfPanicking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"direct answer"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0002}}`+"\n\n")
	}))
	defer srv.Close()
	client := llm.NewClient(srv.URL, "k", "test/model", 0.7, 100)

	s := &Server{}
	complete := s.routedComplete(&config.Config{}, client, "", "sess", nil)
	text, cost, err := complete(context.Background(), []llm.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if text != "direct answer" || cost != 0.0002 {
		t.Errorf("got %q cost %v, want the direct client's answer and cost", text, cost)
	}
}

// The vision describe now goes through the one-shot router instead of
// llm.Client.DescribeImage's own HTTP request. What the model receives must not
// change: one user message whose content is the describe prompt as a text block
// followed by the image as an image_url data URL, with the optional focus
// appended to the prompt.
func TestVisionDescriber_SendsTheSameImageRequestAndReturnsTextAndCost(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a red square"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0003,"prompt_tokens":10,"completion_tokens":3}}`+"\n\n")
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	cfg := h.srvObj.liveConfig()
	vision, found := cfg.MultimodalModel()
	if !found {
		t.Skip("the test registry has no multimodal model")
	}
	describe, ok := h.srvObj.visionDescriber(cfg, config.ModelConfig{}, "thread-1")
	if !ok {
		t.Fatal("visionDescriber found no vision model, but the registry has one")
	}

	text, cost, err := describe(context.Background(), "QUJD", "image/png", "the colour")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if text != "a red square" || cost <= 0 {
		t.Errorf("got %q cost %v, want the model's text and a positive cost", text, cost)
	}

	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want exactly the one user message", len(msgs))
	}
	m := msgs[0].(map[string]any)
	blocks, _ := m["content"].([]any)
	if m["role"] != "user" || len(blocks) != 2 {
		t.Fatalf("message = %v, want a user message with [text, image_url]", m)
	}
	first, second := blocks[0].(map[string]any), blocks[1].(map[string]any)
	if first["type"] != "text" || !strings.Contains(first["text"].(string), "Focus specifically on: the colour") {
		t.Errorf("first block = %v, want the describe prompt with the focus appended", first)
	}
	url, _ := second["image_url"].(map[string]any)["url"].(string)
	if second["type"] != "image_url" || url != "data:image/png;base64,QUJD" {
		t.Errorf("second block = %v, want the image as a data URL", second)
	}
	if body["model"] == "" || body["model"] == nil {
		t.Errorf("no model on the wire (vision model %s)", vision.ID)
	}
}

// RunShootingStar documents that a nil cfg (tests) keeps the direct path, and
// routedCompleteFor did not honor it: a nil cfg with a real *llm.Client
// nil-dereferenced inside routing.Build. pi-review spotted it; reverting the
// guard makes this panic.
func TestRoutedComplete_NilConfigFallsBackToDirectInsteadOfPanicking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"direct answer"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0002}}`+"\n\n")
	}))
	defer srv.Close()
	client := llm.NewClient(srv.URL, "k", "test/model", 0.7, 100)

	complete := routedCompleteFor(nil, "Polaris/test", nil, client, "some-model", "sess", nil)
	text, _, err := complete(context.Background(), []llm.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || text != "direct answer" {
		t.Fatalf("got %q, %v; want the direct client's answer", text, err)
	}
}
