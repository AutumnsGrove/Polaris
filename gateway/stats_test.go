package gateway

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"polaris/brave"
	"polaris/tools"
)

func TestStats_APICapsReportRealUsageAgainstEnforcedCaps(t *testing.T) {
	h := newTestHarness(t, "")
	for i := 0; i < 3; i++ {
		if _, err := h.db.IncrementAPIUsage("tavily"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.IncrementAPIUsage("brave"); err != nil {
		t.Fatal(err)
	}
	if err := h.db.LogJevCost(1.25); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(h.url("/api/stats"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	// The wrapper must not have nested the pre-existing fields: the
	// settings panel and older CLIs read these at the top level.
	for _, key := range []string{"total_cost_usd", "tool_call_counts", "api_caps"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response is missing top-level %q", key)
		}
	}

	var caps APICaps
	if err := json.Unmarshal(raw["api_caps"], &caps); err != nil {
		t.Fatal(err)
	}
	if want := time.Now().UTC().Format("2006-01"); caps.Month != want {
		t.Errorf("month = %q, want %q", caps.Month, want)
	}
	if _, err := time.Parse(time.RFC3339, caps.ResetsAt); err != nil {
		t.Errorf("resets_at %q isn't RFC 3339: %v", caps.ResetsAt, err)
	}

	byProvider := map[string]APICapItem{}
	for _, svc := range caps.Services {
		byProvider[svc.Provider] = svc
	}
	cases := []struct {
		provider string
		unit     string
		used     float64
		cap      float64
	}{
		{"brave", "calls", 1, brave.MonthlyCap},
		{"parallel", "calls", 0, tools.ParallelMonthlyCap},
		{"tavily", "calls", 3, tools.TavilyMonthlyCap},
		{"jev", "usd", 1.25, jevMonthlyCapUSD},
	}
	if len(byProvider) != len(cases) {
		t.Errorf("got %d services, want %d: %+v", len(byProvider), len(cases), caps.Services)
	}
	for _, c := range cases {
		got, ok := byProvider[c.provider]
		if !ok {
			t.Errorf("%s missing from api_caps", c.provider)
			continue
		}
		if got.Unit != c.unit || got.Used != c.used || got.Cap != c.cap {
			t.Errorf("%s = %+v, want unit %s used %v cap %v", c.provider, got, c.unit, c.used, c.cap)
		}
		if math.Abs(got.Remaining-(c.cap-c.used)) > 1e-9 || math.Abs(got.PercentUsed-c.used/c.cap*100) > 1e-9 {
			t.Errorf("%s remaining/percent wrong: %+v", c.provider, got)
		}
	}
}

func TestNewAPICapItem_ClampsRemainingWhenOverCap(t *testing.T) {
	// A burst of concurrent turns can pass the cap check together and
	// overshoot slightly (see parallelMonthlyCap's doc comment); the
	// display must say "0 left", not a negative number.
	item := newAPICapItem("tavily", "calls", 503, 500)
	if item.Remaining != 0 {
		t.Errorf("remaining = %v, want 0", item.Remaining)
	}
	if item.PercentUsed <= 100 {
		t.Errorf("percent = %v, want >100 so an overshoot is visible", item.PercentUsed)
	}
}

func TestBuildAPICaps_ResetsAtRollsOverTheYear(t *testing.T) {
	h := newTestHarness(t, "")
	caps, err := buildAPICaps(h.db, time.Date(2026, 12, 15, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if caps.Month != "2026-12" || caps.ResetsAt != "2027-01-01T00:00:00Z" {
		t.Errorf("month %q resets_at %q, want 2026-12 → 2027-01-01T00:00:00Z", caps.Month, caps.ResetsAt)
	}
}
