package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file is a regression suite from investigating the "thread
// bump-back" bug report: every restart overlaps two full OS processes on
// the same SQLite file for up to shutdownGrace (25s) — cmd/run.go's
// db.Close() is deferred to the very end of runRun, AFTER
// WaitForActiveTurns, but httpServer.Shutdown() frees the port immediately,
// so a freshly-started new process can already be serving live requests
// (its own, entirely separate *sql.DB) while the old one is still
// finishing an in-flight turn on the very same file. store.Open's
// SetMaxOpenConns(1)/_busy_timeout=5000 only reasons about goroutines
// *within* one process; nothing in this codebase previously exercised two
// real, independent connections hitting the file at once the way two OS
// processes actually do.
//
// modernc.org/sqlite is a from-scratch, cgo-free reimplementation of
// SQLite (not a binding to the reference C library), and WAL's
// cross-process coordination — shared-memory (-shm) locking, "last
// connection to close checkpoints the WAL" — is exactly the kind of
// subtle, timing-dependent logic a reimplementation could get wrong even
// when everything else checks out. These tests hammer that specific seam
// directly: two (and up to three) independent *Store handles on the same
// file, concurrent writes, one closing mid-stream while the other is
// still live, and continuous reads checking for the exact reported
// symptom — a read that goes BACKWARDS after already having seen newer
// state.
//
// All of them pass cleanly against v1.56.0 (see go.mod) — this is useful
// negative evidence that the driver itself isn't corrupting or losing
// data under cross-process contention, not proof it never could. Kept as
// a permanent suite (not just a one-off investigation) so a future
// modernc.org/sqlite version bump gets re-checked against exactly this
// scenario automatically.

// TestCrossProcessConcurrentAccess simulates the restart-overlap window
// directly: two independent *Store handles on the same file, hammered
// with concurrent writes, checked for lost writes or stale cross-process
// reads.
func TestCrossProcessConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polaris.db")

	procA, err := Open(path) // "old" process
	if err != nil {
		t.Fatalf("open procA: %v", err)
	}
	defer procA.Close()

	procB, err := Open(path) // "new" process, started while procA still alive
	if err != nil {
		t.Fatalf("open procB: %v", err)
	}
	defer procB.Close()

	if err := procA.CreateThread("t1", "Thread", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const n = 200
	var wg sync.WaitGroup
	errsA := make([]error, n)
	errsB := make([]error, n)

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, err := procA.AddMessage("t1", "user", fmt.Sprintf("A-%d", i), "[]", "[]", 0, "")
			errsA[i] = err
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, err := procB.AddMessage("t1", "assistant", fmt.Sprintf("B-%d", i), "[]", "[]", 0, "")
			errsB[i] = err
		}
	}()
	wg.Wait()

	var failedA, failedB int
	for _, e := range errsA {
		if e != nil {
			failedA++
		}
	}
	for _, e := range errsB {
		if e != nil {
			failedB++
		}
	}
	t.Logf("procA write errors: %d/%d, procB write errors: %d/%d", failedA, n, failedB, n)

	// Give WAL a beat, then check both processes agree on the total count
	// — a lost write here (a message that returned no error but never
	// actually landed, or one process's writes invisible to the other's
	// reads) would show up as a mismatch or a wrong total.
	time.Sleep(200 * time.Millisecond)

	msgsFromA, err := procA.GetMessages("t1")
	if err != nil {
		t.Fatalf("procA.GetMessages: %v", err)
	}
	msgsFromB, err := procB.GetMessages("t1")
	if err != nil {
		t.Fatalf("procB.GetMessages: %v", err)
	}
	t.Logf("procA sees %d messages, procB sees %d messages", len(msgsFromA), len(msgsFromB))

	wantTotal := 2*n - failedA - failedB
	if len(msgsFromA) != wantTotal {
		t.Errorf("procA sees %d messages, want %d (2*%d - %d failedA - %d failedB) — lost or duplicated writes under cross-process contention", len(msgsFromA), wantTotal, n, failedA, failedB)
	}
	if len(msgsFromB) != len(msgsFromA) {
		t.Errorf("procA and procB disagree on message count: %d vs %d — stale cross-process read", len(msgsFromA), len(msgsFromB))
	}

	// Open a THIRD handle, fresh, simulating a client hitting the newest
	// process right after the old one's Close() — must see everything.
	procC, err := Open(path)
	if err != nil {
		t.Fatalf("open procC: %v", err)
	}
	defer procC.Close()
	msgsFromC, err := procC.GetMessages("t1")
	if err != nil {
		t.Fatalf("procC.GetMessages: %v", err)
	}
	if len(msgsFromC) != len(msgsFromA) {
		t.Errorf("fresh procC sees %d messages, want %d matching procA/procB — a freshly-opened connection is not seeing the true committed state", len(msgsFromC), len(msgsFromA))
	}
}

