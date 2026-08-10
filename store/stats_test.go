package store

import (
	"strconv"
	"testing"
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
