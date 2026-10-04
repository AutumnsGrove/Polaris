package gateway

import (
	"strings"
	"sync"
	"time"

	"polaris/tools"
)

// turnEmitter is the per-turn event pipe handed to tools.Context.Emit: it
// streams each event to the browser and persists the subset worth keeping as
// durable evidence, while counting the events that back the turn-info sheet's
// TTFT / tokens-per-second / tool-call stats.
type turnEmitter struct {
	s               *Server
	send            func(ServerEvent)
	threadID        string
	storageThreadID string
	turnID          string

	// emitMu serializes the whole emit() body below — reasoningBuf's
	// mutation isn't otherwise safe for concurrent use, and neither is
	// interleaving arbitrary tool_call/tool_result events with it. Needed
	// now that agent.Run dispatches a turn's tool calls concurrently (see
	// dispatchToolCallsConcurrently): several handlers can call ctx.Emit
	// at the same instant. send() has its own separate mutex already
	// (ws.go) for the WebSocket write specifically; this one covers
	// everything else emit() does around that write.
	mu sync.Mutex

	// reasoningBuf accumulates one "reasoning" burst — a reasoning-capable
	// model's native hidden-thinking stream arrives as dozens-to-hundreds
	// of tiny chunks, so persisting one DB row per chunk (like "token")
	// would be excessive. Instead it's flushed as a single row exactly
	// when something else interrupts it (see emit below) — the same
	// moment the frontend's closeOpenReasoning marks the live timeline
	// item "done" — so a reopened thread's reasoning lands in the same
	// position in the timeline it actually streamed in, not tacked onto
	// the end regardless of when it really happened.
	reasoningBuf strings.Builder

	// reasoningStartedAt is when the current burst's first chunk arrived —
	// flushReasoning persists the elapsed time as duration_ms so a reopened
	// thread's "Thought for 12s" header matches what streamed live (the
	// frontend can't recompute it: a reloaded burst has no timestamps of
	// its own, just the one row).
	reasoningStartedAt time.Time

	// firstTokenAt/tokenEventCount/toolCallEventCount back the Oracle mode
	// turn-info sheet's TTFT/tokens-per-second/tool-call-count stats
	// (docs/plans/oracle-mode.md) — read under emitMu below, after
	// agent.Run returns. tokensPerSecond derived from these is a
	// chunk-arrival-rate proxy (each "token" SSE event, not necessarily
	// exactly one model token), good enough for a UI display, not a
	// precise decode-rate measurement.
	firstTokenAt       time.Time
	tokenEventCount    int
	toolCallEventCount int
}

func newTurnEmitter(s *Server, send func(ServerEvent), threadID, storageThreadID, turnID string) *turnEmitter {
	return &turnEmitter{s: s, send: send, threadID: threadID, storageThreadID: storageThreadID, turnID: turnID}
}

// flushReasoning persists the buffered reasoning burst as a single event row.
func (e *turnEmitter) flushReasoning() {
	if e.reasoningBuf.Len() == 0 {
		return
	}
	data := map[string]interface{}{"content": e.reasoningBuf.String()}
	if !e.reasoningStartedAt.IsZero() {
		data["duration_ms"] = time.Since(e.reasoningStartedAt).Milliseconds()
	}
	e.s.db.LogEvent(e.storageThreadID, "info", "turn", "reasoning", data, e.turnID)
	e.reasoningBuf.Reset()
	e.reasoningStartedAt = time.Time{}
}

