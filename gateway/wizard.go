// wizard.go is the "help me write this" wizard's REST API — an ephemeral,
// non-persisted interview that drives a real agent.Run loop (NoResearch,
// restricted to ask_user_question/finalize_wizard_prompt) to turn a vague
// idea into tuned text for whichever surface asked: a Pulsar routine's
// prompt, a Pulsar Daily block's instructions, a Field's custom
// instructions (see tools.WizardTarget). One implementation for every
// target on purpose — the interview contract is identical, and separate
// per-surface copies are exactly what drifted before this was
// consolidated. See docs/plans/pulsar-routines.md's "v1.2" note for why
// this is its own slice, deliberately separate from gateway/turn.go: a
// wizard turn creates ZERO threads/messages rows, keeping its conversation
// purely in the in-memory session map below rather than reusing
// handleTurn's persistence-heavy machinery.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"polaris/agent"
	"polaris/llm"
	"polaris/prompts"
	"polaris/tools"
)

// wizardSessionTTL is how long an idle wizard session stays valid.
// Eviction is two-layered: handleWizardTurn checks this on next access (so
// a stale session a user comes back to fails fast with a clear "start
// over" message), and sweepExpiredWizardSessions additionally piggybacks
// on the Pulsar scheduler's own once-a-minute tick to catch a session
// that's simply abandoned — opened, never sent a second message, no
// "next access" ever comes to trigger the lazy path. Without the sweep,
// that session sits in the map forever: a real, if slow, memory leak on
// a box that stays up for weeks/months, not just a missed convenience.
const wizardSessionTTL = 30 * time.Minute

type wizardSession struct {
	history   []llm.ChatMessage
	createdAt time.Time
	// busy is true while a turn on this session is actually running
	// agent.Run — checked and set together under s.wizardMu in
	// handleWizardTurn, same "reject rather than race" shape ws.go's own
	// `current != nil` check uses for a connection's in-flight turn. Without
	// it, handleWizardTurn used to read session.history, release the lock
	// for the (possibly slow) agent.Run call, then write the result back —
	// two concurrent turns on the same session_id (a double-submit; the
	// frontend disabling its own button is a courtesy, not a guarantee, same
	// caveat ws.go's own doc comment makes) would both start from the same
	// history and the second write would silently clobber the first's turn.
	busy bool
	// target is what this interview is writing (see tools.WizardTarget),
	// carried across every turn in the session since only the start request
	// actually includes it — a follow-up turn otherwise has no way to know
	// which system prompt or cost kind it belongs to.
	target tools.WizardTarget
}

// wizardStartRequest's Target names what's being written (one of
// tools.Wizard* kinds — the keys of prompts.yaml's wizard.targets), and
// Label is that target's one piece of per-instance text (a Daily block's
// title, a Field's name) for the system prompt to mention. Seed is
// whatever the calling form's field already had typed into it when the
// wizard was opened, if anything — an empty Seed means the interview opens
// with the target's own opener task instead of the user's draft.
type wizardStartRequest struct {
	Target string `json:"target"`
	Label  string `json:"label"`
	Seed   string `json:"seed"`
}

type wizardTurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// wizardResponse is both endpoints' shared reply shape: exactly one of
// Question/Final/Answer is set. Answer is the fallback — the system
// prompt asks the model to always call ask_user_question or
// finalize_wizard_prompt rather than reply in plain prose, but nothing
// enforces that the way a required tool call would, so a model that
// answers in plain text anyway still needs somewhere to go instead of
// silently vanishing (a real bug caught live: the wizard looked frozen
// with no visible response after a tap, because neither Question nor
// Final was ever set for that reply).
type wizardResponse struct {
	SessionID string                 `json:"session_id"`
	Question  *tools.PendingQuestion `json:"question,omitempty"`
	Final     *tools.WizardFinal     `json:"final,omitempty"`
	Answer    string                 `json:"answer,omitempty"`
}

