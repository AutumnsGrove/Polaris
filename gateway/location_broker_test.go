package gateway

import (
	"context"
	"testing"
	"time"
)

// TestConnLocationBroker_DeliversToWaitingRequest exercises the normal
// path: request() sends exactly one "location_request" event carrying the
// thread ID it was given, and deliver() (called here from inside the fake
// send — request() has already registered its channel by the time send()
// runs, so this is deterministic, not a race) unblocks it with the answer.
func TestConnLocationBroker_DeliversToWaitingRequest(t *testing.T) {
	var b connLocationBroker
	var gotEvent ServerEvent
	var sendCount int
	send := func(evt ServerEvent) {
		sendCount++
		gotEvent = evt
		b.deliver("47.6062, -122.3321")
	}

	loc, ok := b.request(context.Background(), send, "thread-1")

	if !ok || loc != "47.6062, -122.3321" {
		t.Fatalf("request() = (%q, %v), want (\"47.6062, -122.3321\", true)", loc, ok)
	}
	if sendCount != 1 {
		t.Fatalf("send called %d times, want exactly 1", sendCount)
	}
	if gotEvent.Type != "location_request" || gotEvent.ThreadID != "thread-1" {
		t.Errorf("sent event = %+v, want type=location_request thread_id=thread-1", gotEvent)
	}
}

// An empty reply (browser denied/unavailable) is a normal outcome, not an
// error — request() should report it as "no location", not as the empty
// string being treated as a valid fix.
func TestConnLocationBroker_EmptyDeliveryMeansNoLocation(t *testing.T) {
	var b connLocationBroker
	send := func(evt ServerEvent) { b.deliver("") }

	loc, ok := b.request(context.Background(), send, "thread-1")

	if ok || loc != "" {
		t.Fatalf("request() = (%q, %v), want (\"\", false) for an empty/denied reply", loc, ok)
	}
}

// A client that never replies at all (tab backgrounded, closed, or just
// slow) must not hang the turn forever — request() has to give up on its
// own once locationRequestTimeout passes.
func TestConnLocationBroker_TimesOutWithNoReply(t *testing.T) {
	orig := locationRequestTimeout
	locationRequestTimeout = 20 * time.Millisecond
	defer func() { locationRequestTimeout = orig }()

	var b connLocationBroker
	send := func(evt ServerEvent) {} // never replies

	start := time.Now()
	loc, ok := b.request(context.Background(), send, "thread-1")
	elapsed := time.Since(start)

	if ok || loc != "" {
		t.Fatalf("request() = (%q, %v), want (\"\", false) on timeout", loc, ok)
	}
	if elapsed < locationRequestTimeout {
		t.Errorf("request() returned after %v, before locationRequestTimeout (%v) had even elapsed", elapsed, locationRequestTimeout)
	}
}

// A "stop" mid-wait cancels the turn's context — request() must respect
// that instead of blocking on a browser reply for a turn nobody's waiting
// on anymore.
func TestConnLocationBroker_CancelledContextStopsWaiting(t *testing.T) {
	var b connLocationBroker
	ctx, cancel := context.WithCancel(context.Background())
	send := func(evt ServerEvent) { cancel() } // simulates "stop" arriving right after the request goes out

	loc, ok := b.request(ctx, send, "thread-1")

	if ok || loc != "" {
		t.Fatalf("request() = (%q, %v), want (\"\", false) once waitCtx is cancelled", loc, ok)
	}
}

// A reply that arrives with nothing pending (already timed out, or no
// request ever made) must be dropped silently — not panic, not block the
// read loop that's delivering it.
func TestConnLocationBroker_DeliverWithNothingPendingIsNoop(t *testing.T) {
	var b connLocationBroker
	b.deliver("47.6062, -122.3321")
}
