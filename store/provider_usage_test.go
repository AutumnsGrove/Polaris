package store

import (
	"testing"
)

// The ledger records one row per call that touched Go (allowance or list
// price in the same currency as the totals), and the Stats breakouts slice
// it without double-counting: covered (go/go-plus) and balance are already
// inside TotalCostUSD via messages.cost_usd.
func TestProviderUsage_LedgerAndBreakouts(t *testing.T) {
	s := openTestStore(t)

	if err := s.LogProviderUsage("opencode-go", "deepseek", 0.02, 0.04, "go", 100, 50, 80, false); err != nil {
		t.Fatalf("LogProviderUsage returned error: %v", err)
	}
	if err := s.LogProviderUsage("opencode-go", "deepseek", 0.00, 0.003, "free", 10, 5, 0, false); err != nil {
		t.Fatalf("LogProviderUsage returned error: %v", err)
	}
	if err := s.LogProviderUsage("opencode-go", "deepseek", 0.00, 0.10, "balance", 200, 100, 0, true); err != nil {
		t.Fatalf("LogProviderUsage returned error: %v", err)
	}

	// Polaris's own share of the plan window is allowance dollars for the
	// calendar month — including the spilled row's allowance (0 here).
	if got, err := s.ProviderAllowanceThisMonth(); err != nil {
		t.Fatalf("ProviderAllowanceThisMonth returned error: %v", err)
	} else if got != 0.02 {
		t.Errorf("ProviderAllowanceThisMonth = %v, want 0.02", got)
	}

	stats, err := s.GetStats(30)
	if err != nil {
		t.Fatalf("GetStats returned error: %v", err)
	}
	// Covered counts retail (list_cost_usd), not allowance: 0.04, not 0.02.
	// Free/byok rows keep their own billing_source and stay out.
	if got := stats.CoveredBySubscription.TotalCostUSD; got != 0.04 {
		t.Errorf("CoveredBySubscription.Total = %v, want 0.04", got)
	}
	if got := stats.CoveredBySubscription.PeriodCostUSD; got != 0.04 {
		t.Errorf("CoveredBySubscription.Period = %v, want 0.04", got)
	}
	if got := stats.BilledToBalance.TotalCostUSD; got != 0.10 {
		t.Errorf("BilledToBalance.Total = %v, want 0.10", got)
	}
	// Breakouts, not buckets: the ledger must not inflate the totals.
	if got := stats.TotalCostUSD; got != 0 {
		t.Errorf("TotalCostUSD = %v, want 0 (no messages rows; ledger never adds)", got)
	}
}

func TestSetMessageProvider_RecordsServingBackend(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("thread-1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	id, err := s.AddMessage("thread-1", "assistant", "answer", "[]", "[]", 0.01, "turn-1")
	if err != nil {
		t.Fatalf("AddMessage returned error: %v", err)
	}
	if err := s.SetMessageProvider(id, "opencode-go"); err != nil {
		t.Fatalf("SetMessageProvider returned error: %v", err)
	}
	msgs, err := s.GetMessages("thread-1")
	if err != nil {
		t.Fatalf("GetMessages returned error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("GetMessages returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Provider != "opencode-go" {
		t.Errorf("Provider = %q, want opencode-go", msgs[0].Provider)
	}
	byID, err := s.GetMessageByID(id)
	if err != nil {
		t.Fatalf("GetMessageByID returned error: %v", err)
	}
	if byID.Provider != "opencode-go" {
		t.Errorf("GetMessageByID Provider = %q, want opencode-go", byID.Provider)
	}
}
