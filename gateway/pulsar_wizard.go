// pulsar_wizard.go is the "help me write the prompt" wizard's REST API —
// an ephemeral, non-persisted interview that drives a real agent.Run loop
// (NoResearch, restricted to ask_user_question/finalize_pulsar_prompt) to
// turn a vague idea into a tuned Pulsar routine prompt. See
// docs/plans/pulsar-routines.md's "v1.2" note for why this is its own
// slice, deliberately separate from gateway/turn.go: a wizard turn creates
// ZERO threads/messages rows, keeping its conversation purely in the
// in-memory session map below rather than reusing handleTurn's
// persistence-heavy machinery.
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
}

// wizardStartRequest's Seed is whatever the routine form's prompt field
// already had typed into it when the wizard was opened, if anything — an
// empty Seed means the interview opens with prompts.PulsarWizard.OpenerTask
// instead of the user's own draft.
type wizardStartRequest struct {
	Seed string `json:"seed"`
}

type wizardTurnRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// wizardResponse is both endpoints' shared reply shape: exactly one of
// Question/Final/Answer is set. Answer is the fallback — the system
// prompt asks the model to always call ask_user_question or
// finalize_pulsar_prompt rather than reply in plain prose, but nothing
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

	sessionID := uuid.NewString()
	turnMessage := strings.TrimSpace(req.Seed)
	if turnMessage == "" {
		turnMessage = prompts.Get().PulsarWizard.OpenerTask
	}

	result, err := s.runWizardTurn(r.Context(), nil, turnMessage)
	if err != nil {
		log.Warn("pulsar wizard start failed", "err", err)
		http.Error(w, "the wizard hit an error starting up — try again", http.StatusInternalServerError)
		return
	}

	s.wizardMu.Lock()
	s.wizardSessions[sessionID] = &wizardSession{history: result.history, createdAt: time.Now()}
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
	s.wizardMu.Unlock()
	if !ok {
		http.Error(w, "this wizard session has expired — start over", http.StatusGone)
		return
	}

	result, err := s.runWizardTurn(r.Context(), session.history, message)
	if err != nil {
		log.Warn("pulsar wizard turn failed", "session", req.SessionID, "err", err)
		http.Error(w, "the wizard hit an error — try again", http.StatusInternalServerError)
		return
	}

	s.wizardMu.Lock()
	session.history = result.history
	s.wizardMu.Unlock()

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
}

// runWizardTurn is the one place both handlers build the client/Context
// and call agent.Run — same tool-calling loop a real chat turn uses
// (turn.go:121-132's client construction, not the narrow one-shot
// generateTitle/generateSuggestions shape), just with no thread, no DB
// writes, and no streaming: the answer comes back directly in the HTTP
// response, not over the WebSocket.
func (s *Server) runWizardTurn(ctx context.Context, history []llm.ChatMessage, turnMessage string) (*wizardTurnResult, error) {
	cfg := s.liveConfig()
	modelCfg := cfg.ModelByID(s.effectiveDefaultModel(cfg))
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	// Locks the tool menu down to essentially ask_user_question/
	// finalize_pulsar_prompt/think: NoResearch already excludes every
	// "research"-category tool, and PulsarWizard is what makes
	// finalize_pulsar_prompt appear at all (see catalog.go's
	// "pulsar_wizard" Requires case) — but NoResearch alone would still
	// leave calculator/memory/read_attachment/visualize on the menu, which
	// a prompt-writing interview has no use for. image_search needs no
	// entry here — it's category: research, so NoResearch above already
	// excludes it the same way it does in plain chat mode.
	disabled := DisabledToolsFromStore(s.db)
	if disabled == nil {
		disabled = map[string]bool{}
	}
	disabled["calculator"] = true
	disabled["memory"] = true
	disabled["read_attachment"] = true
	disabled["visualize"] = true

	agentCtx := &tools.Context{
		NoResearch:    true,
		PulsarWizard:  true,
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
		// either ask_user_question or finalize_pulsar_prompt) depends on
		// ask_user_question actually being on the menu — leaving this nil
		// silently excluded it, degrading every interview to a plain-text
		// reply instead of the intended one-question-at-a-time flow.
		RequestLocation: func() (string, bool) { return "", false },
	}

	// agent.Run builds its own system message internally (loadSystemPrompt,
	// gated on agentCtx.PulsarWizard above to return
	// prompts.Get().PulsarWizard.System instead of the normal prompt.md
	// persona) — history here is purely the prior user/assistant turns,
	// same shape gateway/turn.go's loadHistory produces.
	result, err := agent.Run(ctx, agentCtx, history, turnMessage)
	if err != nil {
		return nil, err
	}

	newHistory := append(append([]llm.ChatMessage{}, history...),
		llm.ChatMessage{Role: "user", Content: turnMessage},
		llm.ChatMessage{Role: "assistant", Content: result.Answer},
	)

	out := &wizardTurnResult{history: newHistory, question: result.PendingQuestion, final: result.WizardFinal}
	if result.PendingQuestion == nil && result.WizardFinal == nil {
		out.answer = result.Answer
	}
	return out, nil
}
