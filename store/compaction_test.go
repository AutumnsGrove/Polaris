package store

import (
	"testing"
)

func TestCompactThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "some answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.CompactThread("t1", "a concise summary", msgID, 0.003, 42); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CompactedSummary != "a concise summary" {
		t.Errorf("CompactedSummary = %q, want %q", thread.CompactedSummary, "a concise summary")
	}
	if thread.CompactedThroughID != msgID {
		t.Errorf("CompactedThroughID = %d, want %d", thread.CompactedThroughID, msgID)
	}
	if thread.ContextTokens != 42 {
		t.Errorf("ContextTokens = %d, want 42", thread.ContextTokens)
	}
	if thread.CostUSD != 0.003 {
		t.Errorf("CostUSD = %v, want 0.003 (compaction's own cost)", thread.CostUSD)
	}
}

// TestTakeCompactionNotice_ConsumesExactlyOnce pins the handoff between a
// detached compaction and the turn that announces it. The bug this
// guards is a cost bug, not a display one: the frontend adds the notice's
// cost_usd to its running session total, so a notice that could be taken
// twice would charge the same summarization call twice — and the second
// take would be a turn that has no compaction to report.
func TestTakeCompactionNotice_ConsumesExactlyOnce(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "an answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.CompactThread("t1", "a concise summary", msgID, 0.004, 42); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	summary, cost, ok, err := s.TakeCompactionNotice("t1")
	if err != nil {
		t.Fatalf("TakeCompactionNotice: %v", err)
	}
	if !ok {
		t.Fatal("ok = false on a just-compacted thread, want true")
	}
	if summary != "a concise summary" {
		t.Errorf("summary = %q, want %q", summary, "a concise summary")
	}
	if cost != 0.004 {
		t.Errorf("cost = %v, want 0.004", cost)
	}

	// The whole point: a second turn must find nothing to announce.
	if _, _, ok, err := s.TakeCompactionNotice("t1"); err != nil {
		t.Fatalf("second TakeCompactionNotice: %v", err)
	} else if ok {
		t.Error("ok = true on the second take, want false — a notice must not be announced twice")
	}
}

// TestTakeCompactionNotice_NothingPending covers the overwhelmingly common
// case: a turn on a thread that simply hasn't been compacted. ok=false
// must be a quiet, ordinary answer — not an error — since this runs at the
// top of every single turn.
func TestTakeCompactionNotice_NothingPending(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	summary, cost, ok, err := s.TakeCompactionNotice("t1")
	if err != nil {
		t.Fatalf("TakeCompactionNotice on an uncompacted thread returned error: %v", err)
	}
	if ok || summary != "" || cost != 0 {
		t.Errorf("got (%q, %v, %v), want (\"\", 0, false)", summary, cost, ok)
	}

	// A thread row that has gone away mid-turn (deleted while the turn ran)
	// is the same quiet answer — there is no notice to give, and failing the
	// turn the user is watching over a missing notice would be absurd.
	if _, _, ok, err := s.TakeCompactionNotice("no-such-thread"); err != nil {
		t.Errorf("TakeCompactionNotice on a missing thread returned error: %v", err)
	} else if ok {
		t.Error("ok = true for a thread that does not exist")
	}
}

// TestCompactThread_PendingCostAccumulates covers the case the in-flight
// guard in gateway/server.go makes rare but does not make impossible: two
// compactions landing before any turn collects the notice. Both costs must
// still reach the session total — accumulating rather than overwriting is
// what makes the second one a lag instead of money that silently vanishes.
func TestCompactThread_PendingCostAccumulates(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "an answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.CompactThread("t1", "first summary", msgID, 0.002, 10); err != nil {
		t.Fatalf("first CompactThread: %v", err)
	}
	if err := s.CompactThread("t1", "second summary", msgID, 0.003, 10); err != nil {
		t.Fatalf("second CompactThread: %v", err)
	}

	summary, cost, ok, err := s.TakeCompactionNotice("t1")
	if err != nil {
		t.Fatalf("TakeCompactionNotice: %v", err)
	}
	if !ok {
		t.Fatal("ok = false after two compactions, want true")
	}
	if cost != 0.005 {
		t.Errorf("cost = %v, want 0.005 (both compactions' costs summed)", cost)
	}
	// The summary is the current one, not the first — the notice announces
	// what the thread's history actually is now, not what it was.
	if summary != "second summary" {
		t.Errorf("summary = %q, want the latest, %q", summary, "second summary")
	}
}

// TestForkThread_DoesNotInheritCompactionState pins the deliberate
// rejection in ForkThread: an edit/retry variant rebuilds history from the
// raw messages it copied rather than inheriting the root's summary, and —
// the part that actually has teeth — must not inherit the pending notice.
// A copied notice would surface a summary describing the ROOT's prefix
// under an unrelated variant, and charge its cost to a session that never
// triggered the compaction.
func TestForkThread_DoesNotInheritCompactionState(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "an answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.CompactThread("t1", "root's summary", msgID, 0.004, 42); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	forkID, err := s.ForkThread("t1", "t1", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}

	fork, err := s.GetThreadRaw(forkID)
	if err != nil {
		t.Fatalf("GetThreadRaw: %v", err)
	}
	if fork.CompactedSummary != "" || fork.CompactedThroughID != 0 {
		t.Errorf("fork = %+v, want no inherited compaction", fork)
	}
	if _, _, ok, err := s.TakeCompactionNotice(forkID); err != nil {
		t.Fatalf("TakeCompactionNotice on the fork: %v", err)
	} else if ok {
		t.Error("ok = true on the fork — the root's pending notice must not be inherited")
	}

	// The root keeps its own, untouched: forking a thread must not consume
	// a notice the root's next turn still needs to show.
	if _, cost, ok, err := s.TakeCompactionNotice("t1"); err != nil {
		t.Fatalf("TakeCompactionNotice on the root: %v", err)
	} else if !ok || cost != 0.004 {
		t.Errorf("root notice = (ok %v, cost %v), want it intact", ok, cost)
	}
}
