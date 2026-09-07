package store

import (
	"strconv"
	"testing"
	"time"
)

// TestGetStats_AvgTurnDurationHandlesFractionalAverage guards against a
// real bug caught by live end-to-end testing (a natural-language request
// against the running server, not just canned test data): SQLite's AVG()
// always returns a real number, even over an all-integer column, so
// scanning it straight into an int64 field failed outright the moment
// more than one turn's durations didn't average to a whole number —
// which every other test in this file's use of a single row or uniform
// durations never happened to exercise. 5000 and 3021 are chosen
// specifically because their average isn't whole.
func TestGetStats_AvgTurnDurationHandlesFractionalAverage(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	for i, durationMs := range []int64{5000, 3021} {
		turnID := "turn-" + strconv.Itoa(i)
		if _, err := s.AddMessage("t1", "user", "hi", "[]", "[]", 0, turnID); err != nil {
			t.Fatalf("AddMessage (user): %v", err)
		}
		assistantID, err := s.AddMessage("t1", "assistant", "hi back", "[]", "[]", 0, turnID)
		if err != nil {
			t.Fatalf("AddMessage (assistant): %v", err)
		}
		if err := s.SetMessageDuration(assistantID, durationMs); err != nil {
			t.Fatalf("SetMessageDuration: %v", err)
		}
	}

	stats, err := s.GetStats(0)
	if err != nil {
		t.Fatalf("GetStats returned error (this is the bug — a fractional AVG() failing an int64 Scan): %v", err)
	}
	// (5000 + 3021) / 2 = 4010.5, truncated to 4010.
	if stats.AvgTurnDurationMs != 4010 {
		t.Errorf("AvgTurnDurationMs = %d, want 4010", stats.AvgTurnDurationMs)
	}
}

