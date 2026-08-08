package gateway

import (
	"context"
	"sync"
	"time"
)

// locationRequestTimeout bounds how long a turn waits for the browser to
// answer a live "location_request" before giving up and falling back to
// tools.Context.DefaultLocation. A var (not const) so tests can shrink it
// instead of eating the real timeout on every run of the fallback path.
var locationRequestTimeout = 8 * time.Second

// connLocationBroker coordinates the (at most one) live location round
// trip in flight on a single WebSocket connection at a time — the
// counterpart to handleWS's cancelSlot, but for "ask the browser for its
// GPS position mid-turn" instead of "cancel the turn". request() sends a
// "location_request" event and blocks until deliver() is called with the
// client's answer, the timeout passes, or waitCtx is cancelled (e.g. a
// "stop" arriving mid-wait). Zero value is ready to use.
//
// Safe for concurrent use: dispatchToolCallsConcurrently can have more
// than one tool call in the same turn reach ResolveLocation at once,
// though handleTurn's sync.Once wrapping (see its requestLocation
// closure) means only the first ever actually calls request() — this
// type doesn't depend on that, it just needs to not race if it happens.
type connLocationBroker struct {
	mu      sync.Mutex
	pending chan string
}

// request asks the browser for a fresh fix and blocks for its answer.
// Returns ("", false) on denial, timeout, or a cancelled waitCtx — never
// an error, since "no live location" is always a normal, expected outcome
// here (see tools.Context.ResolveLocation's fallback to DefaultLocation).
func (b *connLocationBroker) request(waitCtx context.Context, send func(ServerEvent), threadID string) (string, bool) {
	ch := make(chan string, 1)
	b.mu.Lock()
	b.pending = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.pending == ch {
			b.pending = nil
		}
		b.mu.Unlock()
	}()

	send(ServerEvent{Type: "location_request", ThreadID: threadID})

	select {
	case loc := <-ch:
		return loc, loc != ""
	case <-time.After(locationRequestTimeout):
		return "", false
	case <-waitCtx.Done():
		return "", false
	}
}

// deliver hands a client's "location_response" to whichever request()
// call is currently waiting, if any. A stray or late response (nothing
// pending, or a slow reply arriving after request() already timed out) is
// silently dropped rather than treated as an error — there's no
// correlation ID to check it against because only one request is ever in
// flight per connection at a time (see handleWS's single-turn invariant).
func (b *connLocationBroker) deliver(location string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending != nil {
		select {
		case b.pending <- location:
		default:
		}
	}
}
