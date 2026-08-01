package gateway

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/tools"
)

func (s *Server) handleTurn(ctx context.Context, msg ClientMessage, send func(ServerEvent)) {
	cfg := s.liveConfig()

	threadID := msg.ThreadID
	isNewThread := threadID == ""
	if isNewThread {
		threadID = uuid.NewString()
	}

	// turnID ties together the user message, the assistant message it
	// produces, and every event (thinking/tool call/tool result) logged
	// while this turn runs — the join key loadHistory's sibling on the
	// frontend (openThread) uses to regroup a past turn's timeline after
	// a page reload, instead of that history being stranded in the
	// events table with no way to tell one turn's events from another's
	// in the same thread.
	turnID := uuid.NewString()

	// Retry/edit: wipe the message being replaced and everything after it
	// (no branching history) before persisting the new/unchanged content.
	if msg.EditFromID != 0 {
		if err := s.db.DeleteMessagesFrom(threadID, msg.EditFromID); err != nil {
			s.db.LogEvent(threadID, "error", "turn", "deleting messages for edit/retry failed", map[string]interface{}{"err": err.Error()}, turnID)
			send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
			return
		}
	}

	requestedModel := msg.Model
	if requestedModel == "" {
		requestedModel = s.effectiveDefaultModel(cfg)
	}
	modelCfg := cfg.ModelByID(requestedModel)
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		WithSessionID(threadID) // sticky routing — same provider endpoint across the thread, for cache hits
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: true, Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	if isNewThread {
		title := msg.Content
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		source := msg.Source
		if source == "" {
			source = "web"
		}
		if err := s.db.CreateThread(threadID, title, modelCfg.ID, source); err != nil {
			s.db.LogEvent(threadID, "error", "turn", "creating thread failed", map[string]interface{}{"err": err.Error()}, turnID)
			send(ServerEvent{Type: "error", Message: err.Error()})
			return
		}
	}

	history, err := s.loadHistory(threadID)
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "loading history failed", map[string]interface{}{"err": err.Error()}, turnID)
		send(ServerEvent{Type: "error", Message: err.Error()})
		return
	}

	// Persist the user message before running the agent, not after — so
	// it (and its ID, needed for retry/edit) survives even if the turn
	// below errors out. Previously a failed turn left no record at all.
	// SttCostUSD folds in push-to-talk transcription cost, if this
	// message originated from a voice memo.
	userMsgID, err := s.db.AddMessage(threadID, "user", msg.Content, "[]", "[]", msg.SttCostUSD, turnID)
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "persisting user message failed", map[string]interface{}{"err": err.Error()}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
		return
	}
	send(ServerEvent{Type: "user_message", ThreadID: threadID, UserMessageID: userMsgID})

	if msg.AttachmentID != "" {
		if err := s.db.SetMessageAttachment(userMsgID, msg.AttachmentFilename, msg.AttachmentContentType); err != nil {
			log.Warn("failed to record attachment metadata", "err", err)
			s.db.LogEvent(threadID, "warn", "turn", "recording attachment metadata failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	// turnMessage is what the agent actually sees — msg.Content plus the
	// attachment's extracted text, if any (see resolveAttachment). The
	// persisted user message above stays as exactly what the user typed;
	// only the in-flight prompt to the model is augmented, so reopening
	// this thread later shows the original question, not a wall of
	// extracted PDF text glued onto it.
	turnMessage, attachmentCostUSD, err := resolveAttachment(ctx, cfg, msg)
	if err != nil {
		log.Warn("resolving attachment failed, continuing without it", "err", err)
		s.db.LogEvent(threadID, "warn", "turn", "resolving attachment failed", map[string]interface{}{"err": err.Error()}, turnID)
		turnMessage = msg.Content
		attachmentCostUSD = 0
	}

	s.db.LogEvent(threadID, "info", "turn", "turn started", map[string]interface{}{
		"model":         modelCfg.ID,
		"is_new_thread": isNewThread,
		"voice_mode":    msg.VoiceMode,
		"is_retry":      msg.EditFromID != 0,
	}, turnID)

	// reasoningBuf accumulates one "reasoning" burst — a reasoning-capable
	// model's native hidden-thinking stream arrives as dozens-to-hundreds
	// of tiny chunks, so persisting one DB row per chunk (like "token")
	// would be excessive. Instead it's flushed as a single row exactly
	// when something else interrupts it (see emit below) — the same
	// moment the frontend's closeOpenReasoning marks the live timeline
	// item "done" — so a reopened thread's reasoning lands in the same
	// position in the timeline it actually streamed in, not tacked onto
	// the end regardless of when it really happened.
	var reasoningBuf strings.Builder
	flushReasoning := func() {
		if reasoningBuf.Len() == 0 {
			return
		}
		s.db.LogEvent(threadID, "info", "turn", "reasoning", map[string]interface{}{"content": reasoningBuf.String()}, turnID)
		reasoningBuf.Reset()
	}

	// emitMu serializes the whole emit() body below — reasoningBuf's
	// mutation isn't otherwise safe for concurrent use, and neither is
	// interleaving arbitrary tool_call/tool_result events with it. Needed
	// now that agent.Run dispatches a turn's tool calls concurrently (see
	// dispatchToolCallsConcurrently): several handlers can call ctx.Emit
	// at the same instant. send() has its own separate mutex already
	// (ws.go) for the WebSocket write specifically; this one covers
	// everything else emit() does around that write.
	var emitMu sync.Mutex

	// emit both streams the event to the browser (send) and, for the
	// subset worth keeping as durable evidence, persists it to the events
	// table — "token" is deliberately excluded: it arrives as
	// dozens-to-hundreds of small chunks per turn, and the assembled
	// final answer is already persisted in full as the assistant message.
	// "reasoning" chunks are handled separately above/below, batched into
	// one row per burst instead of skipped entirely — unlike "token",
	// they have no other persisted home to fall back to.
	emit := func(eventType string, payload map[string]interface{}) {
		emitMu.Lock()
		defer emitMu.Unlock()

		evt := ServerEvent{Type: eventType, ThreadID: threadID}
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
		if v, ok := payload["citations"].([]tools.Citation); ok {
			evt.Citations = v
		}
		if eventType == "reasoning" {
			reasoningBuf.WriteString(evt.Content)
		} else {
			flushReasoning()
		}
		send(evt)
		s.logTurnEvent(threadID, turnID, eventType, evt)
	}

	// The browser's geolocation (cached client-side, see protocol.go's
	// UserLocation doc comment) takes precedence over the static
	// config.yaml default — a "near me" query should mean where the
	// phone actually is right now, not wherever the operator was when
	// they first set up the potato.
	defaultLocation := cfg.DefaultLocation
	if msg.UserLocation != "" {
		defaultLocation = msg.UserLocation
	}

	agentCtx := &tools.Context{
		SearXNG:         s.searxng,
		Foursquare:      s.foursquare,
		Tavily:          s.tavily,
		DefaultLocation: defaultLocation,
		VoiceMode:       msg.VoiceMode,
		FocusMode:       msg.FocusMode,
		DeepResearch:    msg.DeepResearch,
		LLM:             client,
		Emit:            emit,
		MaxTurns:        cfg.MaxAgentTurns,
	}

	// Timed around agent.Run specifically, not the whole handler — this is
	// "how long it took to get an answer", the number a user watching the
	// tokens stream in actually cares about. Excludes the follow-up
	// suggestions/title-generation calls after it, which run invisibly
	// (the answer's already fully rendered) and would otherwise inflate a
	// short answer's reported time with unrelated background work.
	turnStart := time.Now()
	result, err := agent.Run(ctx, agentCtx, history, turnMessage)
	durationMs := time.Since(turnStart).Milliseconds()
	// Catches a reasoning burst still open when the turn ended — normally
	// the final answer's "token" events already triggered this via emit
	// above, but a turn that errored or was stopped mid-reasoning
	// wouldn't have reached that point.
	flushReasoning()
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "turn failed", map[string]interface{}{"err": err.Error(), "model": modelCfg.ID}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, UserMessageID: userMsgID, Message: err.Error()})
		return
	}

	// Follow-up suggestions, Perplexity-style — generated before persisting
	// so they're saved alongside the answer, same as citations, instead of
	// living only in this turn's live event stream. Skipped on a stopped
	// generation (ctx.Err() != nil) since suggesting where to go next from
	// an answer the user just cut off isn't useful.
	var suggestions []string
	if ctx.Err() == nil && result.Answer != "" {
		if sug, sugCost, err := s.generateSuggestions(cfg, modelCfg, msg.Content, result.Answer); err != nil {
			log.Warn("follow-up suggestions failed", "thread", threadID, "err", err)
			s.db.LogEvent(threadID, "warn", "suggestions", "follow-up suggestions failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else {
			suggestions = sug
			result.CostUSD += sugCost
			// No error, but nothing usable came back either — a reasoning
			// model can spend its whole completion budget on hidden
			// reasoning tokens and never reach visible content (see
			// generateTitle's doc comment for the real case that
			// surfaced this). Otherwise this failure mode is completely
			// silent: no error to log, no suggestions to show, no trace
			// in the event log to explain why.
			if len(sug) == 0 {
				s.db.LogEvent(threadID, "warn", "suggestions", "model returned no usable suggestions", nil, turnID)
			}
		}
	}
	suggestionsJSON, _ := json.Marshal(suggestions)

	// One-time LLM-generated thread title, replacing the truncated-first-
	// message placeholder CreateThread set above — only on a brand-new
	// thread's first turn, never again after (a manual rename, or just
	// leaving the placeholder, both take precedence over ever
	// regenerating this). Same completion-gating as suggestions: skip on
	// a stopped generation or an empty answer, where the placeholder is
	// already the more sensible title anyway.
	if isNewThread && ctx.Err() == nil && result.Answer != "" {
		if title, titleCost, err := s.generateTitle(cfg, modelCfg, msg.Content); err != nil {
			log.Warn("thread title generation failed", "thread", threadID, "err", err)
			s.db.LogEvent(threadID, "warn", "title", "thread title generation failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else if title != "" {
			if err := s.db.SetThreadTitle(threadID, title); err != nil {
				log.Warn("failed to persist generated thread title", "err", err)
				s.db.LogEvent(threadID, "warn", "title", "persisting generated title failed", map[string]interface{}{"err": err.Error()}, turnID)
			} else {
				result.CostUSD += titleCost
				s.db.LogEvent(threadID, "info", "title", "thread title generated", map[string]interface{}{"title": title, "cost_usd": titleCost}, turnID)
			}
		} else {
			// No error, but nothing usable came back — this exact gap is
			// what let a real bug hide completely: generateTitle's client
			// used to cap completions at 60 tokens, and a reasoning model
			// (this app's own default, "deepseek") can spend that whole
			// budget on hidden reasoning tokens before emitting any
			// visible content, leaving resp.Content empty. Neither branch
			// above fired, so the thread silently kept its
			// truncated-question placeholder forever with zero trace in
			// the event log explaining why. Logging this case (and
			// raising generateTitle's token budget) closes both the
			// silence and the likely cause.
			s.db.LogEvent(threadID, "warn", "title", "model returned no usable title", nil, turnID)
		}
	}

	citationsJSON, _ := json.Marshal(result.Citations)
	assistantMsgID, err := s.db.AddMessage(threadID, "assistant", result.Answer, string(citationsJSON), string(suggestionsJSON), result.CostUSD, turnID)
	if err != nil {
		log.Warn("failed to persist assistant message", "err", err)
		s.db.LogEvent(threadID, "error", "turn", "persisting assistant message failed", map[string]interface{}{"err": err.Error()}, turnID)
	}

	if err := s.db.SetContextTokens(threadID, result.ContextTokens); err != nil {
		log.Warn("failed to record context tokens", "err", err)
		s.db.LogEvent(threadID, "warn", "turn", "recording context tokens failed", map[string]interface{}{"err": err.Error()}, turnID)
	}

	if assistantMsgID != 0 {
		if err := s.db.SetMessageDuration(assistantMsgID, durationMs); err != nil {
			log.Warn("failed to record message duration", "err", err)
			s.db.LogEvent(threadID, "warn", "turn", "recording message duration failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	// Auto-compact once this thread crosses the configured threshold: the
	// model summarizes everything covered so far, and future turns build
	// history from that summary instead of the full raw text. The
	// messages table itself is untouched — only what gets sent back to
	// the LLM shrinks, the visible transcript stays the true record.
	contextTokens := result.ContextTokens
	if result.ContextTokens >= cfg.ContextWindowTokens && assistantMsgID != 0 {
		if summary, compactCost, err := s.compactThread(client, threadID, assistantMsgID); err != nil {
			log.Warn("auto-compaction failed", "thread", threadID, "err", err)
			s.db.LogEvent(threadID, "warn", "compaction", "auto-compaction failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else {
			contextTokens = estimateTokens(summary)
			send(ServerEvent{Type: "compacted", ThreadID: threadID, Content: summary})
			s.db.LogEvent(threadID, "info", "compaction", "thread auto-compacted", map[string]interface{}{
				"through_message_id": assistantMsgID,
				"cost_usd":           compactCost,
				"summary":            summary,
			}, turnID)
			result.CostUSD += compactCost
		}
	}

	// Total cost added to the thread this turn: the agent's LLM/tool
	// spend plus any STT cost from a voice memo, plus an image
	// attachment's description call, plus compaction's own cost if it
	// just ran, plus follow-up suggestions — all persisted above, so the
	// frontend's running total should reflect all of them.
	totalCost := result.CostUSD + msg.SttCostUSD + attachmentCostUSD
	s.db.LogEvent(threadID, "info", "turn", "turn completed", map[string]interface{}{
		"model":          modelCfg.ID,
		"cost_usd":       totalCost,
		"context_tokens": contextTokens,
		"citations":      len(result.Citations),
		"stopped":        ctx.Err() != nil,
	}, turnID)

	send(ServerEvent{
		Type:          "done",
		ThreadID:      threadID,
		UserMessageID: userMsgID,
		Citations:     result.Citations,
		CostUSD:       totalCost,
		ContextTokens: contextTokens,
		Suggestions:   suggestions,
		DurationMs:    durationMs,
	})
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
		s.db.LogEvent(threadID, "info", "tool."+evt.Tool, "tool call started", map[string]interface{}{"args": evt.Args}, turnID)
	case "tool_result":
		level := "info"
		if strings.HasPrefix(evt.Result, "error:") {
			level = "warn"
		}
		s.db.LogEvent(threadID, level, "tool."+evt.Tool, "tool call finished", map[string]interface{}{"result": evt.Result, "citations": evt.Citations}, turnID)
	}
}

// suggestionListPrefix strips list-style prefixes ("1. ", "- ", "• ") the
// model sometimes adds despite being told not to — deliberately narrow
// (requires the punctuation/space right after digits) so it never eats a
// genuine leading number in a question, e.g. "2024 election results?".
var suggestionListPrefix = regexp.MustCompile(`^(?:[-*•]\s+|\d+[.)]\s+)`)

// maxSuggestionLen caps how long a parsed line can be and still count as
// a follow-up question — a real one reads like "Which company has built
// the most transformer models so far?" (well under this), not a paragraph.
// Belt-and-suspenders against ever showing a runaway response as a chip
// again: the tight MaxTokens below should already prevent it, but this
// means a formatting slip can't leak a full answer into the UI either.
const maxSuggestionLen = 140

// generateSuggestions asks for up to 3 short follow-up questions based on
// the exchange that just finished — one extra cheap, non-streamed LLM
// call, same pattern as compactThread below. Only the last exchange is
// given as context (not the full thread history): follow-ups are about
// "where could this conversation go next", not a function of everything
// said earlier.
//
// Deliberately builds its own client from modelCfg rather than reusing the
// thread's tool-capable client — a fully separate call with no tools
// offered and a tight token cap, so it can never wander into producing a
// real answer instead of short questions. Still pins the provider the
// same way the main client does: leaving that off routes to whatever
// OpenRouter picks by default, which can land on a degraded/no-reasoning
// endpoint for the same model slug and come back with near-empty content.
func (s *Server) generateSuggestions(cfg *config.Config, modelCfg config.ModelConfig, userMessage, answer string) ([]string, float64, error) {
	// 500, not a tighter cap — a reasoning model (this app's own default,
	// "deepseek") spends part of any completion budget on hidden
	// reasoning tokens before it ever emits visible content. A cap too
	// close to what a normal short answer needs risks the reasoning
	// alone consuming it, leaving resp.Content empty — silently, with no
	// error to catch. See generateTitle's doc comment for the concrete
	// case this exact failure mode caused.
	sugClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 500).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})

	// The task instruction lives in the LAST message, as a fresh "user"
	// turn after the exchange — not just in the system prompt above it.
	// Ending the array on the assistant's full answer (as this used to)
	// invites a weaker model to keep going: it reads as "continue this
	// turn", so the model just extends/qualifies the answer instead of
	// switching to the actual task. A real example hit this exactly —
	// asked about the largest dense LLM, answered PaLM, and the single
	// "suggestion" that came back was "The model was never open-sourced,
	// but the architecture and results were published." — a continuation
	// sentence, not a question. Putting the instruction last, with an
	// explicit anti-continuation line and a worked example, reliably
	// signals a context switch instead.
	prompt := []llm.ChatMessage{
		{Role: "system", Content: "You write short follow-up-question suggestions for a Q&A search app's " +
			"UI. You never continue, restate, or add commentary to the previous answer — your only output " +
			"is brand-new questions the user could ask next."},
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: answer},
		{Role: "user", Content: "Suggest exactly 3 short, natural follow-up questions based on the " +
			"exchange above. One per line, no numbering, no quotes, no preamble — just the 3 questions. " +
			"Each line must be a real question and end with a question mark. Do not continue or add to " +
			"the previous answer.\n\nExample output:\nWhat is the population of Paris?\nHow does it " +
			"compare to other European capitals?\nWhat other cities have served as France's capital?"},
	}

	resp, err := sugClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return nil, 0, err
	}

	var suggestions []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = suggestionListPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		line = strings.Trim(line, "\"")
		// Requiring a trailing question mark is a belt-and-suspenders
		// filter against exactly the continuation-sentence failure mode
		// above: even if a model slips past the prompt's instructions, a
		// non-question line gets dropped silently rather than shown as a
		// "suggestion" that's actually just more of the answer. Showing
		// 0 suggestions is far less confusing than showing a wrong one.
		if line == "" || len(line) > maxSuggestionLen {
			continue
		}
		if !strings.HasSuffix(line, "?") && !strings.HasSuffix(line, "？") {
			continue
		}
		suggestions = append(suggestions, line)
		if len(suggestions) == 3 {
			break
		}
	}
	return suggestions, resp.CostUSD, nil
}

