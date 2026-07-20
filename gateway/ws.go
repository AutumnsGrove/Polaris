package gateway

import (
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Tailscale-only deployment (like every other service in this
	// homelab) — no public exposure, so a permissive origin check is
	// fine here rather than maintaining an allowlist.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn("websocket upgrade failed", "err", err)
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

	// Only one turn runs at a time per connection (the frontend disables
	// the composer while busy), so a single cancel slot is enough to
	// support "stop". Each turn now runs in its own goroutine so this read
	// loop can keep pulling frames off the wire concurrently — otherwise a
	// "stop" message sent mid-turn would just sit unread until the turn
	// finished on its own, defeating the point.
	var cancelMu sync.Mutex
	var cancelTurn context.CancelFunc

	for {
		var msg ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			cancelMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
			}
			cancelMu.Unlock()
			return // client disconnected or sent garbage
		}

		if msg.Type == "stop" {
			cancelMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
			}
			cancelMu.Unlock()
			continue
		}

		turnCtx, cancel := context.WithCancel(context.Background())
		cancelMu.Lock()
		cancelTurn = cancel
		cancelMu.Unlock()

		go func(ctx context.Context, cancel context.CancelFunc, msg ClientMessage) {
			defer cancel()
			s.handleTurn(ctx, msg, send)
			cancelMu.Lock()
			cancelTurn = nil
			cancelMu.Unlock()
		}(turnCtx, cancel, msg)
	}
}
