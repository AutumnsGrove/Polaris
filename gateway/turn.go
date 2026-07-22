package gateway

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

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

	// Retry/edit: wipe the message being replaced and everything after it
	// (no branching history) before persisting the new/unchanged content.
	if msg.EditFromID != 0 {
		if err := s.db.DeleteMessagesFrom(threadID, msg.EditFromID); err != nil {
			s.db.LogEvent(threadID, "error", "turn", "deleting messages for edit/retry failed", map[string]interface{}{"err": err.Error()})
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
			s.db.LogEvent(threadID, "error", "turn", "creating thread failed", map[string]interface{}{"err": err.Error()})
			send(ServerEvent{Type: "error", Message: err.Error()})
			return
		}
	}

	history, err := s.loadHistory(threadID)
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "loading history failed", map[string]interface{}{"err": err.Error()})
		send(ServerEvent{Type: "error", Message: err.Error()})
		return
	}

	// Persist the user message before running the agent, not after — so
	// it (and its ID, needed for retry/edit) survives even if the turn
	// below errors out. Previously a failed turn left no record at all.
	// SttCostUSD folds in push-to-talk transcription cost, if this
	// message originated from a voice memo.
	userMsgID, err := s.db.AddMessage(threadID, "user", msg.Content, "[]", "[]", msg.SttCostUSD)
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "persisting user message failed", map[string]interface{}{"err": err.Error()})
		send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
		return
	}
	send(ServerEvent{Type: "user_message", ThreadID: threadID, UserMessageID: userMsgID})

	s.db.LogEvent(threadID, "info", "turn", "turn started", map[string]interface{}{
		"model":         modelCfg.ID,
		"is_new_thread": isNewThread,
		"voice_mode":    msg.VoiceMode,
		"is_retry":      msg.EditFromID != 0,
	})

	// emit both streams the event to the browser (send) and, for the
	// subset worth keeping as durable evidence, persists it to the events
	// table — "token"/"reasoning" are deliberately excluded: they arrive
	// as dozens-to-hundreds of small chunks per turn, and the assembled
	// final answer is already persisted in full as the assistant message.
	emit := func(eventType string, payload map[string]interface{}) {
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
		send(evt)
		s.logTurnEvent(threadID, eventType, evt)
	}

	agentCtx := &tools.Context{
		SearXNG:         s.searxng,
		Foursquare:      s.foursquare,
		DefaultLocation: cfg.DefaultLocation,
		VoiceMode:       msg.VoiceMode,
		LLM:             client,
		Emit:            emit,
		MaxTurns:        cfg.MaxAgentTurns,
	}

	result, err := agent.Run(ctx, agentCtx, history, msg.Content)
	if err != nil {
		s.db.LogEvent(threadID, "error", "turn", "turn failed", map[string]interface{}{"err": err.Error(), "model": modelCfg.ID})
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
			s.db.LogEvent(threadID, "warn", "suggestions", "follow-up suggestions failed", map[string]interface{}{"err": err.Error()})
		} else {
			suggestions = sug
			result.CostUSD += sugCost
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
		if title, titleCost, err := s.generateTitle(cfg, modelCfg, msg.Content, result.Answer); err != nil {
			log.Warn("thread title generation failed", "thread", threadID, "err", err)
			s.db.LogEvent(threadID, "warn", "title", "thread title generation failed", map[string]interface{}{"err": err.Error()})
		} else if title != "" {
			if err := s.db.SetThreadTitle(threadID, title); err != nil {
				log.Warn("failed to persist generated thread title", "err", err)
				s.db.LogEvent(threadID, "warn", "title", "persisting generated title failed", map[string]interface{}{"err": err.Error()})
			} else {
				result.CostUSD += titleCost
				s.db.LogEvent(threadID, "info", "title", "thread title generated", map[string]interface{}{"title": title, "cost_usd": titleCost})
			}
		}
	}

	citationsJSON, _ := json.Marshal(result.Citations)
	assistantMsgID, err := s.db.AddMessage(threadID, "assistant", result.Answer, string(citationsJSON), string(suggestionsJSON), result.CostUSD)
	if err != nil {
		log.Warn("failed to persist assistant message", "err", err)
		s.db.LogEvent(threadID, "error", "turn", "persisting assistant message failed", map[string]interface{}{"err": err.Error()})
	}

	if err := s.db.SetContextTokens(threadID, result.ContextTokens); err != nil {
		log.Warn("failed to record context tokens", "err", err)
		s.db.LogEvent(threadID, "warn", "turn", "recording context tokens failed", map[string]interface{}{"err": err.Error()})
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
			s.db.LogEvent(threadID, "warn", "compaction", "auto-compaction failed", map[string]interface{}{"err": err.Error()})
		} else {
			contextTokens = estimateTokens(summary)
			send(ServerEvent{Type: "compacted", ThreadID: threadID, Content: summary})
			s.db.LogEvent(threadID, "info", "compaction", "thread auto-compacted", map[string]interface{}{
				"through_message_id": assistantMsgID,
				"cost_usd":           compactCost,
				"summary":            summary,
			})
			result.CostUSD += compactCost
		}
	}

	// Total cost added to the thread this turn: the agent's LLM/tool
	// spend plus any STT cost from a voice memo, plus compaction's own
	// cost if it just ran, plus follow-up suggestions — all persisted
	// above, so the frontend's running total should reflect all of them.
	totalCost := result.CostUSD + msg.SttCostUSD
	s.db.LogEvent(threadID, "info", "turn", "turn completed", map[string]interface{}{
		"model":          modelCfg.ID,
		"cost_usd":       totalCost,
		"context_tokens": contextTokens,
		"citations":      len(result.Citations),
		"stopped":        ctx.Err() != nil,
	})

	send(ServerEvent{
		Type:          "done",
		ThreadID:      threadID,
		UserMessageID: userMsgID,
		Citations:     result.Citations,
		CostUSD:       totalCost,
		ContextTokens: contextTokens,
		Suggestions:   suggestions,
	})
}

