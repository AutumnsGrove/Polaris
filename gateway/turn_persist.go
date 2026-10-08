package gateway

import (
	"encoding/json"

	"polaris/llm"
)

// persistAnswer writes the assistant message and every post-hoc field that
// hangs off it (context tokens, timing, Oracle result, transcript, cards,
// chart, pending question). Returns false when the message itself couldn't
// be saved, since "done" would then be a lie.
func (t *turnRun) persistAnswer() bool {
	citationsJSON, err := json.Marshal(t.result.Citations)
	if err != nil {
		log.Warn("failed to marshal citations, persisting message without them", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "marshaling citations failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		citationsJSON = []byte("[]")
	}
	// Suggestions start empty and are filled in by a post-hoc UPDATE once
	// generateSuggestions returns — see the call after "done" ships below.
	assistantMsgID, err2 := t.s.db.AddMessage(t.storageThreadID, "assistant", t.result.Answer, string(citationsJSON), "[]", t.result.CostUSD, t.turnID)
	t.assistantMsgID = assistantMsgID
	if err2 != nil {
		// The answer was already fully streamed live via "token" events —
		// the browser has it. But "done" (see protocol.go's doc comment)
		// means "persisted, safe to re-enable input assuming this thread
		// can be reopened with it intact" — sending it here would be a lie:
		// reopening this thread would show the question with no reply, and
		// its cost would never be added to the thread's running total.
		// Surfacing an explicit error instead tells the user their answer
		// exists only in this live view and won't survive a reload.
		log.Warn("failed to persist assistant message", "err", err2)
		t.logEvent(t.storageThreadID, "error", "turn", "persisting assistant message failed", map[string]interface{}{"err": err2.Error()}, t.turnID)
		t.send(ServerEvent{
			Type:          "error",
			ThreadID:      t.threadID,
			UserMessageID: t.userMsgID,
			Message:       "Your answer was generated but couldn't be saved — copy it now if you need it, then try again.",
		})
		return false
	}
	// See the matching call above (and TouchUpdatedAt's doc comment) —
	// same gap, same fix, for the assistant reply's own write.
	if err := t.s.db.TouchUpdatedAt(t.threadID); err != nil {
		log.Warn("failed to bump thread recency", "thread", t.threadID, "err", err)
	}

	if err := t.s.db.SetContextTokens(t.storageThreadID, t.result.ContextTokens); err != nil {
		log.Warn("failed to record context tokens", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording context tokens failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	if err := t.s.db.SetMessageDuration(t.assistantMsgID, t.durationMs); err != nil {
		log.Warn("failed to record message duration", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording message duration failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	t.ttftMs, t.tokensPerSecond, t.toolCallCount = t.emitter.turnStats(t.turnStart)
	if err := t.s.db.SetMessageTurnStats(t.assistantMsgID, t.ttftMs, t.tokensPerSecond, t.toolCallCount); err != nil {
		log.Warn("failed to record turn stats", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording turn stats failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	// Written unconditionally (unlike oracle_result below, which only
	// persists when Oracle actually got a live answer) — a turn's applied
	// focus mode is meaningful even with Oracle off/quiet this turn, and
	// the frontend's margin note needs it either way (see the schema
	// comment above messages.applied_focus_mode).
	if err := t.s.db.SetMessageAppliedFocusMode(t.assistantMsgID, t.appliedFocusMode); err != nil {
		log.Warn("failed to record applied focus mode", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording applied focus mode failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}
	if err := t.s.db.SetMessageAppliedModel(t.assistantMsgID, t.requestedModel); err != nil {
		log.Warn("failed to record applied model", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording applied model failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	// oracleResult.Checks is only non-empty when Oracle actually got a
	// live Jev answer this turn (RunOracle returns early, before
	// appending anything, on every off/unconfigured/error/timeout path)
	// — persisting only then keeps oracle_result "" for every ordinary
	// Oracle-off turn, matching its schema comment, rather than storing
	// an empty-but-marshaled "{}".
	if len(t.oracleResult.Checks) > 0 {
		if oracleResultJSON, err := json.Marshal(t.oracleResult); err != nil {
			log.Warn("failed to marshal oracle result", "err", err)
		} else if err := t.s.db.SetMessageOracleResult(t.assistantMsgID, string(oracleResultJSON), t.oracleFocusModeSource); err != nil {
			log.Warn("failed to record oracle result", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "recording oracle result failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
		if t.oracleResult.CostUSD > 0 {
			if err := t.s.db.AddTurnCost(t.storageThreadID, t.assistantMsgID, "oracle", t.oracleResult.CostUSD); err != nil {
				log.Warn("failed to record oracle cost", "err", err)
			}
		}
	}
	// Oracle timed out or errored (no checks to persist), but the turn still
	// carried a focus mode whose provenance the next turn's sticky/retract
	// logic reads back — losing it here would make an Oracle-set mode look
	// like a plain default after one Jev hiccup.
	if len(t.oracleResult.Checks) == 0 && t.oracleFocusModeSource != "" {
		if err := t.s.db.SetMessageFocusModeSource(t.assistantMsgID, t.oracleFocusModeSource); err != nil {
			log.Warn("failed to record focus mode source", "err", err)
		}
	}

	// The turn's exact wire messages, so the next turn replays them
	// verbatim — see loadHistory. The closing assistant message is
	// synced to the answer actually persisted above (StripFakeSourcesNote
	// may have trimmed it): that message was generated, never sent, so
	// no cached prefix depends on its original bytes. A failure here
	// isn't fatal: loadHistory rebuilds a transcript-less turn the
	// legacy way.
	if n := len(t.result.Transcript); n > 0 {
		t.result.Transcript[n-1].Content = t.result.Answer
		if transcriptJSON, err := llm.EncodeTranscript(t.result.Transcript); err != nil {
			log.Warn("failed to encode turn transcript", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "encoding transcript failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		} else if err := t.s.db.SetMessageTranscript(t.assistantMsgID, transcriptJSON); err != nil {
			log.Warn("failed to record turn transcript", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "recording transcript failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}

	if err := t.s.db.SetMessageCacheUsage(t.assistantMsgID, t.result.PromptTokens, t.result.CacheReadTokens); err != nil {
		log.Warn("failed to record cache usage", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording cache usage failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}
	if err := t.s.db.SetMessageCompletionTokens(t.assistantMsgID, t.result.CompletionTokens); err != nil {
		log.Warn("failed to record completion tokens", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording completion tokens failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}
	if err := t.s.db.SetMessageCallStats(t.assistantMsgID, t.result.LastPromptTokens, t.result.LLMCalls); err != nil {
		log.Warn("failed to record call stats", "err", err)
		t.logEvent(t.storageThreadID, "warn", "turn", "recording call stats failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	if len(t.result.Cards) > 0 {
		if cardsJSON, err := json.Marshal(t.result.Cards); err != nil {
			log.Warn("failed to marshal cards, message persisted without them", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "marshaling cards failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		} else if err := t.s.db.SetMessageCards(t.assistantMsgID, string(cardsJSON)); err != nil {
			log.Warn("failed to record cards", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "recording cards failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}

	if t.result.Chart != nil {
		if chartJSON, err := json.Marshal(t.result.Chart); err != nil {
			log.Warn("failed to marshal chart, message persisted without it", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "marshaling chart failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		} else if err := t.s.db.SetMessageChart(t.assistantMsgID, string(chartJSON)); err != nil {
			log.Warn("failed to record chart", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "recording chart failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}

	if t.result.PendingQuestion != nil {
		if pendingJSON, err := json.Marshal(t.result.PendingQuestion); err != nil {
			log.Warn("failed to marshal pending question, message persisted without it", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "marshaling pending question failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		} else if err := t.s.db.SetMessagePendingQuestion(t.assistantMsgID, string(pendingJSON)); err != nil {
			log.Warn("failed to record pending question", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "recording pending question failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}
	return true
}

// finishTurn logs completion and sends "done" with the turn's totals.
func (t *turnRun) finishTurn() {
	// Auto-compact once this thread crosses the configured threshold: the
	// model summarizes everything covered so far, and future turns build
	// history from that summary instead of the full raw text. The
	// messages table itself is untouched — only what gets sent back to
	// the LLM shrinks, the visible transcript stays the true record.
	// Decided here, fired in the detached goroutine after "done" below —
	// see that goroutine for why the call itself no longer runs inline.
	contextTokens := t.result.ContextTokens
	t.needsCompaction = t.result.ContextTokens >= t.cfg.CompactionThreshold(t.modelCfg)

	// Total cost added to the thread this turn: the agent's LLM/tool spend
	// plus any STT cost from a voice memo. Note what is NOT here any more —
	// compaction's own cost. It is still persisted to both ledgers by
	// CompactThread, but it is spent by a detached call that outlives this
	// turn, so folding it into this turn's totalCost would put money in the
	// "done" event for a call that hadn't run yet. It ships instead on the
	// "compacted" event of the thread's next turn, the same delayed-carrier
	// shape as "suggestions" below.
	totalCost := t.result.CostUSD + t.msg.SttCostUSD
	t.logEvent(t.storageThreadID, "info", "turn", "turn completed", map[string]interface{}{
		"model":          t.modelCfg.ID,
		"cost_usd":       totalCost,
		"context_tokens": contextTokens,
		"citations":      len(t.result.Citations),
		"stopped":        t.ctx.Err() != nil,
		// Summed across the turn's LLM calls — see agent.Result. The
		// before/after evidence for docs/plans/verbatim-turn-transcripts.md.
		"prompt_tokens":     t.result.PromptTokens,
		"cache_read_tokens": t.result.CacheReadTokens,
	}, t.turnID)

	// oracleResultForEvent was filled in above, right before the early
	// "oracle" event — only non-nil when Oracle actually got a live answer
	// this turn, mirroring the persistence condition above, so a reload
	// (oracle_result "") and the live events agree on what "Oracle didn't
	// run" looks like.
	t.send(ServerEvent{
		Type:               "done",
		ThreadID:           t.threadID,
		UserMessageID:      t.userMsgID,
		AssistantMessageID: t.assistantMsgID,
		Citations:          t.result.Citations,
		Cards:              t.result.Cards,
		Chart:              t.result.Chart,
		// Oracle's Jev spend is already on this message/thread in the DB
		// (AddTurnCost above), so the live total must carry it too or the
		// thread's running cost only catches up on reload.
		CostUSD:               totalCost + t.oracleResult.CostUSD,
		ContextTokens:         contextTokens,
		DurationMs:            t.durationMs,
		PromptTokens:          t.result.PromptTokens,
		CacheReadTokens:       t.result.CacheReadTokens,
		CompletionTokens:      t.result.CompletionTokens,
		LastPromptTokens:      t.result.LastPromptTokens,
		LLMCalls:              t.result.LLMCalls,
		PendingQuestion:       t.result.PendingQuestion,
		OracleResult:          t.oracleResultForEvent,
		OracleFocusModeSource: t.oracleFocusModeSource,
		AppliedFocusMode:      t.appliedFocusMode,
		AppliedModel:          t.requestedModel,
		CostAnswerUSD:         totalCost,
		// CostVerificationUSD deliberately left at its zero value: that
		// spend isn't known yet (verification runs in a detached goroutine
		// after this event ships, adding to cost_verification_usd
		// separately) — same "reload to see it" gap that already existed
		// for verification cost before this three-tier split.
		CostOracleUSD:   t.oracleResult.CostUSD,
		TTFTMs:          t.ttftMs,
		TokensPerSecond: t.tokensPerSecond,
		ToolCallCount:   t.toolCallCount,
	})
}
