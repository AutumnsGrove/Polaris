package store

import (
	"strings"
	"testing"
)

func TestThreadExportInput_GathersMessagesEventsAndMetadata(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Tea notes", "model-x", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "hi", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	aid, err := s.AddMessage("t1", "assistant", "hello", `[{"title":"T","url":"https://e.com"}]`, "[]", 0.25, "turn-1")
	if err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}
	if err := s.SetMessageTranscript(aid, `[{"role":"user","content":"hi"}]`); err != nil {
		t.Fatalf("SetMessageTranscript: %v", err)
	}
	s.LogEvent("t1", "info", "turn", "reasoning", map[string]interface{}{"content": "think"}, "turn-1")

	in, err := s.ThreadExportInput("t1")
	if err != nil {
		t.Fatalf("ThreadExportInput: %v", err)
	}
	if in.Thread.ID != "t1" || in.Thread.Title != "Tea notes" || in.Thread.Model != "model-x" {
		t.Errorf("thread metadata = %+v", in.Thread)
	}
	if len(in.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(in.Messages))
	}
	if in.Messages[1].Transcript == "" {
		t.Errorf("assistant transcript was not carried into the export input")
	}
	if !strings.Contains(in.Messages[1].Citations, "e.com") {
		t.Errorf("citations not carried: %q", in.Messages[1].Citations)
	}
	if len(in.Events) != 1 || in.Events[0].Message != "reasoning" || in.Events[0].TurnID != "turn-1" {
		t.Errorf("events = %+v, want the one reasoning row", in.Events)
	}
}

// TestThreadExportInput_UsesActiveVariantPathWithRootMetadata pins the
// "export the selected path only" decision: after swapping to a fork, the
// messages come from the fork while the title stays the root's (a fork's
// title is blank by construction).
func TestThreadExportInput_UsesActiveVariantPathWithRootMetadata(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Root Title", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "q", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "root answer", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(root assistant): %v", err)
	}
	fork, err := s.ForkThread("t1", "t1", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := s.AddMessage(fork, "assistant", "fork answer", "[]", "[]", 0, "turn-1b"); err != nil {
		t.Fatalf("AddMessage(fork assistant): %v", err)
	}
	if err := s.SetActiveVariant("t1", fork); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}

	in, err := s.ThreadExportInput("t1")
	if err != nil {
		t.Fatalf("ThreadExportInput: %v", err)
	}
	if in.Thread.Title != "Root Title" {
		t.Errorf("title = %q, want the root's title", in.Thread.Title)
	}
	if len(in.Messages) != 2 {
		t.Fatalf("got %d messages, want the fork's 2", len(in.Messages))
	}
	if in.Messages[1].Content != "fork answer" {
		t.Errorf("second message = %q, want the active variant's answer", in.Messages[1].Content)
	}
}

func TestThreadExportInput_RefusesGhostThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g1", "", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := s.ThreadExportInput("g1"); err == nil {
		t.Fatal("want an error for an unpromoted ghost thread, got nil")
	}
}
