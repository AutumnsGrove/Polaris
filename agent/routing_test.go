package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/testkit"

	"polaris/config"
	"polaris/llm"
	"polaris/routing"
	"polaris/tools"
)

var routedTestRegistry = []config.ModelConfig{
	{ID: "test-model", Name: "Test Model", Model: "test/slug", Temperature: 0.4, MaxTokens: 100},
}

func routedTestConfig(t *testing.T, openRouterBaseURL, extra string) *config.Config {
	t.Helper()
	contents := "providers:\n  openrouter:\n    api_key: \"k\"\n    base_url: \"" + openRouterBaseURL + "\"\n" + extra
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := config.Load(path, routedTestRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return cfg
}

func discardEmit(string, map[string]interface{}) {}

// A routed turn resolves the catalog id through the router (wire slug on
// the wire, never the catalog id), attributes the call to the serving
// backend, and records list cost — the three wirings §8 says must exist
// before anything routes, because without them every call silently bills
// OpenRouter while looking exactly like success.
func TestRoutedTurn_ResolvesModelIDAndAttributes(t *testing.T) {
	srv := testkit.NewServer(t,
		testkit.ServerTurn{Turn: testkit.Turn{
			Text:  "routed answer",
			Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CostUSD: 0.001, CostSource: "provider"},
		}},
	)
	cfg := routedTestConfig(t, srv.URL, "routing:\n  default_backend: openrouter\n")
	r := routing.Build(cfg, routing.Options{})

	client := llm.NewClient(srv.URL, "k", "test/slug", 0, 100)
	ctx := &tools.Context{LLM: client, Emit: discardEmit, MaxTurns: 2, ModelID: "test-model", Router: r}
	res, err := runWithNova(context.Background(), ctx, nil, "hello")
	if err != nil {
		t.Fatalf("runWithNova returned error: %v", err)
	}
	if !strings.Contains(res.Answer, "routed answer") {
		t.Errorf("Answer = %q, want it to contain the scripted reply", res.Answer)
	}
	if len(res.ProviderCalls) != 1 {
		t.Fatalf("ProviderCalls has %d entries, want 1", len(res.ProviderCalls))
	}
	if got := res.ProviderCalls[0].Provider; got != "openrouter" {
		t.Errorf("ProviderCalls[0].Provider = %q, want openrouter", got)
	}
	if res.CostUSD != 0.001 {
		t.Errorf("CostUSD = %v, want 0.001 (list cost of the OpenRouter leg)", res.CostUSD)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatalf("decoding recorded request body: %v", err)
	}
	if body.Model != "test/slug" {
		t.Errorf("wire model = %q, want the registry slug test/slug (never the catalog id)", body.Model)
	}
}

// A context with a router but no ModelID runs direct: the router would key
// on the wire slug, find no route, and silently bill OpenRouter — the
// exact misroute the guard in novaProviderFor exists to prevent.
func TestNovaProviderFor_MissingModelIDRunsDirect(t *testing.T) {
	srv := testkit.NewServer(t)
	cfg := routedTestConfig(t, srv.URL, "routing:\n  default_backend: auto\n")
	r := routing.Build(cfg, routing.Options{})
	client := llm.NewClient(srv.URL, "k", "test/slug", 0, 100)

	p, m := novaProviderFor(&tools.Context{LLM: client, Router: r})
	if m.ModelID != "" {
		t.Errorf("ModelID = %q, want empty (no catalog id, no routing)", m.ModelID)
	}
	if p.Name() != "openrouter" {
		t.Errorf("provider = %q, want the direct openrouter provider", p.Name())
	}

	p, m = novaProviderFor(&tools.Context{LLM: client, Router: r, ModelID: "test-model"})
	if m.ModelID != "test-model" {
		t.Errorf("ModelID = %q, want test-model", m.ModelID)
	}
	if p.Name() != "route" {
		t.Errorf("provider = %q, want the router", p.Name())
	}
}