// titleQuotePrefix strips a leading/trailing quote mark the model
// sometimes wraps the title in despite being told not to — trimmed
// separately from strings.Trim below since that would also eat a quote
// that's genuinely part of the title (e.g. a title ending in "quotes").
var titleQuotePrefix = regexp.MustCompile(`^["'“‘]+|["'”’]+$`)

// maxTitleLen caps the generated title's length — a good one reads like
// "Capital of France" or "Debugging a Go goroutine leak" (well under
// this), not a restated question. Same belt-and-suspenders reasoning as
// maxSuggestionLen: the tight MaxTokens below should already prevent a
// runaway response, this just means a formatting slip can't leak one
// into the sidebar as a "title" anyway — falls back to the
// truncated-first-message placeholder in that case instead.
const maxTitleLen = 60

// generateTitle asks for a short thread title based on the user's
// question alone — one extra cheap, non-streamed LLM call, same pattern
// as generateSuggestions/compactThread. Only called once, right after a
// brand-new thread's first turn (see handleTurn's isNewThread gate).
//
// Deliberately does NOT include the answer, unlike an earlier version of
// this function — with the answer in context, the message array ends on
// a full assistant turn, and a weaker model reliably reads that as "keep
// going" rather than "switch to a new task", producing a title that's
// actually just a continuation of the answer (a real example: title came
// back as "Unfortunately, it was never open-sourced, so while
// available..." — a sentence fragment from the answer, not a title). A
// search-app question is almost always self-descriptive enough on its
// own ("what is the largest dense llm released?"), so dropping the
// answer sidesteps the whole failure mode instead of working around it.
//
// 300, not a tighter cap — this used to be 60, and a real production
// thread never got a generated title at all: the default model
// ("deepseek") is a reasoning model, and reasoning tokens count against
// the same completion budget as visible content. 60 tokens was
// consumed entirely by hidden reasoning, resp.Content came back empty,
// and — since that's not an error — the code below silently did
// nothing, leaving the truncated-question placeholder forever with no
// trace in the event log explaining why (see handleTurn's title-gating
// block, which now logs this case explicitly too).
func (s *Server) generateTitle(cfg *config.Config, modelCfg config.ModelConfig, userMessage string) (string, float64, error) {
	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 300).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})

	prompt := []llm.ChatMessage{
		{Role: "system", Content: "Based on this question, write a short thread title summarizing what " +
			"it's about — 3 to 6 words, plain text, no quotes, no trailing punctuation, no preamble or " +
			"extra commentary. Title Case is fine but not required."},
		{Role: "user", Content: userMessage},
	}

	resp, err := titleClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}

	title := strings.TrimSpace(resp.Content)
	title = strings.TrimSpace(titleQuotePrefix.ReplaceAllString(title, ""))
	title = strings.TrimRight(title, ".!。")
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen]
	}
	return title, resp.CostUSD, nil
}

