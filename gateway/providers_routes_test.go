package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The providers status endpoint reports configuration, effective policy,
// and the per-model route table — and degrades, never fails, when Go is
// unconfigured: the meters probe is skipped and the error names the
// missing key.
func TestProvidersStatus_LegacyConfig(t *testing.T) {
	// The pinned assertion below is about the un-pinned env; without this the
	// test fails whenever the suite runs under the POLARIS_NOVA_LOOP=0 kill
	// switch it is meant to be green under. "1", not "": ForceOpenRouter
	// treats any set value other than 1/true — including the empty string —
	// as the kill switch.
	t.Setenv("POLARIS_NOVA_LOOP", "1")
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/providers/status"))
	if err != nil {
		t.Fatalf("GET providers/status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body ProvidersStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding status: %v", err)
	}

	if !body.OpenRouter.Configured {
		t.Error("openrouter.configured = false, want true (test config keys it)")
	}
	if body.OpenCodeGo.Configured {
		t.Error("opencode_go.configured = true, want false (no key in test config)")
	}
	if body.OpenCodeGo.Error == "" {
		t.Error("opencode_go.error empty, want the not-configured note")
	}
	if body.Policy.DefaultBackend != "openrouter" {
		t.Errorf("policy.default_backend = %q, want openrouter (legacy default)", body.Policy.DefaultBackend)
	}
	if body.Policy.Pinned {
		t.Error("policy.pinned = true, want false (no kill switch in test env)")
	}
	if len(body.Models) == 0 {
		t.Fatal("models table empty, want the full registry")
	}
	byID := map[string]ProvidersModelStatus{}
	for _, m := range body.Models {
		byID[m.ID] = m
	}
	deepseek, ok := byID["deepseek"]
	if !ok {
		t.Fatal("models table has no deepseek entry")
	}
	if deepseek.Primary != "openrouter" {
		t.Errorf("deepseek.primary = %q, want openrouter (dark default: no Go routes)", deepseek.Primary)
	}
	if deepseek.WireModel != "deepseek/deepseek-v4.1-flash-1017" {
		t.Errorf("deepseek.wire_model = %q, want the OpenRouter slug", deepseek.WireModel)
	}
	if deepseek.GoMonthlyLimitUSD != 100 {
		t.Errorf("deepseek.go_monthly_limit_usd = %v, want 100 (Nova Go baseline)", deepseek.GoMonthlyLimitUSD)
	}
}