// emit both streams the event to the browser (send) and, for the
// subset worth keeping as durable evidence, persists it to the events
// table — "token" is deliberately excluded: it arrives as
// dozens-to-hundreds of small chunks per turn, and the assembled
// final answer is already persisted in full as the assistant message.
// "reasoning" chunks are handled separately above/below, batched into
// one row per burst instead of skipped entirely — unlike "token",
// they have no other persisted home to fall back to.
func (e *turnEmitter) emit(eventType string, payload map[string]interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch eventType {
	case "token":
		e.tokenEventCount++
		if e.firstTokenAt.IsZero() {
			e.firstTokenAt = time.Now()
		}
	case "tool_call":
		e.toolCallEventCount++
	}

	evt := ServerEvent{Type: eventType, ThreadID: e.threadID}
	if v, ok := payload["content"].(string); ok {
		evt.Content = v
	}
	if v, ok := payload["tool"].(string); ok {
		evt.Tool = v
	}
	if v, ok := payload["args"].(map[string]interface{}); ok {
		evt.Args = v
	}
	if v, ok := payload["result"].(string); ok {
		evt.Result = v
	}
	if v, ok := payload["call_id"].(string); ok {
		evt.CallID = v
	}
	if v, ok := payload["provider"].(string); ok {
		evt.Provider = v
	}
	if v, ok := payload["citations"].([]tools.Citation); ok {
		evt.Citations = v
	}
	if v, ok := payload["cards"].([]tools.Card); ok {
		evt.Cards = v
	}
	if v, ok := payload["chart"].(*tools.ChartSpec); ok {
		evt.Chart = v
	}
	if v, ok := payload["url"].(string); ok {
		evt.URL = v
	}
	if v, ok := payload["caption"].(string); ok {
		evt.Caption = v
	}
	if v, ok := payload["images"].([]tools.Card); ok {
		evt.Images = v
	}
	if v, ok := payload["cost_usd"].(float64); ok {
		evt.CostUSD = v
	}
	if eventType == "reasoning" {
		if e.reasoningBuf.Len() == 0 {
			e.reasoningStartedAt = time.Now()
		}
		e.reasoningBuf.WriteString(evt.Content)
	} else {
		e.flushReasoning()
	}
	e.send(evt)
	e.s.logTurnEvent(e.storageThreadID, e.turnID, eventType, evt)
}

// turnStats derives the turn-info sheet's stats from the counters. Read under
// the same lock emit holds while writing them: agent.Run has already returned
// by the time this is called, so nothing is still writing, but locking here
// costs nothing and avoids relying on that ordering guarantee staying true
// forever.
func (e *turnEmitter) turnStats(turnStart time.Time) (ttftMs int64, tokensPerSecond float64, toolCallCount int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.firstTokenAt.IsZero() {
		ttftMs = e.firstTokenAt.Sub(turnStart).Milliseconds()
	}
	if !e.firstTokenAt.IsZero() && e.tokenEventCount > 0 {
		if elapsed := time.Since(e.firstTokenAt).Seconds(); elapsed > 0 {
			tokensPerSecond = float64(e.tokenEventCount) / elapsed
		}
	}
	return ttftMs, tokensPerSecond, e.toolCallEventCount
}

// logTurnEvent persists the subset of streamed turn events worth keeping
// as durable evidence — thinking steps and tool calls/results, so "what
// happened during this turn" survives even if the process crashed before
// the turn finished normally. Errors surfaced mid-stream (a tool
// dispatch failure, still wrapped as a normal "tool_result" whose result
// string starts with "error:") are logged at warn instead of info so they
// stand out when scanning a thread's event history.
func (s *Server) logTurnEvent(threadID, turnID, eventType string, evt ServerEvent) {
	switch eventType {
	case "thinking":
		s.db.LogEvent(threadID, "info", "turn", "thinking", map[string]interface{}{"content": evt.Content}, turnID)
	case "commentary":
		s.db.LogEvent(threadID, "info", "turn", "commentary", map[string]interface{}{"content": evt.Content}, turnID)
	case "tool_call":
		s.db.LogEvent(threadID, "info", "tool."+evt.Tool, "tool call started", map[string]interface{}{"args": evt.Args, "call_id": evt.CallID}, turnID)
	case "tool_result":
		level := "info"
		if strings.HasPrefix(evt.Result, "error:") {
			level = "warn"
		}
		data := map[string]interface{}{
			"result": evt.Result, "citations": evt.Citations, "provider": evt.Provider, "call_id": evt.CallID,
			"url": evt.URL, "caption": evt.Caption, "images": evt.Images,
		}
		s.db.LogEvent(threadID, level, "tool."+evt.Tool, "tool call finished", data, turnID)
	case "agent_nudge":
		// Durable record of a research-steering signal firing (see
		// agent.emitNudge) — evt.Args carries kind/call_count/
		// citation_count. store.Store.GetStats reads these back to report
		// how often each signal actually fires against real usage.
		s.db.LogEvent(threadID, "info", "agent.nudge", "research steering nudge fired", evt.Args, turnID)
	}
}