// logTurnEvent persists the subset of streamed turn events worth keeping
// as durable evidence — thinking steps and tool calls/results, so "what
// happened during this turn" survives even if the process crashed before
// the turn finished normally. Errors surfaced mid-stream (a tool
// dispatch failure, still wrapped as a normal "tool_result" whose result
// string starts with "error:") are logged at warn instead of info so they
// stand out when scanning a thread's event history.
func (s *Server) logTurnEvent(threadID, eventType string, evt ServerEvent) {
	switch eventType {
	case "thinking":
		s.db.LogEvent(threadID, "info", "turn", "thinking", map[string]interface{}{"content": evt.Content})
	case "tool_call":
		s.db.LogEvent(threadID, "info", "tool."+evt.Tool, "tool call started", map[string]interface{}{"args": evt.Args})
	case "tool_result":
		level := "info"
		if strings.HasPrefix(evt.Result, "error:") {
			level = "warn"
		}
		s.db.LogEvent(threadID, level, "tool."+evt.Tool, "tool call finished", map[string]interface{}{"result": evt.Result})
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
	sugClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 150).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})

	prompt := []llm.ChatMessage{
		{Role: "system", Content: "Based on this question-and-answer exchange, suggest exactly 3 short, " +
			"natural follow-up questions the user might ask next. One per line, no numbering, no quotes, " +
			"no preamble or extra commentary. Each question must be a single short line — never a paragraph, " +
			"never an actual answer to anything."},
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: answer},
	}

	resp, err := sugClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return nil, 0, err
	}

	var suggestions []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = suggestionListPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		line = strings.Trim(line, "\"")
		if line == "" || len(line) > maxSuggestionLen {
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

// generateTitle asks for a short thread title based on the exchange that
// just finished — one extra cheap, non-streamed LLM call, same pattern
// as generateSuggestions/compactThread. Only called once, right after a
// brand-new thread's first turn (see handleTurn's isNewThread gate);
// deliberately given both the question and the answer, not just the
// question, since a vague opener ("help me with this") often only
// reveals what the thread is actually about once the answer (and
// whatever it found via search) comes back.
func (s *Server) generateTitle(cfg *config.Config, modelCfg config.ModelConfig, userMessage, answer string) (string, float64, error) {
	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 60).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})

	prompt := []llm.ChatMessage{
		{Role: "system", Content: "Based on this question-and-answer exchange, write a short thread title " +
			"summarizing what it's about — 3 to 6 words, plain text, no quotes, no trailing punctuation, " +
			"no preamble or extra commentary. Title Case is fine but not required."},
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: answer},
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