// TestCrossProcessUpdatedAtOrdering targets the exact recency-ordering
// path (ListThreads/updated_at) under the same cross-process overlap,
// since that's the concrete mechanism behind the "current thread"
// tracking this bug hunt is about.
func TestCrossProcessUpdatedAtOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polaris.db")

	procA, err := Open(path)
	if err != nil {
		t.Fatalf("open procA: %v", err)
	}
	defer procA.Close()
	procB, err := Open(path)
	if err != nil {
		t.Fatalf("open procB: %v", err)
	}
	defer procB.Close()

	if err := procA.CreateThread("older", "Older", "m", "web"); err != nil {
		t.Fatalf("CreateThread(older): %v", err)
	}
	if err := procA.CreateThread("current", "Current", "m", "web"); err != nil {
		t.Fatalf("CreateThread(current): %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	// procA (the "old" server) is still finishing a turn on "current" —
	// bumps its updated_at right as procB (the "new" server) comes up and
	// independently touches "older" (e.g. a stray background write —
	// title regen, a settings change, anything). Order matters: procA's
	// write happens LAST, so "current" should still end up on top.
	if _, err := procB.AddMessage("older", "user", "hi from new proc", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("procB.AddMessage: %v", err)
	}
	if _, err := procA.AddMessage("current", "assistant", "finishing up from old proc", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("procA.AddMessage: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	for _, p := range []struct {
		name string
		s    *Store
	}{{"procA", procA}, {"procB", procB}} {
		threads, err := p.s.ListThreads(10)
		if err != nil {
			t.Fatalf("%s.ListThreads: %v", p.name, err)
		}
		ids := make([]string, len(threads))
		for i, th := range threads {
			ids[i] = th.ID
		}
		t.Logf("%s sees order: %v", p.name, ids)
		if len(ids) < 1 || ids[0] != "current" {
			t.Errorf("%s: order = %v, want [current, older] — the thread that was actually written to LAST (from the OTHER process) isn't sorting first", p.name, ids)
		}
	}
}

// TestCrossProcessCloseRace targets the specific moment production hits on
// every restart: the OLD process's db.Close() firing WHILE the NEW
// process — already bound to the port and serving live traffic — is
// actively reading/writing the same file. SQLite's "last connection to
// close checkpoints WAL into the main file" behavior is exactly the kind
// of thing a from-scratch reimplementation could get subtly wrong under
// real cross-process contention, even if isolated concurrent writes (see
// TestCrossProcessConcurrentAccess) look fine. Looped many times since a
// race like this is inherently timing-dependent.
func TestCrossProcessCloseRace(t *testing.T) {
	const iterations = 25
	for iter := 0; iter < iterations; iter++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "polaris.db")

		oldProc, err := Open(path)
		if err != nil {
			t.Fatalf("iter %d: open oldProc: %v", iter, err)
		}
		if err := oldProc.CreateThread("t1", "Thread", "m", "web"); err != nil {
			t.Fatalf("iter %d: CreateThread: %v", iter, err)
		}

		newProc, err := Open(path)
		if err != nil {
			t.Fatalf("iter %d: open newProc: %v", iter, err)
		}

		var newProcWrites int32
		var newProcErrs int32
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := newProc.AddMessage("t1", "user", fmt.Sprintf("new-%d", i), "[]", "[]", 0, ""); err != nil {
					atomic.AddInt32(&newProcErrs, 1)
				} else {
					atomic.AddInt32(&newProcWrites, 1)
				}
				i++
			}
		}()

		// oldProc is mid-drain: a few more writes, then Close() — exactly
		// like cmd/run.go finishing WaitForActiveTurns and returning,
		// racing against newProc's live traffic above.
		for i := 0; i < 5; i++ {
			if _, err := oldProc.AddMessage("t1", "assistant", fmt.Sprintf("old-%d", i), "[]", "[]", 0, ""); err != nil {
				t.Errorf("iter %d: oldProc.AddMessage: %v", iter, err)
			}
		}
		if err := oldProc.Close(); err != nil {
			t.Errorf("iter %d: oldProc.Close: %v", iter, err)
		}

		time.Sleep(2 * time.Millisecond)
		close(stop)
		wg.Wait()

		writes := atomic.LoadInt32(&newProcWrites)
		errs := atomic.LoadInt32(&newProcErrs)

		// Verify newProc's own view is internally consistent first.
		msgs, err := newProc.GetMessages("t1")
		if err != nil {
			t.Fatalf("iter %d: newProc.GetMessages: %v", iter, err)
		}
		wantAtLeast := int(writes) + 5 // newProc's successful writes + oldProc's 5
		if len(msgs) < wantAtLeast {
			t.Errorf("iter %d: newProc sees %d messages, want at least %d (writes=%d errs=%d) — a write reported success but is missing, right around oldProc.Close()", iter, len(msgs), wantAtLeast, writes, errs)
		}

		if err := newProc.Close(); err != nil {
			t.Errorf("iter %d: newProc.Close: %v", iter, err)
		}

		// Reopen completely fresh (third connection, both prior ones now
		// closed) and confirm the final on-disk state matches what newProc
		// itself believed — this is the "does a later reconnect see the
		// true committed history" check.
		verify, err := Open(path)
		if err != nil {
			t.Fatalf("iter %d: reopen: %v", iter, err)
		}
		finalMsgs, err := verify.GetMessages("t1")
		if err != nil {
			t.Fatalf("iter %d: verify.GetMessages: %v", iter, err)
		}
		if len(finalMsgs) != len(msgs) {
			t.Errorf("iter %d: fresh reopen sees %d messages, newProc believed %d — mismatch after close/reopen", iter, len(finalMsgs), len(msgs))
		}
		verify.Close()
	}
}

