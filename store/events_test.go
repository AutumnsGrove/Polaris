package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogEvent_GlobalAndThreadScoped(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	s.LogEvent("", "info", "startup", "server started", map[string]interface{}{"dev": false}, "")
	s.LogEvent("t1", "info", "turn", "turn started", map[string]interface{}{"model": "test-model"}, "")
	s.LogEvent("t1", "error", "turn", "turn failed", map[string]interface{}{"err": "boom"}, "")

	threadEvents, err := s.ListEvents("t1", 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(threadEvents) != 2 {
		t.Fatalf("got %d thread events, want 2 (global event excluded)", len(threadEvents))
	}
	if threadEvents[0].Message != "turn started" || threadEvents[1].Message != "turn failed" {
		t.Errorf("events out of order: %+v", threadEvents)
	}
	if threadEvents[1].Level != "error" {
		t.Errorf("Level = %q, want error", threadEvents[1].Level)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(threadEvents[0].Data), &data); err != nil {
		t.Fatalf("Data not valid JSON: %v", err)
	}
	if data["model"] != "test-model" {
		t.Errorf("Data[model] = %v, want test-model", data["model"])
	}

	recent, err := s.ListRecentEvents(0)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d recent events, want 3 (global + both thread events)", len(recent))
	}
	// Newest first.
	if recent[0].Message != "turn failed" {
		t.Errorf("recent[0] = %+v, want the most recently logged event first", recent[0])
	}
	if recent[2].ThreadID != "" {
		t.Errorf("oldest recent event ThreadID = %q, want empty (the global startup event)", recent[2].ThreadID)
	}
}

func TestLogEvent_NilDataProducesEmptyObject(t *testing.T) {
	s := openTestStore(t)
	s.LogEvent("", "info", "startup", "server started", nil, "")

	events, err := s.ListRecentEvents(0)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(events) != 1 || events[0].Data != "{}" {
		t.Errorf("events = %+v, want a single event with Data \"{}\"", events)
	}
}

func TestLogEvent_TruncatesOversizedStringFields(t *testing.T) {
	s := openTestStore(t)
	huge := strings.Repeat("x", maxEventDataBytes+500)
	s.LogEvent("", "info", "tool.web_read", "tool call finished", map[string]interface{}{"result": huge}, "")

	events, err := s.ListRecentEvents(0)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("Data not valid JSON: %v", err)
	}
	result, _ := data["result"].(string)
	if len(result) >= len(huge) {
		t.Errorf("result length = %d, want it truncated below the original %d", len(result), len(huge))
	}
	if !strings.HasSuffix(result, "[truncated]") {
		t.Errorf("truncated result should end with a [truncated] marker, got suffix %q", result[max(0, len(result)-20):])
	}
}

// Events are never pruned on a timer (see the "no PruneEvents" note in
// events.go) — they live exactly as long as their thread. The UI's delete is
// a soft delete, which must leave the trail intact; a hard delete (the ghost
// cleanup path) is the only thing that cascades events away with the row.
func TestEventsSurviveSoftDeleteAndCascadeOnHardDelete(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("kept", "Kept", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := s.CreateGhostThread("ghost", "", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	s.LogEvent("kept", "info", "turn", "reasoning", map[string]interface{}{"content": "kept thinking"}, "turn-1")
	s.LogEvent("ghost", "info", "turn", "reasoning", map[string]interface{}{"content": "ghost thinking"}, "turn-2")

	if err := s.DeleteThread("kept"); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	kept, err := s.ListEvents("kept", 0)
	if err != nil {
		t.Fatalf("ListEvents after soft delete: %v", err)
	}
	if len(kept) != 1 {
		t.Errorf("got %d events for a soft-deleted thread, want 1 — a soft delete must not drop the audit trail", len(kept))
	}

	if _, err := s.DeleteAllGhostThreads(); err != nil {
		t.Fatalf("DeleteAllGhostThreads: %v", err)
	}
	gone, err := s.ListEvents("ghost", 0)
	if err != nil {
		t.Fatalf("ListEvents after hard delete: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("got %d events for a hard-deleted thread, want 0 (events.thread_id should cascade)", len(gone))
	}
}
