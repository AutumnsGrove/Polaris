package routing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AutumnsGrove/nova/provider"

	"polaris/config"
)

var testRegistry = []config.ModelConfig{
	{ID: "go-model", Name: "Go Model", Model: "vendor/go-model-1", GoModel: "go-model-1"},
	{ID: "plain-model", Name: "Plain Model", Model: "vendor/plain-1"},
}

func loadTestConfig(t *testing.T, contents string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := config.Load(path, testRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return cfg
}

const keyedBoth = `providers:
  openrouter:
    api_key: "or-key"
  opencode_go:
    api_key: "go-key"
`

// The dark-ship merge builds no Go routes: with default_backend openrouter
// every id resolves to a single OpenRouter attempt carrying the registry
// slug, so traffic is byte-identical to before.
func TestBuild_DarkByDefault(t *testing.T) {
	cfg := loadTestConfig(t, keyedBoth+`routing:
  default_backend: openrouter
`)
	r := Build(cfg, Options{SessionID: func() string { return "thread-1" }, UserAgent: "polaris-test/1"})
	prov, wire, ok := r.Primary("go-model")
	if !ok {
		t.Fatal("Primary(go-model) not found")
	}
	if prov != OpenRouter || wire != "vendor/go-model-1" {
		t.Errorf("Primary(go-model) = %q %q, want openrouter vendor/go-model-1", prov, wire)
	}
	rt, _ := r.Resolve("go-model")
	if len(rt.Attempts) != 1 {
		t.Errorf("dark route has %d attempts, want 1 (no failover leg to build)", len(rt.Attempts))
	}
}

// Auto sends eligible models Go-first (Go slug, then the OpenRouter slug)
// and leaves go_model-less models on a single OpenRouter attempt.
func TestBuild_AutoRoutes(t *testing.T) {
	cfg := loadTestConfig(t, keyedBoth+`routing:
  default_backend: auto
`)
	r := Build(cfg, Options{})
	prov, wire, _ := r.Primary("go-model")
	if prov != OpenCodeGo || wire != "go-model-1" {
		t.Errorf("Primary(go-model) = %q %q, want opencode-go go-model-1 (bare slug, never vendor-prefixed)", prov, wire)
	}
	rt, found := r.Resolve("go-model")
	if !found {
		t.Fatal("Resolve(go-model) not found")
	}
	if len(rt.Attempts) != 2 || rt.Attempts[0].Provider != OpenCodeGo || rt.Attempts[1].Provider != OpenRouter {
		t.Errorf("go-model attempts = %+v, want [opencode-go, openrouter]", rt.Attempts)
	}
	if rt.Attempts[1].Model != "vendor/go-model-1" {
		t.Errorf("failover leg wire model = %q, want the OpenRouter slug", rt.Attempts[1].Model)
	}
	prov, wire, _ = r.Primary("plain-model")
	if prov != OpenRouter || wire != "vendor/plain-1" {
		t.Errorf("Primary(plain-model) = %q %q, want openrouter vendor/plain-1", prov, wire)
	}
	rt, _ = r.Resolve("plain-model")
	if len(rt.Attempts) != 1 {
		t.Errorf("plain-model has %d attempts, want 1", len(rt.Attempts))
	}
}

// failover: none omits the second attempt — there is no separate flag, the
// route is just single-legged.
func TestBuild_FailoverNoneOmitsSecondAttempt(t *testing.T) {
	cfg := loadTestConfig(t, keyedBoth+`routing:
  default_backend: auto
  failover: none
`)
	r := Build(cfg, Options{})
	rt, _ := r.Resolve("go-model")
	if len(rt.Attempts) != 1 || rt.Attempts[0].Provider != OpenCodeGo {
		t.Errorf("failover:none route = %+v, want single opencode-go attempt", rt.Attempts)
	}
}

// An explicit model_backends entry beats the default either way, and a
// go_model-less model under the opencode_go default stays on OpenRouter
// rather than being sent to Go carrying an OpenRouter slug.
func TestBuild_Overrides(t *testing.T) {
	cfg := loadTestConfig(t, keyedBoth+`routing:
  default_backend: auto
model_backends:
  go-model: openrouter
`)
	r := Build(cfg, Options{})
	if prov, _, _ := r.Primary("go-model"); prov != OpenRouter {
		t.Errorf("override to openrouter = %q, want openrouter", prov)
	}

	cfg = loadTestConfig(t, keyedBoth+`routing:
  default_backend: opencode_go
`)
	r = Build(cfg, Options{})
	if prov, _, _ := r.Primary("plain-model"); prov != OpenRouter {
		t.Errorf("opencode_go default for go_model-less model = %q, want openrouter", prov)
	}
	if prov, _, _ := r.Primary("go-model"); prov != OpenCodeGo {
		t.Errorf("opencode_go default for eligible model = %q, want opencode-go", prov)
	}
}

// Build registers every registry id in Nova's catalog, so chat.Complete
// can translate a Polaris id to its wire slug. Without this the router
// keys on the wire slug, finds no route, and silently bills OpenRouter.
func TestBuild_RegistersModels(t *testing.T) {
	cfg := loadTestConfig(t, keyedBoth)
	Build(cfg, Options{})
	m, ok := provider.GetModel("go-model")
	if !ok {
		t.Fatal("go-model not registered in Nova catalog")
	}
	if m.Slug != "vendor/go-model-1" {
		t.Errorf("registered slug = %q, want vendor/go-model-1", m.Slug)
	}
}

// ListCost is the one expression correct in all three call shapes; the
// failure mode is a plausible-looking wrong number, so each shape is
// pinned here, not just trusted to the doc comment.
func TestListCost_Shapes(t *testing.T) {
	// A single Go attempt: CostUSD is allowance, ListCostUSD is retail.
	goCall := provider.Usage{CostUSD: 0.02, CostSource: "allowance", AllowanceUSD: 0.02, ListCostUSD: 0.04, BillingSource: "go"}
	if got := ListCost(goCall); got != 0.04 {
		t.Errorf("Go call ListCost = %v, want 0.04 (retail, not allowance)", got)
	}
	// An OpenRouter attempt: both Go fields zero, CostUSD already retail.
	orCall := provider.Usage{CostUSD: 0.01, CostSource: "provider"}
	if got := ListCost(orCall); got != 0.01 {
		t.Errorf("OpenRouter call ListCost = %v, want 0.01", got)
	}
	// A spilled call: summed CostUSD is allowance+retail, ListCostUSD
	// covers only the Go attempt — the subtraction removes the allowance.
	spilled := provider.Usage{CostUSD: 0.02 + 0.05, CostSource: "provider", AllowanceUSD: 0.02, ListCostUSD: 0.04}
	if got := ListCost(spilled); got != 0.09 {
		t.Errorf("spilled call ListCost = %v, want 0.09 (both legs' retail)", got)
	}
	// A covered free-model call still has a real list price: recording
	// CostUSD ($0) would zero it.
	free := provider.Usage{CostUSD: 0, CostSource: "allowance", AllowanceUSD: 0, ListCostUSD: 0.003, BillingSource: "free"}
	if got := ListCost(free); got != 0.003 {
		t.Errorf("free call ListCost = %v, want 0.003", got)
	}
}

// A Go-only deployment (no OpenRouter key) must build a router rather than
// panic in route.New: eligible models go to Go with no spill leg, and a
// go_model-less model has no route to build. Before the fix the plain
// model's OpenRouter route and the failover attempt both named a provider
// that wasn't declared.
func TestBuild_GoOnlyDoesNotPanic(t *testing.T) {
	cfg := loadTestConfig(t, `providers:
  opencode_go:
    api_key: "go-key"
`)
	r := Build(cfg, Options{})
	prov, wire, ok := r.Primary("go-model")
	if !ok || prov != OpenCodeGo || wire != "go-model-1" {
		t.Errorf("Primary(go-model) = %q %q %v, want opencode-go go-model-1", prov, wire, ok)
	}
	rt, _ := r.Resolve("go-model")
	if len(rt.Attempts) != 1 {
		t.Errorf("go-only route has %d attempts, want 1 (nowhere to fail over to)", len(rt.Attempts))
	}
}

// A hand-built config with only the legacy openrouter: block (no Providers
// map) must still build — the fixture shape several gateway tests use.
func TestBuild_LegacyOnlyBlockDoesNotPanic(t *testing.T) {
	cfg := &config.Config{Models: testRegistry}
	cfg.OpenRouter.APIKey = "or-key"
	cfg.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	r := Build(cfg, Options{})
	if prov, _, ok := r.Primary("plain-model"); !ok || prov != OpenRouter {
		t.Errorf("Primary(plain-model) = %q %v, want openrouter", prov, ok)
	}
}