// TestGetStats_CostBySource covers the three-way split (Polaris/Pulsar/
// Daily) — Polaris and Pulsar are distinguished by threads.source, Daily
// is a wholly separate cost path (pulsar_daily_editions) that never
// touches threads/messages at all, previously invisible in either total.
func TestGetStats_CostBySource(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("web-thread", "Chat", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread (web): %v", err)
	}
	if _, err := s.AddMessage("web-thread", "assistant", "hi", "[]", "[]", 0.01, "turn-1"); err != nil {
		t.Fatalf("AddMessage (web): %v", err)
	}

	if err := s.CreateThread("pulsar-thread", "Pulse", "test-model", "pulsar"); err != nil {
		t.Fatalf("CreateThread (pulsar): %v", err)
	}
	if _, err := s.AddMessage("pulsar-thread", "assistant", "hi", "[]", "[]", 0.02, "turn-2"); err != nil {
		t.Fatalf("AddMessage (pulsar): %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	if err := s.UpsertDailyEdition(today, []PulsarDailyBlock{{Key: "quote", Title: "Quote", Content: "x"}}, 0.03); err != nil {
		t.Fatalf("UpsertDailyEdition: %v", err)
	}

	stats, err := s.GetStats(0)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.CostBySource.Polaris.TotalCostUSD != 0.01 {
		t.Errorf("Polaris.TotalCostUSD = %v, want 0.01", stats.CostBySource.Polaris.TotalCostUSD)
	}
	if stats.CostBySource.Pulsar.TotalCostUSD != 0.02 {
		t.Errorf("Pulsar.TotalCostUSD = %v, want 0.02", stats.CostBySource.Pulsar.TotalCostUSD)
	}
	if stats.CostBySource.Daily.TotalCostUSD != 0.03 {
		t.Errorf("Daily.TotalCostUSD = %v, want 0.03", stats.CostBySource.Daily.TotalCostUSD)
	}
	// Polaris + Pulsar must sum back to the plain (unsplit) total, not a
	// second, subtly different number computed a different way.
	if got, want := stats.CostBySource.Polaris.TotalCostUSD+stats.CostBySource.Pulsar.TotalCostUSD, stats.TotalCostUSD; got != want {
		t.Errorf("Polaris+Pulsar = %v, want stats.TotalCostUSD = %v", got, want)
	}
	if stats.CostBySource.Polaris.PeriodCostUSD != 0.01 || stats.CostBySource.Pulsar.PeriodCostUSD != 0.02 {
		t.Errorf("period costs = %+v, want 0.01/0.02", stats.CostBySource)
	}
	if stats.CostBySource.Daily.PeriodCostUSD != 0.03 {
		t.Errorf("Daily.PeriodCostUSD = %v, want 0.03 (today's edition, within a 30-day window)", stats.CostBySource.Daily.PeriodCostUSD)
	}
}

// TestGetStats_SearchProviderCounts guards against conflating this with
// api_usage's billing-cap counters (see Stats.SearchProviderCounts' doc
// comment) — only "tool call finished" events on tool.web_search with a
// non-empty provider count, and different providers tally independently.
func TestGetStats_SearchProviderCounts(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	s.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": "...", "provider": "searxng"}, "turn-1")
	s.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": "...", "provider": "brave"}, "turn-2")
	s.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": "...", "provider": "brave"}, "turn-3")
	// A degraded/no-results/error tool_result carries no provider at all
	// (formatSearchResults is never reached) — must not show up as some
	// empty-string bucket.
	s.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": "no results"}, "turn-4")
	// A different tool's own "tool call finished" event must never be
	// mistaken for a web_search provider hit even if it happened to carry
	// a same-named field.
	s.LogEvent("t1", "info", "tool.web_read", "tool call finished", map[string]interface{}{"result": "...", "provider": "brave"}, "turn-5")

	stats, err := s.GetStats(0)
	if err != nil {
		t.Fatalf("GetStats returned error: %v", err)
	}
	want := map[string]int{"searxng": 1, "brave": 2}
	if len(stats.SearchProviderCounts) != len(want) {
		t.Fatalf("SearchProviderCounts = %+v, want %+v", stats.SearchProviderCounts, want)
	}
	for provider, count := range want {
		if stats.SearchProviderCounts[provider] != count {
			t.Errorf("SearchProviderCounts[%q] = %d, want %d", provider, stats.SearchProviderCounts[provider], count)
		}
	}
}

func TestGetStats_ChartKindCounts(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	s.LogEvent("t1", "info", "tool.visualize", "tool call finished", map[string]interface{}{"result": "...", "chart_kind": "line"}, "turn-1")
	s.LogEvent("t1", "info", "tool.visualize", "tool call finished", map[string]interface{}{"result": "...", "chart_kind": "meter"}, "turn-2")
	s.LogEvent("t1", "info", "tool.visualize", "tool call finished", map[string]interface{}{"result": "...", "chart_kind": "meter"}, "turn-3")
	// A rejected call (cap exceeded, bad kind) never reaches ctx.SetChart,
	// so its tool_result carries no chart_kind at all — must not show up
	// as some empty-string bucket.
	s.LogEvent("t1", "warn", "tool.visualize", "tool call finished", map[string]interface{}{"result": "error: too many points"}, "turn-4")
	// weather's own Tier-1 auto-chart is deliberately excluded — its kind
	// is never a model decision (see ChartKindCounts's doc comment).
	s.LogEvent("t1", "info", "tool.weather", "tool call finished", map[string]interface{}{"result": "...", "chart_kind": "line"}, "turn-5")

	stats, err := s.GetStats(0)
	if err != nil {
		t.Fatalf("GetStats returned error: %v", err)
	}
	want := map[string]int{"line": 1, "meter": 2}
	if len(stats.ChartKindCounts) != len(want) {
		t.Fatalf("ChartKindCounts = %+v, want %+v", stats.ChartKindCounts, want)
	}
	for kind, count := range want {
		if stats.ChartKindCounts[kind] != count {
			t.Errorf("ChartKindCounts[%q] = %d, want %d", kind, stats.ChartKindCounts[kind], count)
		}
	}
}
