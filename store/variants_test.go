package store

import (
	"testing"
)

// TestForkThread_PreservesOldContentAndCopiesSharedPrefix verifies the
// non-destructive edit/retry mechanism: editing/regenerating no longer
// deletes anything (the old DeleteMessagesFromAndAddMessage behavior) —
// the reply being replaced gets forked off into its own thread first, so
// it stays fully intact and reachable via VariantsAt.
func TestForkThread_PreservesOldContentAndCopiesSharedPrefix(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("root", "user", "q1", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a1", "[]", "[]", 0.01, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Regenerating "a1" forks at index 1 (the assistant message's own
	// position) — the fork should end up with just the shared prefix (q1),
	// ready for the caller to add the new reply.
	forkID, err := s.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	forkMsgs, err := s.GetMessages(forkID)
	if err != nil {
		t.Fatalf("GetMessages(fork): %v", err)
	}
	if len(forkMsgs) != 1 || forkMsgs[0].Content != "q1" {
		t.Fatalf("fork messages = %+v, want just the shared prefix [q1]", forkMsgs)
	}

	// root's own original content — the thing being "edited away" —
	// must still be completely untouched.
	rootMsgs, err := s.GetMessages("root")
	if err != nil {
		t.Fatalf("GetMessages(root): %v", err)
	}
	if len(rootMsgs) != 2 || rootMsgs[1].Content != "a1" {
		t.Errorf("root messages = %+v, want the original [q1, a1] still intact", rootMsgs)
	}

	// The fork must show up as a variant at index 1, alongside root's own
	// original content (which still reaches that far).
	variants, err := s.VariantsAt("root", 1)
	if err != nil {
		t.Fatalf("VariantsAt: %v", err)
	}
	if len(variants) != 2 || variants[0] != "root" || variants[1] != forkID {
		t.Errorf("VariantsAt = %v, want [root, %s]", variants, forkID)
	}
}

// TestForkThread_CopiesEventsForSharedPrefix guards against exactly the
// bug this shipped with once already: ForkThread copied messages but not
// their events, so a fork's shared-prefix replies had the right text but
// a silently empty reasoning/tool-call timeline — ListEvents filters by
// thread_id, and those events were still sitting under the source
// thread's id, invisible when queried by the fork's own id.
func TestForkThread_CopiesEventsForSharedPrefix(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("root", "user", "q1", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a1", "[]", "[]", 0.01, "turn-1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	s.LogEvent("root", "info", "tool.web_search", "tool call started", map[string]interface{}{"query": "q1"}, "turn-1")
	s.LogEvent("root", "info", "turn", "reasoning", map[string]interface{}{"content": "thinking about q1"}, "turn-1")

	// A second, later turn whose events must NOT be copied into a fork
	// that only reaches the first turn — proof this isn't just copying
	// srcID's entire event history wholesale.
	if _, err := s.AddMessage("root", "user", "q2", "[]", "[]", 0, "turn-2"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a2", "[]", "[]", 0.01, "turn-2"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	s.LogEvent("root", "info", "turn", "reasoning", map[string]interface{}{"content": "thinking about q2"}, "turn-2")

	// Fork at index 2 — covers turn-1 (q1/a1) only, not turn-2.
	forkID, err := s.ForkThread("root", "root", 2)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}

	events, err := s.ListEvents(forkID, 100)
	if err != nil {
		t.Fatalf("ListEvents(fork): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("fork events = %+v, want exactly turn-1's tool call + reasoning", events)
	}
	for _, e := range events {
		if e.TurnID != "turn-1" {
			t.Errorf("event %+v has turn_id %q, want only turn-1's events copied", e, e.TurnID)
		}
	}

	// root's own events must be completely untouched — ForkThread reads,
	// never mutates, the source.
	rootEvents, err := s.ListEvents("root", 100)
	if err != nil {
		t.Fatalf("ListEvents(root): %v", err)
	}
	if len(rootEvents) != 3 {
		t.Errorf("root events = %+v, want all 3 original events still there", rootEvents)
	}
}

func TestEffectiveThreadID_FollowsSetActiveVariant(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	effective, err := s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != "root" {
		t.Errorf("EffectiveThreadID = %q, want %q (no variant set yet)", effective, "root")
	}

	forkID, err := s.ForkThread("root", "root", 0)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if err := s.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}
	effective, err = s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != forkID {
		t.Errorf("EffectiveThreadID = %q, want the fork %q after browsing to it", effective, forkID)
	}

	// Swapping back to root itself resets it — no lingering pointer.
	if err := s.SetActiveVariant("root", "root"); err != nil {
		t.Fatalf("SetActiveVariant(back to root): %v", err)
	}
	effective, err = s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != "root" {
		t.Errorf("EffectiveThreadID = %q, want %q after switching back", effective, "root")
	}
}

// TestTouchUpdatedAt_KeepsRootRecentAfterVariantSwitch is a regression test
// for the "thread bump-back" symptom: once a thread has ever been edited/
// retried (SetActiveVariant points it at a fork), every later AddMessage
// writes to that fork's own row, not the root's — so without an explicit
// TouchUpdatedAt on the root, ListThreads' recency order silently freezes
// that thread in place even while it's actively being used, letting an
// untouched, older thread outrank it and sit above it in the sidebar.
func TestTouchUpdatedAt_KeepsRootRecentAfterVariantSwitch(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Root", "m", "web"); err != nil {
		t.Fatalf("CreateThread(root): %v", err)
	}
	if _, err := s.AddMessage("root", "user", "hi", "[]", "[]", 0, "turn1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.CreateThread("other", "Other", "m", "web"); err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}

	forkID, err := s.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if err := s.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}

	// Simulate handleTurn continuing the conversation post-edit: it writes
	// to the effective (forked) thread, then explicitly touches the root.
	effective, err := s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if _, err := s.AddMessage(effective, "assistant", "new answer", "[]", "[]", 0, "turn2"); err != nil {
		t.Fatalf("AddMessage(effective): %v", err)
	}
	if err := s.TouchUpdatedAt("root"); err != nil {
		t.Fatalf("TouchUpdatedAt: %v", err)
	}

	threads, err := s.ListThreads(10)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 || threads[0].ID != "root" {
		t.Errorf("threads = %+v, want [root, other] — root should still sort as most recently active", threads)
	}
}
