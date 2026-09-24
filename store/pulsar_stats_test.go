package store

import "testing"

// TestGetPulsarStats_Aggregates covers the shape GetPulsarStats adds on
// top of GetStats' own patterns: routine counts, pulse count scoped to
// threads.source = 'pulsar', a distinct-thread failed-pulse count (not a
// raw error-event count), and tool call/nudge counts joined through to
// pulsar threads only — a non-pulsar thread's own tool calls/errors/
// nudges must never leak into these numbers.
func TestGetPulsarStats_Aggregates(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreatePulsarRoutine("Daily news", "prompt", "test-model", "default", false, "daily", "", "07:00"); err != nil {
		t.Fatalf("CreatePulsarRoutine (active): %v", err)
	}
	archivedID, err := s.CreatePulsarRoutine("Old routine", "prompt", "test-model", "default", false, "daily", "", "08:00")
	if err != nil {
		t.Fatalf("CreatePulsarRoutine (to archive): %v", err)
	}
	if err := s.ArchivePulsarRoutine(archivedID); err != nil {
		t.Fatalf("ArchivePulsarRoutine: %v", err)
	}

	// A successful pulse: one tool call, one check-in nudge, real cost.
	if err := s.CreateThread("pulse-ok", "Pulse", "test-model", "pulsar"); err != nil {
		t.Fatalf("CreateThread (pulse-ok): %v", err)
	}
	if _, err := s.AddMessage("pulse-ok", "assistant", "hi", "[]", "[]", 0.05, "turn-ok"); err != nil {
		t.Fatalf("AddMessage (pulse-ok): %v", err)
	}
	s.LogEvent("pulse-ok", "info", "tool.web_search", "tool call finished", nil, "turn-ok")
	s.LogEvent("pulse-ok", "info", "agent.nudge", "nudge", map[string]interface{}{"kind": "check_in"}, "turn-ok")

	// A failed pulse: two error events on the same thread (should still
	// count as one failed pulse, not two) and one errored tool call.
	if err := s.CreateThread("pulse-failed", "Pulse", "test-model", "pulsar"); err != nil {
		t.Fatalf("CreateThread (pulse-failed): %v", err)
	}
	s.LogEvent("pulse-failed", "warn", "tool.web_search", "tool call finished", nil, "turn-failed")
	s.LogEvent("pulse-failed", "error", "turn", "turn failed", nil, "turn-failed")
	s.LogEvent("pulse-failed", "error", "turn", "persisting assistant message failed", nil, "turn-failed")

	// A non-pulsar thread with its own tool call, error event, and nudge —
	// none of this should leak into the pulsar-scoped numbers above.
	if err := s.CreateThread("web-thread", "Chat", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread (web-thread): %v", err)
	}
	s.LogEvent("web-thread", "info", "tool.web_search", "tool call finished", nil, "turn-web")
	s.LogEvent("web-thread", "error", "turn", "turn failed", nil, "turn-web")
	s.LogEvent("web-thread", "info", "agent.nudge", "nudge", map[string]interface{}{"kind": "stale_streak"}, "turn-web")

	stats, err := s.GetPulsarStats(0)
	if err != nil {
		t.Fatalf("GetPulsarStats: %v", err)
	}

	if stats.ActiveRoutineCount != 1 {
		t.Errorf("ActiveRoutineCount = %d, want 1", stats.ActiveRoutineCount)
	}
	if stats.ArchivedRoutineCount != 1 {
		t.Errorf("ArchivedRoutineCount = %d, want 1", stats.ArchivedRoutineCount)
	}
	if stats.PulseCount != 2 {
		t.Errorf("PulseCount = %d, want 2", stats.PulseCount)
	}
	if stats.FailedPulseCount != 1 {
		t.Errorf("FailedPulseCount = %d, want 1 (two error events on one thread must count once)", stats.FailedPulseCount)
	}
	if stats.TotalCostUSD != 0.05 {
		t.Errorf("TotalCostUSD = %v, want 0.05 (the web thread's cost must not leak in)", stats.TotalCostUSD)
	}
	if got := stats.ToolCallCounts["web_search"]; got != 2 {
		t.Errorf("ToolCallCounts[web_search] = %d, want 2 (pulsar threads only)", got)
	}
	if got := stats.ToolErrorCounts["web_search"]; got != 1 {
		t.Errorf("ToolErrorCounts[web_search] = %d, want 1", got)
	}
	if stats.CheckInCount != 1 {
		t.Errorf("CheckInCount = %d, want 1 (the web thread's stale_streak nudge must not count here)", stats.CheckInCount)
	}
	if stats.StaleStreakCount != 0 {
		t.Errorf("StaleStreakCount = %d, want 0", stats.StaleStreakCount)
	}
}