// TestCrossProcessReadsStayMonotonic is the most direct test of the actual
// reported symptom: a client reopens/re-polls a thread and briefly sees
// OLDER state than it already saw a moment before — "bumped back". This
// keeps ONE connection (the "client-facing" one, standing in for whichever
// process is currently answering HTTP requests) polling GetMessages in a
// tight loop while a SEPARATE connection (standing in for the other
// process, mid-restart-overlap) writes concurrently and then closes
// mid-stream, exactly like cmd/run.go's deferred db.Close(). A single
// observed decrease in message count across consecutive polls is the
// signature this whole bug hunt is chasing.
func TestCrossProcessReadsStayMonotonic(t *testing.T) {
	const iterations = 15
	for iter := 0; iter < iterations; iter++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "polaris.db")

		writer, err := Open(path)
		if err != nil {
			t.Fatalf("iter %d: open writer: %v", iter, err)
		}
		if err := writer.CreateThread("t1", "Thread", "m", "web"); err != nil {
			t.Fatalf("iter %d: CreateThread: %v", iter, err)
		}

		reader, err := Open(path)
		if err != nil {
			t.Fatalf("iter %d: open reader: %v", iter, err)
		}

		stop := make(chan struct{})
		done := make(chan struct{})
		var maxSeen int
		var regressions []string

		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				msgs, err := reader.GetMessages("t1")
				if err != nil {
					continue
				}
				if len(msgs) < maxSeen {
					regressions = append(regressions, fmt.Sprintf("saw %d after already seeing %d", len(msgs), maxSeen))
				}
				if len(msgs) > maxSeen {
					maxSeen = len(msgs)
				}
			}
		}()

		for i := 0; i < 30; i++ {
			if _, err := writer.AddMessage("t1", "user", fmt.Sprintf("m-%d", i), "[]", "[]", 0, ""); err != nil {
				t.Errorf("iter %d: AddMessage: %v", iter, err)
			}
			if i == 15 {
				// Close mid-stream, like the old process exiting partway
				// through — a fresh writer takes over immediately after,
				// like a second restart landing before the first settled.
				writer.Close()
				writer, err = Open(path)
				if err != nil {
					t.Fatalf("iter %d: reopen writer: %v", iter, err)
				}
			}
		}
		writer.Close()

		// Let the reader goroutine catch up, then stop it.
		time.Sleep(20 * time.Millisecond)
		close(stop)
		<-done

		reader.Close()

		if len(regressions) > 0 {
			t.Errorf("iter %d: observed %d monotonicity regressions (a read went BACKWARDS): %v", iter, len(regressions), regressions[:min(5, len(regressions))])
		}
		if maxSeen != 30 {
			t.Errorf("iter %d: reader's max observed count = %d, want 30 (all writes eventually visible)", iter, maxSeen)
		}
	}
}
