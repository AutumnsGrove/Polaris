package tools

import (
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

// TestSearchDedupKey_NormalizesQueryCaseAndWhitespace covers the "near-
// identical query" case from docs/plans/deep-research-two-tier.md's
// "Budget (session-level)" section — two sub-agents phrasing the same
// query with different capitalization or incidental whitespace should
// still dedupe to the same key.
func TestSearchDedupKey_NormalizesQueryCaseAndWhitespace(t *testing.T) {
	a := searchDedupKey("searxng", "Golang   Release  Notes", "", 1, 5)
	b := searchDedupKey("searxng", "golang release notes", "", 1, 5)
	if a != b {
		t.Errorf("keys differ after case/whitespace normalization: %q vs %q", a, b)
	}
}

// TestSearchDedupKey_DistinguishesRelevantParams covers the failure mode
// of over-aggressive deduping — two calls that differ in provider,
// category, page, or max_results are genuinely different requests and
// must not collide.
func TestSearchDedupKey_DistinguishesRelevantParams(t *testing.T) {
	base := searchDedupKey("searxng", "cats", "", 1, 5)
	cases := map[string]string{
		"provider":    searchDedupKey("brave", "cats", "", 1, 5),
		"category":    searchDedupKey("searxng", "cats", "news", 1, 5),
		"page":        searchDedupKey("searxng", "cats", "", 2, 5),
		"max_results": searchDedupKey("searxng", "cats", "", 1, 10),
	}
	for name, key := range cases {
		if key == base {
			t.Errorf("%s: key %q collided with base %q, want distinct", name, key, base)
		}
	}
}

// TestDedupedCall_NilGroupRunsEveryTime covers the default (no
// Context.SearchDedup configured) case — every call executes fn
// independently, never sharing.
func TestDedupedCall_NilGroupRunsEveryTime(t *testing.T) {
	ctx := &Context{}
	calls := 0
	for i := 0; i < 3; i++ {
		_, shared, err := dedupedCall(ctx, "key", func() (int, error) {
			calls++
			return 42, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if shared {
			t.Error("shared = true with nil SearchDedup, want false")
		}
	}
	if calls != 3 {
		t.Errorf("fn called %d times, want 3 (no dedup without a group)", calls)
	}
}

// TestDedupedCall_ConcurrentIdenticalKeysShareOneCall is the core
// guarantee the dedup layer exists for: two goroutines (standing in for
// two sub-agents) calling dedupedCall with the same key at the same time
// must trigger fn exactly once, with the second caller reported as
// shared=true.
func TestDedupedCall_ConcurrentIdenticalKeysShareOneCall(t *testing.T) {
	ctx := &Context{SearchDedup: &singleflight.Group{}}
	var calls int
	var mu sync.Mutex
	release := make(chan struct{})
	started := make(chan struct{})

	fn := func() (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		<-release // hold the call open so the second goroutine overlaps it
		return "result", nil
	}

	var wg sync.WaitGroup
	results := make([]string, 2)
	shareds := make([]bool, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		v, shared, _ := dedupedCall(ctx, "same-key", fn)
		results[0], shareds[0] = v, shared
	}()
	go func() {
		defer wg.Done()
		<-started // ensure the first call is already in flight before this one starts
		v, shared, _ := dedupedCall(ctx, "same-key", func() (string, error) {
			t.Error("second goroutine's own fn ran — it should have shared the first call's in-flight result instead")
			return "wrong", nil
		})
		results[1], shareds[1] = v, shared
	}()

	<-started
	// Give the second goroutine a chance to actually reach its Do() call
	// and register itself as a waiter on "same-key" before the first call
	// completes — singleflight only shares a result with callers that
	// join while the original call is still in flight, so releasing too
	// early would let both fn's run independently and defeat the test.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("fn executed %d times, want exactly 1", calls)
	}
	if results[0] != "result" || results[1] != "result" {
		t.Errorf("results = %v, want both callers to get the shared result", results)
	}
	if !shareds[0] && !shareds[1] {
		t.Error("neither call reported shared=true, want at least one (whichever arrived second)")
	}
}

// TestDedupedCall_PropagatesError confirms an error from fn is returned
// to the caller rather than swallowed.
func TestDedupedCall_PropagatesError(t *testing.T) {
	ctx := &Context{SearchDedup: &singleflight.Group{}}
	wantErr := errors.New("boom")
	_, _, err := dedupedCall(ctx, "key", func() (int, error) {
		return 0, wantErr
	})
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}
