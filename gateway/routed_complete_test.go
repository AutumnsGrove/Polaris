package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