func (s *Server) handleWizardStart(w http.ResponseWriter, r *http.Request) {
	var req wizardStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Rejected up front rather than letting an unknown kind through to
	// prompts.WizardSystem, which would happily assemble a system prompt
	// out of empty strings — an interview with no instructions at all.
	p := prompts.Get()
	if !p.HasWizardTarget(req.Target) {
		http.Error(w, "unknown wizard target", http.StatusBadRequest)
		return
	}
	target := tools.WizardTarget{Kind: req.Target, Label: strings.TrimSpace(req.Label)}

	sessionID := uuid.NewString()
	turnMessage := strings.TrimSpace(req.Seed)
	if turnMessage == "" {
		turnMessage = p.WizardOpenerTask(target.Kind)
	}

	result, err := s.runWizardTurn(r.Context(), nil, turnMessage, target)
	if err != nil {
		log.Warn("wizard start failed", "target", target.Kind, "err", err)
		http.Error(w, "the wizard hit an error starting up — try again", http.StatusInternalServerError)
		return
	}

	s.recordWizardCost(sessionID, target.Kind, result.costUSD)

	s.wizardMu.Lock()
	s.wizardSessions[sessionID] = &wizardSession{history: result.history, createdAt: time.Now(), target: target}
	s.wizardMu.Unlock()

	writeJSON(w, wizardResponse{SessionID: sessionID, Question: result.question, Final: result.final, Answer: result.answer})
}

func (s *Server) handleWizardTurn(w http.ResponseWriter, r *http.Request) {
	var req wizardTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	s.wizardMu.Lock()
	session, ok := s.wizardSessions[req.SessionID]
	if ok && time.Since(session.createdAt) > wizardSessionTTL {
		delete(s.wizardSessions, req.SessionID)
		ok = false
	}
	if ok && session.busy {
		s.wizardMu.Unlock()
		http.Error(w, "a response is already in progress for this wizard session — please wait for it to finish", http.StatusConflict)
		return
	}
	if ok {
		session.busy = true
	}
	s.wizardMu.Unlock()
	if !ok {
		http.Error(w, "this wizard session has expired — start over", http.StatusGone)
		return
	}

	result, err := s.runWizardTurn(r.Context(), session.history, message, session.target)

	s.wizardMu.Lock()
	session.busy = false
	if err == nil {
		session.history = result.history
	}
	s.wizardMu.Unlock()

	if err != nil {
		log.Warn("wizard turn failed", "session", req.SessionID, "target", session.target.Kind, "err", err)
		http.Error(w, "the wizard hit an error — try again", http.StatusInternalServerError)
		return
	}

	s.recordWizardCost(req.SessionID, session.target.Kind, result.costUSD)

	writeJSON(w, wizardResponse{SessionID: req.SessionID, Question: result.question, Final: result.final, Answer: result.answer})
}

// sweepExpiredWizardSessions removes every wizard session past
// wizardSessionTTL — see that constant's doc comment for why this exists
// alongside handleWizardTurn's own lazy check. Called once per Pulsar
// scheduler tick (pulsar_scheduler.go's RunPulsarScheduler), not its own
// goroutine/ticker — piggybacking keeps this to a few lines instead of a
// second timer for what's a very cheap, infrequent cleanup.
func (s *Server) sweepExpiredWizardSessions() {
	s.wizardMu.Lock()
	defer s.wizardMu.Unlock()
	for id, sess := range s.wizardSessions {
		if time.Since(sess.createdAt) > wizardSessionTTL {
			delete(s.wizardSessions, id)
		}
	}
}

type wizardTurnResult struct {
	history  []llm.ChatMessage
	question *tools.PendingQuestion
	final    *tools.WizardFinal
	// answer is set when the model replied in plain prose instead of
	// calling either tool — see wizardResponse's doc comment.
	answer string
	// costUSD is what this turn's agent.Run actually cost — carried here
	// so both handlers can bill it (see recordWizardCost). The wizard
	// persists no threads/messages rows, so without this the spend reached
	// no ledger at all.
	costUSD float64
}

