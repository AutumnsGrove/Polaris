package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogEvent_GlobalAndThreadScoped(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "m"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	s.LogEvent("", "info", "startup", "server started", map[string]interface{}{"dev": false})
	s.LogEvent("t1", "info", "turn", "turn started", map[string]interface{}{"model": "test-model"})
	s.LogEvent("t1", "error", "turn", "turn failed", map[string]interface{}{"err": "boom"})

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
	s.LogEvent("", "info", "startup", "server started", nil)

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
	s.LogEvent("", "info", "tool.web_read", "tool call finished", map[string]interface{}{"result": huge})

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

func TestPruneEvents_RemovesOnlyOldEvents(t *testing.T) {
	s := openTestStore(t)
	s.LogEvent("", "info", "startup", "recent event", nil)

	// Backdate a second event directly, since LogEvent always stamps "now".
	if _, err := s.db.Exec(
		`INSERT INTO events (thread_id, level, source, message, data, created_at) VALUES (NULL, 'info', 'startup', 'old event', '{}', ?)`,
		time.Now().AddDate(0, 0, -100).Format("2006-01-02 15:04:05"),
	); err != nil {
		t.Fatalf("inserting backdated event: %v", err)
	}

	before, err := s.ListRecentEvents(0)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("got %d events before prune, want 2", len(before))
	}

	if err := s.PruneEvents(90); err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}

	after, err := s.ListRecentEvents(0)
	if err != nil {
		t.Fatalf("ListRecentEvents: %v", err)
	}
	if len(after) != 1 || after[0].Message != "recent event" {
		t.Errorf("after prune = %+v, want only \"recent event\" to survive", after)
	}
}
