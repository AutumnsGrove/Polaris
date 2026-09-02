package tools

import "testing"

// TestResearchBudget_AllowedUnderHardCeiling covers the common case: well
// under the hard ceiling, every call is allowed.
func TestResearchBudget_AllowedUnderHardCeiling(t *testing.T) {
	b := NewResearchBudget()
	for i := 0; i < 10; i++ {
		if !b.Allowed() {
			t.Fatalf("Allowed() = false after %d calls, want true (under hard ceiling)", i)
		}
		b.RecordCall(false)
	}
}

// TestResearchBudget_BlocksAtHardCeiling covers the circuit-breaker case
// from docs/plans/deep-research-two-tier.md's "Budget (session-level)"
// section — the hard ceiling (~150, researchBudgetHardCeiling) is meant
// to be a rare backstop, not the normal stopping point, but it must
// actually stop calls once crossed.
func TestResearchBudget_BlocksAtHardCeiling(t *testing.T) {
	b := NewResearchBudget()
	for i := 0; i < researchBudgetHardCeiling; i++ {
		if !b.Allowed() {
			t.Fatalf("Allowed() = false after %d calls, want true (still under ceiling)", i)
		}
		b.RecordCall(false)
	}
	if b.Allowed() {
		t.Error("Allowed() = true at the hard ceiling, want false")
	}
}

// TestResearchBudget_SoftNudgeFiresOnceAtTotalThreshold covers the normal
// (SearXNG-only, no paid fallback) soft-nudge path — fires exactly once,
// on the call that crosses researchBudgetSoftThreshold, not on every call
// after.
func TestResearchBudget_SoftNudgeFiresOnceAtTotalThreshold(t *testing.T) {
	b := NewResearchBudget()
	fired := 0
	for i := 0; i < researchBudgetSoftThreshold+5; i++ {
		if b.RecordCall(false) {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("soft nudge fired %d times, want exactly 1", fired)
	}
}

// TestResearchBudget_SoftNudgeFiresEarlierUnderFallback covers the
// provider-aware soft threshold — the doc calls out that fallback usage
// (Brave/Parallel/Tavily, once SearXNG has degraded) is "the actual
// expensive scenario," so the nudge should fire at a materially lower
// call count than the SearXNG-only case once fallback calls are present.
func TestResearchBudget_SoftNudgeFiresEarlierUnderFallback(t *testing.T) {
	b := NewResearchBudget()
	calls := 0
	fired := false
	for i := 0; i < researchBudgetSoftThreshold; i++ {
		calls++
		if b.RecordCall(true) {
			fired = true
			break
		}
	}
	if !fired {
		t.Fatal("soft nudge never fired under sustained fallback usage before reaching the SearXNG-only threshold")
	}
	if calls >= researchBudgetSoftThreshold {
		t.Errorf("soft nudge fired at %d fallback calls, want earlier than researchBudgetSoftThreshold (%d)", calls, researchBudgetSoftThreshold)
	}
}

// TestResearchBudget_Summary confirms Summary reports both counters
// independently — the check-in message needs "N total calls, M against
// paid fallback" (see the doc's nudge wording), not just a combined total.
func TestResearchBudget_Summary(t *testing.T) {
	b := NewResearchBudget()
	b.RecordCall(false)
	b.RecordCall(true)
	b.RecordCall(true)
	total, fallback := b.Summary()
	if total != 3 {
		t.Errorf("Summary() total = %d, want 3", total)
	}
	if fallback != 2 {
		t.Errorf("Summary() fallback = %d, want 2", fallback)
	}
}

// TestResearchBudget_ConcurrentCallsAreCounted exercises the concurrency
// use case ResearchBudget actually exists for — multiple sub-agent
// goroutines sharing one budget (see docs/plans/deep-research-two-tier.md:
// "a small struct passed into each sub-agent's tools.Context"). A data
// race here would be caught by `go test -race`.
func TestResearchBudget_ConcurrentCallsAreCounted(t *testing.T) {
	b := NewResearchBudget()
	const goroutines = 20
	const callsEach = 5
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < callsEach; j++ {
				if b.Allowed() {
					b.RecordCall(false)
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	total, _ := b.Summary()
	if total != goroutines*callsEach {
		t.Errorf("Summary() total = %d, want %d", total, goroutines*callsEach)
	}
}