// compactThread summarizes every message up to and including throughID,
// via one extra (non-streamed, not shown as a normal answer) LLM call,
// and records that summary so loadHistory substitutes it for the raw
// messages it covers on every subsequent turn.
func (s *Server) compactThread(client llm.ChatClient, threadID string, throughID int64) (summary string, cost float64, err error) {
	history, err := s.loadHistory(threadID)
	if err != nil {
		return "", 0, err
	}
	prompt := []llm.ChatMessage{
		{Role: "system", Content: "Summarize the following conversation concisely but completely: preserve " +
			"every fact, decision, name, number, and cited URL that might matter later. This summary will " +
			"fully replace the conversation history, so omitting something means it's gone for good. Write " +
			"it as plain prose, not a transcript."},
	}
	prompt = append(prompt, history...)

	resp, err := client.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}

	if err := s.db.CompactThread(threadID, resp.Content, throughID, resp.CostUSD, estimateTokens(resp.Content)); err != nil {
		return "", 0, err
	}
	return resp.Content, resp.CostUSD, nil
}

// estimateTokens is a rough tokens-per-character heuristic (English text
// averages ~4 chars/token) used only to seed context_tokens right after a
// compaction, before the next real LLM call reports an actual count.
func estimateTokens(s string) int {
	return len(s) / 4
}

// loadHistory reconstructs prior turns as ChatMessage pairs so a
// resumed/continued thread has full context. If the thread has been
// auto-compacted, everything at or below compacted_through_id is replaced
// by a single summary message instead of being sent in full.
func (s *Server) loadHistory(threadID string) ([]llm.ChatMessage, error) {
	thread, err := s.db.GetThread(threadID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.db.GetMessages(threadID)
	if err != nil {
		return nil, err
	}

	history := make([]llm.ChatMessage, 0, len(msgs)+1)
	if thread.CompactedSummary != "" {
		history = append(history, llm.ChatMessage{
			Role: "assistant",
			Content: "(Summary of earlier conversation, compacted to save context — the full history " +
				"is no longer available, only this summary)\n\n" + thread.CompactedSummary,
		})
	}
	for _, m := range msgs {
		if m.ID <= thread.CompactedThroughID {
			continue // covered by the summary above
		}
		history = append(history, llm.ChatMessage{Role: m.Role, Content: m.Content})
	}
	return history, nil
}
