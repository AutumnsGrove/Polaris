package gateway

import (
	"context"
	"time"

	"github.com/google/uuid"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
)

// turnRun carries one handleTurn call's state across its phases. handleTurn
// used to be a single ~1,500-line function whose locals (thread ids, the
// resolved field/model, the agent's tools.Context, Oracle's verdict, the
// finished result) were shared by every stage; hoisting them into a struct is
// what lets each stage live in its own method and file (turn_thread.go,
// turn_message.go, turn_context.go, turn_oracle.go, turn_agent.go,
// turn_persist.go, turn_followups.go) without threading a dozen parameters
// through each call. One turnRun is built per turn and never shared, so its
// fields need no locking of their own — the only state touched concurrently
// (tool handlers calling Emit) lives in turnEmitter, behind its own mutex.
type turnRun struct {
	s   *Server
	ctx context.Context
	msg ClientMessage
	cfg *config.Config

	send            func(ServerEvent)
	requestLocation func(waitCtx context.Context, threadID string) (string, bool)
	noteGhostThread func(threadID string)
	spawnBackground func(fn func())
	logEvent        func(threadID, level, source, message string, data map[string]interface{}, turnID string)

	// threadID is what the client sees; storageThreadID is where this turn's
	// messages/events are actually written (a fresh fork on edit/retry).
	threadID           string
	storageThreadID    string
	turnID             string
	isNewThread        bool
	isFirstMessageEdit bool
	isWeaverThread     bool
	ghost              bool
	fieldID            string
	field              *store.Field

	requestedModel string
	modelCfg       config.ModelConfig
	client         *llm.Client

	history         []llm.ChatMessage
	prevUserMessage string
	userMsgID       int64
	turnMessage     string

	emitter  *turnEmitter
	agentCtx *tools.Context

	oracleResult          OracleResult
	oracleFocusModeSource string
	oracleAttempted       bool
	oracleResultForEvent  *OracleResult
	appliedFocusMode      string

	turnStart       time.Time
	durationMs      int64
	result          *agent.Result
	assistantMsgID  int64
	ttftMs          int64
	tokensPerSecond float64
	toolCallCount   int
	needsCompaction bool
}

// requestLocation, when non-nil, asks the connected browser for a live
// GPS fix and blocks (bounded by locationRequestTimeout, or waitCtx being
// cancelled) for its answer — see location_broker.go. Nil on turns with
// no live client to ask, e.g. POST /api/ask (see ask.go).
func (s *Server) handleTurn(ctx context.Context, msg ClientMessage, send func(ServerEvent), requestLocation func(waitCtx context.Context, threadID string) (string, bool), trackBackground func(fn func()), noteGhostThread func(threadID string)) {
	t := &turnRun{
		s:               s,
		ctx:             ctx,
		msg:             msg,
		send:            send,
		cfg:             s.liveConfig(),
		requestLocation: requestLocation,
		noteGhostThread: noteGhostThread,
	}
	// spawnBackground launches a goroutine that outlives this function's
	// own return (auto-compaction, follow-up suggestions, verification —
	// see their call sites below), routed through trackBackground when
	// the caller has a live WebSocket connection to wait on before that
	// connection's disconnect cleanup can safely delete an abandoned
	// ghost thread (see ws.go's connWG) — nil for callers with no such
	// connection (ask.go, pulsar), which just get today's plain `go fn()`.
	t.spawnBackground = func(fn func()) {
		if trackBackground != nil {
			trackBackground(fn)
		} else {
			go fn()
		}
	}

	t.threadID = msg.ThreadID
	t.isNewThread = t.threadID == ""
	if t.isNewThread {
		t.threadID = uuid.NewString()
	}

	// Covers this entire function, every early return included — see
	// markTurnInFlight's doc comment for what this is actually for
	// (letting handleGetThread tell a pulse that's still genuinely
	// running apart from one that crashed, since a pulse has no live
	// WebSocket connection for the frontend's usual heuristic to key
	// off). Marked here, right after threadID is finalized, rather than
	// down in ws.go/ask.go's callers, so this also covers handleTurn's
	// own synchronous callers (handleAsk) without each needing its own
	// copy of this bookkeeping.
	s.markTurnInFlight(t.threadID)
	defer s.clearTurnInFlight(t.threadID)

	// turnID ties together the user message, the assistant message it
	// produces, and every event (thinking/tool call/tool result) logged
	// while this turn runs — the join key loadHistory's sibling on the
	// frontend (openThread) uses to regroup a past turn's timeline after
	// a page reload, instead of that history being stranded in the
	// events table with no way to tell one turn's events from another's
	// in the same thread.
	t.turnID = uuid.NewString()

	// ghost is issue #67's ghost/incognito mode. A ghost thread is a fully
	// real thread from its very first turn — real messages/events/title/
	// cost, nothing skipped — the only things gated on this flag below are
	// (a) which of CreateThread/CreateGhostThread tags the row at creation,
	// and (b) personalization tool wiring (memory/chat_search/stars/custom
	// instructions), re-derived fresh every turn rather than trusted from
	// the client past the creation turn — see store.go's ghost schema
	// comment and protocol.go's Anonymous doc comment. logEvent is just
	// s.db.LogEvent unconditionally now: a ghost thread gets a real, full
	// audit trail like any other.
	t.logEvent = s.db.LogEvent

	if !t.resolveStorageThread() {
		return
	}
	t.resolveThreadFlags()
	if !t.resolveField() {
		return
	}
	t.chooseModel()
	if !t.ensureThread() {
		return
	}
	if !t.loadTurnContext() {
		return
	}
	if !t.persistUserMessage() {
		return
	}
	t.resolveTurnMessage()
	t.logTurnStart()
	t.emitter = newTurnEmitter(s, send, t.threadID, t.storageThreadID, t.turnID)
	t.appendPulsarReport()
	t.buildAgentContext()
	t.runOracleStage()
	t.announceOracle()
	if !t.runAgent() {
		return
	}
	t.stripAndTitle()
	if !t.persistAnswer() {
		return
	}
	t.finishTurn()
	t.spawnCompaction()
	t.spawnSuggestions()
	t.spawnVerification()
}
