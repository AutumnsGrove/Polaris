package store

import (
	"testing"
)

func TestGetAPIUsage_ZeroForNeverUsedProvider(t *testing.T) {
	s := openTestStore(t)
	count, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 for a provider that's never been recorded", count)
	}
}

func TestIncrementAPIUsage_CountsUpWithinTheSameMonth(t *testing.T) {
	s := openTestStore(t)
	for i := 1; i <= 3; i++ {
		count, err := s.IncrementAPIUsage("parallel")
		if err != nil {
			t.Fatalf("IncrementAPIUsage (call %d): %v", i, err)
		}
		if count != i {
			t.Errorf("IncrementAPIUsage call %d returned %d, want %d", i, count, i)
		}
	}
	stored, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage: %v", err)
	}
	if stored != 3 {
		t.Errorf("GetAPIUsage = %d, want 3", stored)
	}
}

func TestAPIUsage_IsPerProvider(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.IncrementAPIUsage("parallel"); err != nil {
		t.Fatalf("IncrementAPIUsage(parallel): %v", err)
	}
	if _, err := s.IncrementAPIUsage("parallel"); err != nil {
		t.Fatalf("IncrementAPIUsage(parallel): %v", err)
	}
	if _, err := s.IncrementAPIUsage("tavily"); err != nil {
		t.Fatalf("IncrementAPIUsage(tavily): %v", err)
	}

	parallelCount, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage(parallel): %v", err)
	}
	if parallelCount != 2 {
		t.Errorf("GetAPIUsage(parallel) = %d, want 2", parallelCount)
	}

	tavilyCount, err := s.GetAPIUsage("tavily")
	if err != nil {
		t.Fatalf("GetAPIUsage(tavily): %v", err)
	}
	if tavilyCount != 1 {
		t.Errorf("GetAPIUsage(tavily) = %d, want 1 — providers must not share a counter", tavilyCount)
	}
}
