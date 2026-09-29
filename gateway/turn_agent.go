package gateway

import (
	"time"

	"polaris/agent"
	"polaris/llm"
	"polaris/store"
)

// runAgent runs the agent loop and reports a failed or empty turn to the
// client. Returns false when there is no answer to persist.
func (t *turnRun) runAgent() bool {
	// Timed around agent.Run specifically, not the whole handler — this is
	// "how long it took to get an answer", the number a user watching the
	// tokens stream in actually cares about. Excludes the follow-up
	// suggestions/title-generation calls after it, which run invisibly
	// (the answer's already fully rendered) and would otherwise inflate a
	// short answer's reported time with unrelated background work.
	// image_search's numbered candidates persist per thread: the model's
	// history keeps earlier turns' numbered lists, so "show images 2 and 3"
	// on a follow-up turn must still resolve (issue #124). Saved even when
	// the turn errors or is stopped — a partial run's numbers may already
	// be in history.
	loadImageCandidates(t.s.db, t.storageThreadID, t.agentCtx)
	t.turnStart = time.Now()
	result, err := agent.Run(t.ctx, t.agentCtx, t.history, t.turnMessage)
	t.result = result
	saveImageCandidates(t.s.db, t.storageThreadID, t.agentCtx)
	t.durationMs = time.Since(t.turnStart).Milliseconds()
	// Catches a reasoning burst still open when the turn ended — normally
	// the final answer's "token" events already triggered this via emit
	// above, but a turn that errored or was stopped mid-reasoning
	// wouldn't have reached that point.
	t.emitter.flushReasoning()
	if err != nil {
		// agent.Run still returns a non-nil result carrying whatever cost
		// the turn accrued before the failing call — real, billed LLM
		// calls happened even though the turn ultimately errored, so this
		// records that spend in the event log instead of silently losing
		// it (there's no assistant message row to attach it to here, since
		// the turn never produced an answer). Not folded into per-thread
		// cost stats — that's a separate design question about where a
		// failed turn's spend should live in aggregate reporting, not
		// solved by this log line.
		var partialCost float64
		if t.result != nil {
			partialCost = t.result.CostUSD
		}
		t.logEvent(t.storageThreadID, "error", "turn", "turn failed", map[string]interface{}{"err": err.Error(), "model": t.modelCfg.ID, "cost_usd": partialCost}, t.turnID)
		errorKind := ""
		if llm.IsNetworkError(err) {
			errorKind = "network"
		}
		t.send(ServerEvent{Type: "error", ThreadID: t.threadID, UserMessageID: t.userMsgID, Message: err.Error(), ErrorKind: errorKind})
		return false
	}
	// No error, but no answer either — same reasoning-exhaustion failure
	// mode as generateTitle/generateSuggestions (see generateTitle's doc
	// comment), except here it's the primary answer, not a side call: a
	// reasoning model that spends its whole completion budget on hidden
	// reasoning tokens returns empty visible content with no error at all.
	// Left unchecked, this used to fall straight through to AddMessage
	// below and persist a blank assistant turn — no error shown, no log
	// trace, just a silently empty reply. ctx.Err() == nil excludes a
	// user-initiated stop hit before any token streamed, which is a
	// legitimate empty answer, not this failure mode.
	if t.ctx.Err() == nil && t.result.Answer == "" {
		const emptyAnswerMessage = "The model didn't return an answer — it may have spent its whole response budget on " +
			"internal reasoning. Try again, or switch models."
		log.Warn("turn produced an empty answer", "thread", t.threadID, "model", t.modelCfg.ID)
		t.logEvent(t.storageThreadID, "warn", "turn", "model returned an empty answer", map[string]interface{}{"model": t.modelCfg.ID}, t.turnID)
		t.send(ServerEvent{Type: "error", ThreadID: t.threadID, UserMessageID: t.userMsgID, Message: emptyAnswerMessage})
		return false
	}
	return true
}

// stripAndTitle cleans the answer of any imitated sources note and, on a
// thread's first turn, replaces the placeholder title with a real one.
func (t *turnRun) stripAndTitle() {
	// Seen live (issue found via a real potato thread, full_turn_history
	// on): once a thread's replayed history carries a few of
	// appendCitedSources' injected "[Polaris note...]" blocks, the model
	// sometimes imitates the exact format in its own answer — despite
	// prompt.md explicitly telling it not to. A prompt instruction alone
	// isn't a reliable enough guard against a model copying a formatting
	// convention it's been shown repeatedly in-context, so this strips any
	// such trailing block mechanically before the answer is used for
	// anything (persistence, title/suggestion generation, verification) —
	// belt-and-suspenders alongside the prompt instruction, not a
	// replacement for it.
	t.result.Answer = store.StripFakeSourcesNote(t.result.Answer)

	// One-time LLM-generated thread title, replacing the truncated
	// placeholder set above — on a brand-new thread's first turn, or on
	// an edit/retry of that first turn (isFirstMessageEdit), since in
	// both cases the title needs to describe a question that's only just
	// been asked. Never regenerated on any other turn — a manual rename,
	// or just leaving the placeholder, both take precedence there. Same
	// completion-gating as suggestions: skip on a stopped generation or
	// an empty answer, where the placeholder is already the more
	// sensible title anyway.
	if (t.isNewThread || t.isFirstMessageEdit) && t.ctx.Err() == nil && t.result.Answer != "" {
		if t.msg.PulsarRoutineID != 0 {
			// Deterministic, not an LLM call — see the plan doc's "Pulse
			// execution model": every pulse from the same routine starts
			// from a near-identical prompt, so the normal LLM-generated
			// title (which reads the opening question) would produce a
			// wall of near-duplicate titles down a routine's pulse
			// history. Routine name + the run's own date is exactly
			// enough to differentiate them (the plan doc's own example:
			// "Daily news — Sept 4") without generateTitle's failure
			// modes (an empty completion leaving the placeholder
			// forever) or its cost.
			title := t.msg.PulsarRoutineName + " — " + time.Now().Format("Jan 2")
			if len(title) > maxThreadTitleLen {
				title = title[:maxThreadTitleLen]
			}
			if err := t.s.db.SetThreadTitle(t.threadID, title); err != nil {
				log.Warn("failed to persist pulse title", "thread", t.threadID, "err", err)
				t.logEvent(t.storageThreadID, "warn", "title", "persisting pulse title failed", map[string]interface{}{"err": err.Error()}, t.turnID)
			}
		} else if title, titleCost, err := t.s.generateTitle(t.cfg, t.modelCfg, firstNonEmpty(t.msg.TitleSeed, t.msg.Content), t.isWeaverThread); err != nil {
			log.Warn("thread title generation failed", "thread", t.threadID, "err", err)
			t.logEvent(t.storageThreadID, "warn", "title", "thread title generation failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		} else if title != "" {
			if err := t.s.db.SetThreadTitle(t.threadID, title); err != nil {
				log.Warn("failed to persist generated thread title", "err", err)
				t.logEvent(t.storageThreadID, "warn", "title", "persisting generated title failed", map[string]interface{}{"err": err.Error()}, t.turnID)
			} else {
				t.result.CostUSD += titleCost
				t.logEvent(t.storageThreadID, "info", "title", "thread title generated", map[string]interface{}{"title": title, "cost_usd": titleCost}, t.turnID)
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
			t.logEvent(t.storageThreadID, "warn", "title", "model returned no usable title", nil, t.turnID)
		}
	}
}
