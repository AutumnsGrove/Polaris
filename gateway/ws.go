package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Tailscale-only deployment (like every other service in this
	// homelab) — no public exposure, so a permissive origin check is
	// fine here rather than maintaining an allowlist.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// pongWait/pingPeriod implement a standard gorilla/websocket keepalive:
// without a read deadline, a half-open connection (the client's machine
// sleeps or loses network without sending a clean close frame) never
// produces a ReadJSON error, so the per-connection goroutine — and the
// read loop below — blocks forever. Sending a ping well before the
// deadline expires, and resetting the deadline on every pong, means a
// genuinely alive-but-quiet connection stays open while a truly dead one
// gets noticed and cleaned up within pongWait.
const (
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn("websocket upgrade failed", "err", err)
		s.db.LogEvent("", "warn", "ws", "websocket upgrade failed", map[string]interface{}{"err": err.Error()}, "")
		return
	}
	defer conn.Close()

	// gorilla/websocket connections aren't safe for concurrent writes;
	// emit() is called synchronously from the agent loop on this same
	// goroutine, so a mutex is defensive but cheap insurance against
	// future concurrent use (e.g. a heartbeat goroutine).
	var writeMu sync.Mutex
	send := func(evt ServerEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(evt)
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// pingDone stops the keepalive goroutine below when this connection's
	// read loop returns — otherwise every closed connection would leak
	// one goroutine ticking forever.
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				writeMu.Unlock()
				if err != nil {
					return // connection's already going away; ReadJSON below will notice
				}
			case <-pingDone:
				return
			}
		}
	}()

	// cancelSlot wraps a turn's cancel func behind a pointer so the
	// "clear the slot when this turn finishes" step below can check
	// pointer identity — plain context.CancelFunc values aren't
	// comparable in Go, and without this check, turn A finishing after
	// turn B has already started (a "stop" cancelled A, but its goroutine
	// hadn't reached this cleanup yet when B's ReadJSON returned) could
	// null out B's still-in-flight cancel func, silently breaking "stop"
	// for B. Only one turn runs at a time per connection (the frontend
	// disables the composer while busy), so a single slot is enough — it
	// just needs to be the RIGHT turn's slot being cleared.
	type cancelSlot struct{ cancel context.CancelFunc }
	var cancelMu sync.Mutex
	var current *cancelSlot

	// One broker per connection, reused across every turn sent on it —
	// see location_broker.go. A turn that never calls RequestLocation
	// just never touches it.
	var locationBroker connLocationBroker

	for {
		var msg ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				// A clean close (browser tab closed, navigated away) always
				// comes back as *websocket.CloseError — anything else is a
				// real protocol violation (garbage frame, JSON that doesn't
				// match ClientMessage) or a read timeout from a half-open
				// connection, worth a trace since it's either a client bug
				// or someone probing the endpoint.
				log.Warn("websocket read failed", "err", err)
				s.db.LogEvent("", "warn", "ws", "websocket read failed", map[string]interface{}{"err": err.Error()}, "")
			}
			// Deliberately NOT cancelling `current` here. A dropped
			// connection is not the same as the user hitting Stop — a
			// backgrounded mobile tab or a brief network blip closes the
			// socket the same way a deliberate close does, and used to
			// silently truncate the in-flight turn mid-tool-call, still
			// persisting a "done" answer with whatever partial content had
			// streamed so far. handleTurn's turnCtx is already derived from
			// context.Background(), not this connection's lifetime, so it's
			// safe to just let the goroutine keep running unattended — it
			// persists straight to the DB (see handleTurn/logTurnEvent) and
			// a later reconnect resyncs from there (see AppState's
			// resyncAfterReconnect). Only an explicit "stop" message, below,
			// should ever call current.cancel().
			return // client disconnected or sent garbage
		}

		if msg.Type == "stop" {
			cancelMu.Lock()
			if current != nil {
				current.cancel()
			}
			cancelMu.Unlock()
			continue
		}

		if msg.Type == "location_response" {
			locationBroker.deliver(msg.UserLocation)
			continue
		}

		// Only one turn runs at a time per connection — the frontend
		// enforces this by disabling the composer while busy (see
		// AppState.busy in state.svelte.ts), but that's a client-side
		// courtesy, not a guarantee: a double-submit race, a buggy/stale
		// client, or anything else that gets two "message" frames onto the
		// wire before the first turn finishes would otherwise spawn a
		// second handleTurn goroutine here and silently overwrite `current`
		// with its cancel func — orphaning the first turn's "stop"
		// capability with no way to cancel it anymore. Reject outright
		// instead of accepting a race the rest of this file was never
		// built to handle two of at once.
		cancelMu.Lock()
		if current != nil {
			cancelMu.Unlock()
			log.Warn("rejected a new turn while one was already in flight on this connection", "thread", msg.ThreadID)
			s.db.LogEvent(msg.ThreadID, "warn", "ws", "rejected concurrent turn on the same connection", nil, "")
			send(ServerEvent{Type: "error", ThreadID: msg.ThreadID, Message: "a response is already in progress on this connection — please wait for it to finish"})
			continue
		}
		turnCtx, cancel := context.WithCancel(context.Background())
		slot := &cancelSlot{cancel: cancel}
		current = slot
		cancelMu.Unlock()

		// requestLocation lets handleTurn ask this specific browser for a
		// live GPS fix mid-turn, keyed to whatever thread ID that turn
		// actually resolves (a brand-new thread's ID isn't known yet at
		// this point — msg.ThreadID is empty — so handleTurn passes its
		// own resolved threadID through at call time rather than this
		// closure capturing the wrong one).
		requestLocation := func(waitCtx context.Context, threadID string) (string, bool) {
			return locationBroker.request(waitCtx, send, threadID)
		}

		go func(ctx context.Context, cancel context.CancelFunc, msg ClientMessage) {
			defer cancel()
			// net/http recovers a panic in a handler running synchronously
			// under ServeHTTP, but this goroutine runs outside that call
			// stack — an unrecovered panic here (a bug three tool calls
			// deep, say) would otherwise crash the entire process, taking
			// down every other in-flight thread with it. Turn it into a
			// normal error event instead: the user sees a failed turn, not
			// a dead server.
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic in turn goroutine", "thread", msg.ThreadID, "panic", r)
					s.db.LogEvent(msg.ThreadID, "error", "turn", "panic during turn", map[string]interface{}{"panic": fmt.Sprint(r)}, "")
					send(ServerEvent{Type: "error", ThreadID: msg.ThreadID, Message: "internal error — please retry"})
				}
			}()
			s.handleTurn(ctx, msg, send, requestLocation)
			cancelMu.Lock()
			if current == slot {
				current = nil
			}
			cancelMu.Unlock()
		}(turnCtx, cancel, msg)
	}
}
