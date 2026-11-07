package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/testkit"

	"polaris/config"
	"polaris/llm"
)

func oneshotTestConfig(t *testing.T, baseURL string) *config.Config {
	t.Helper()
	contents := "providers:\n  openrouter:\n    api_key: \"k\"\n    base_url: \"" + baseURL + "\"\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	registry := []config.ModelConfig{
		{ID: "test-model", Name: "Test Model", Model: "test/slug", Temperature: 0.7, MaxTokens: 100},
	}
	cfg, err := config.Load(path, registry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return cfg
}

func TestCompleteOneShot_RoutesByModelID(t *testing.T) {
	srv := testkit.NewServer(t,
		testkit.ServerTurn{Turn: testkit.Turn{
			Text:  "one-shot answer",
			Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8, CostUSD: 0.002, CostSource: "provider"},
		}},
	)
	cfg := oneshotTestConfig(t, srv.URL)
	client := llm.NewClient(srv.URL, "k", "test/slug", 0.7, 100)

	res, err := CompleteOneShot(context.Background(), cfg, client, "test-model",
		OneShotOptions{SessionID: "thread-9", UserAgent: "polaris-test/1"},
		[]llm.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("CompleteOneShot returned error: %v", err)
	}
	if res.Text != "one-shot answer" {
		t.Errorf("Text = %q, want the scripted answer", res.Text)
	}
	if res.Provider != "openrouter" {
		t.Errorf("Provider = %q, want openrouter", res.Provider)
	}
	if res.Spilled {
		t.Error("Spilled = true, want false (single OpenRouter leg)")
	}
	if res.ListCost != 0.002 {
		t.Errorf("ListCost = %v, want 0.002", res.ListCost)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if body.Model != "test/slug" {
		t.Errorf("wire model = %q, want the registry slug", body.Model)
	}
}
