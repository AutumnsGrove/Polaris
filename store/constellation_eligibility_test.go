package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestEligibleConstellationThreads_Gates(t *testing.T) {
	s := openTestStore(t)

	// A thread that's never been processed and is old enough to be idle
	// — eligible via the delta gate (no prior run at all).
	neverProcessed := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, neverProcessed); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	// A thread still "mid-thought" (a message from seconds ago) — must be
	// excluded by the idle-timing gate regardless of everything else.
	midThought := seedThread(t, s)

	// A thread already fully processed with nothing new since — must be
	// excluded (fails the delta gate, no retry needed).
	upToDate := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, upToDate); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	msgs, err := s.GetMessages(upToDate)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	runID, err := s.StartShootingStarRun(upToDate, msgs[len(msgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(runID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	// A thread that failed once but has since succeeded — the bug this
	// test exists to catch: an EXISTS-any-row check on needs_retry would
	// wrongly keep flagging this eligible forever using the stale failed
	// row, instead of only the most recent run's needs_retry value.
	failedThenSucceeded := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, failedThenSucceeded); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	fsMsgs, err := s.GetMessages(failedThenSucceeded)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	failedRunID, err := s.StartShootingStarRun(failedThenSucceeded, fsMsgs[len(fsMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(failedRunID, "", "some error", true); err != nil {
		t.Fatalf("FinishShootingStarRun (failure): %v", err)
	}
	retryRunID, err := s.StartShootingStarRun(failedThenSucceeded, fsMsgs[len(fsMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun (retry): %v", err)
	}
	if err := s.FinishShootingStarRun(retryRunID, "done on retry", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun (retry succeeded): %v", err)
	}

	// A thread that's currently needing retry — eligible unconditionally
	// via the retry gate, regardless of the delta gate.
	needsRetry := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, needsRetry); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	nrMsgs, err := s.GetMessages(needsRetry)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	nrRunID, err := s.StartShootingStarRun(needsRetry, nrMsgs[len(nrMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(nrRunID, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun (needs retry): %v", err)
	}

	// A disabled thread, otherwise eligible by every other gate — excluded
	// outright.
	disabled := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, disabled); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	if err := s.DeleteThread(disabled); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	// A pulsar-sourced thread (a routine's pulse history), otherwise
	// eligible by every other gate — excluded outright, same as
	// ListThreads' own source != 'pulsar' filter. Weaver should never mine
	// Constellation's own scheduled-pulse output for new stars.
	pulsar := uuid.NewString()
	if err := s.CreateThread(pulsar, "Test pulse", "deepseek", "pulsar"); err != nil {
		t.Fatalf("CreateThread (pulsar): %v", err)
	}
	if _, err := s.AddMessage(pulsar, "user", "some pulse content", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage (pulsar): %v", err)
	}
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, pulsar); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	got, err := s.EligibleConstellationThreads(60)
	if err != nil {
		t.Fatalf("EligibleConstellationThreads: %v", err)
	}

	want := map[string]bool{neverProcessed: true, needsRetry: true}
	gotSet := map[string]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	for id := range want {
		if !gotSet[id] {
			t.Errorf("EligibleConstellationThreads missing expected thread %q; got %v", id, got)
		}
	}
	for _, excluded := range []string{midThought, upToDate, failedThenSucceeded, disabled, pulsar} {
		if gotSet[excluded] {
			t.Errorf("EligibleConstellationThreads wrongly included %q; got %v", excluded, got)
		}
	}
}

// TestEligibleConstellationThreads_ResolvesForkedThreadToRoot covers a real
// bug found by inspecting a production database: editing/regenerating a
// message forks a hidden variant thread (fork_root_id set, its own title
// always "" — see ForkThread's doc comment), and this function used to
// return that raw variant id directly. Weaver then linked stars to it via
// star_sources, and since a variant is never independently addressable
// (GetThread can't open one, nothing lists it), every such star's "source"
// permanently showed as a broken/untitled thread in the UI. This must
// return the stable root id instead — same join-through-root fix
// SearchMessages already uses for the identical class of problem.
func TestEligibleConstellationThreads_ResolvesForkedThreadToRoot(t *testing.T) {
	s := openTestStore(t)

	root := seedThread(t, s)
	forkID, err := s.ForkThread(root, root, 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := s.AddMessage(forkID, "assistant", "edited reply", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.SetActiveVariant(root, forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}
	// Idle-gate: back-date the variant's messages (its own row, not root's
	// — the whole point is root's own messages table is stale/irrelevant
	// once a variant becomes active).
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, forkID); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	got, err := s.EligibleConstellationThreads(60)
	if err != nil {
		t.Fatalf("EligibleConstellationThreads: %v", err)
	}

	if len(got) != 1 || got[0] != root {
		t.Errorf("EligibleConstellationThreads = %v, want exactly [%q] (the root, not the hidden fork %q)", got, root, forkID)
	}
}