// recordWizardCost bills one wizard turn's spend to the aux_usage ledger,
// which GetStats folds into the Polaris bucket and thus the settings
// panel's grand total (see that table's schema comment). Each turn records
// its own cost, not just the start: an interview runs several completions
// (one per user answer), so folding them all into the opener's row would
// lose every follow-up's spend. The ledger kind is "wizard:<target kind>"
// so a per-target breakdown stays possible without a schema change — the
// total is what the settings panel shows today, but which surface is
// spending is the first thing to ask if that number ever looks off.
// Best-effort by design, same convention as gateway/pulsar_suggest.go — a
// ledger write failing must not fail a turn the client is waiting on.
func (s *Server) recordWizardCost(sessionID, kind string, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	if err := s.db.RecordAuxCost("wizard:"+kind, costUSD); err != nil {
		log.Warn("wizard: recording cost failed", "session", sessionID, "target", kind, "err", err)
	}
}

// runWizardTurn is the one place both handlers build the client/Context
// and call agent.Run — same tool-calling loop a real chat turn uses
// (turn.go:121-132's client construction, not the narrow one-shot
// generateTitle/generateSuggestions shape), just with no thread, no DB
// writes, and no streaming: the answer comes back directly in the HTTP
// response, not over the WebSocket.
func (s *Server) runWizardTurn(ctx context.Context, history []llm.ChatMessage, turnMessage string, target tools.WizardTarget) (*wizardTurnResult, error) {
	cfg := s.liveConfig()
	modelCfg := cfg.ModelByID(s.effectiveDefaultModel(cfg))
	// AllowFallbacks(true) — escape valve for every pinned provider being
	// down at once; see gateway/turn.go's main client construction.
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)})
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	// Locks the tool menu down to essentially ask_user_question/
	// finalize_wizard_prompt/think: NoResearch already excludes every
	// "research"-category tool, and Wizard is what makes
	// finalize_wizard_prompt appear at all (see catalog.go's
	// "wizard" Requires case) — but NoResearch alone would still
	// leave calculator/memory on the menu, which a prompt-writing interview
	// has no use for. image_search needs no entry here — it's category:
	// research, so NoResearch above already excludes it the same way it
	// does in plain chat mode. code_exec (and visualize before its
	// removal, see issue #44) was never reachable here regardless — this
	// wizard's tool context never wires CodeExecEnabled, so catalog.go's
	// "docker_only" gate already keeps it off the menu without needing an
	// entry here.
	disabled := DisabledToolsFromStore(s.db)
	if disabled == nil {
		disabled = map[string]bool{}
	}
	disabled["calculator"] = true
	disabled["memory"] = true

	agentCtx := &tools.Context{
		NoResearch:    true,
		Wizard:        &target,
		DisabledTools: disabled,
		LLM:           client,
		Emit:          func(string, map[string]interface{}) {}, // no live client to stream to
		MaxTurns:      cfg.MaxAgentTurns,
		// RequestLocation is never actually called here — no location-
		// needing tool (weather/nearby_search) is ever offered under
		// NoResearch above — but catalog.go's "interactive_chat" gate on
		// ask_user_question keys off this being non-nil, not off anything
		// it returns, as its own doc comment says: "is there a live client
		// on the other end of this turn". The wizard's whole interview
		// loop (and its system prompt, which mandates every reply be
		// either ask_user_question or finalize_wizard_prompt) depends on
		// ask_user_question actually being on the menu — leaving this nil
		// silently excluded it, degrading every interview to a plain-text
		// reply instead of the intended one-question-at-a-time flow.
		RequestLocation: func() (string, bool) { return "", false },
	}

	// agent.Run builds its own system message internally (loadSystemPrompt,
	// gated on agentCtx.Wizard above to return the target's assembled
	// prompts.Get().WizardSystem instead of the normal prompt.md persona)
	// — history here is purely the prior user/assistant turns, same shape
	// gateway/turn.go's loadHistory produces.
	result, err := agent.Run(ctx, agentCtx, history, turnMessage)
	if err != nil {
		return nil, err
	}

	newHistory := append(append([]llm.ChatMessage{}, history...),
		llm.ChatMessage{Role: "user", Content: turnMessage},
		llm.ChatMessage{Role: "assistant", Content: result.Answer},
	)

	out := &wizardTurnResult{history: newHistory, question: result.PendingQuestion, final: result.WizardFinal, costUSD: result.CostUSD}
	if result.PendingQuestion == nil && result.WizardFinal == nil {
		out.answer = result.Answer
	}
	return out, nil
}
