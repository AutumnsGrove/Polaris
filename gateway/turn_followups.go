package gateway

import (
	"encoding/json"
)

// The three detached, post-"done" passes. Each is routed through
// spawnBackground so a live WebSocket connection can wait on it before
// cleaning up an abandoned ghost thread.

// spawnCompaction summarizes the thread in the background once it crosses
// the context threshold.
func (t *turnRun) spawnCompaction() {
	// Auto-compaction, detached for the same reason as the suggestions
	// goroutine just below: it is a second full LLM round-trip the user
	// never asked to watch, and running it inline made every
	// threshold-crossing turn — already the slowest ones — pay it before
	// the input re-enabled. Mandatory recover() for the same reason too:
	// this runs outside any net/http stack that would catch a panic.
	//
	// Nothing is announced from here, and that is the whole point of the
	// pending-notice dance. There may be no live client at all (a POST
	// /api/ask turn, an Atlas quick answer, a Pulsar pulse), and even when
	// there is, the turn this belongs to has already ended — a "compacted"
	// event now would land on a finished turn's timeline. So the completed
	// compaction arms a flag on the thread row instead (see
	// CompactThread/TakeCompactionNotice) and the thread's NEXT turn
	// announces it, with that turn's own turnID, which is what keeps live
	// and reloaded rendering identical. That next turn is also where the
	// cost finally reaches the frontend's running total.
	//
	// The context_tokens the user sees therefore lags by one turn: the
	// "done" above already reported this turn's real, pre-compaction count,
	// and nothing here can revise it. CompactThread has already written the
	// smaller estimate to the DB, so a reload in between shows the reduced
	// number early; either way the next turn's "done" carries a real count
	// measured against the compacted history, and the two converge.
	if t.needsCompaction {
		t.spawnBackground(func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic during auto-compaction", "thread", t.threadID, "panic", r)
				}
			}()
			if !t.s.tryBeginCompaction(t.storageThreadID) {
				// A compaction for this thread is already running — see
				// tryBeginCompaction's doc comment. Skipping is that
				// method's intended one-turn lag, not dropped work: this
				// thread is already being summarized, and the turn after
				// next will find it still over the threshold if the result
				// wasn't enough.
				return
			}
			defer t.s.endCompaction(t.storageThreadID)

			summary, compactCost, err := t.s.compactThread(t.client, t.storageThreadID, t.assistantMsgID)
			if err != nil {
				log.Warn("auto-compaction failed", "thread", t.threadID, "err", err)
				t.logEvent(t.storageThreadID, "warn", "compaction", "auto-compaction failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				return
			}
			// Logged with an empty turnID, deliberately. This row is the
			// durable "a compaction happened" record — store/stats.go counts
			// exactly these for the user-facing Auto-compactions figure, so
			// it must be written once per compaction whether or not anyone
			// is around to be told. It is untagged because
			// buildTimelineFromEvents only ever matches a turn's own slice:
			// tagging it here would render a second, duplicate note on the
			// *triggering* turn's timeline, contradicting the notice the
			// next turn shows. The rendered note is the "compaction notice
			// shown" row that turn writes (see the surface block above).
			t.logEvent(t.storageThreadID, "info", "compaction", "thread auto-compacted", map[string]interface{}{
				"through_message_id": t.assistantMsgID,
				"cost_usd":           compactCost,
				"summary":            summary,
			}, "")
		})
	}
}

// spawnSuggestions generates follow-up suggestions in the background.
func (t *turnRun) spawnSuggestions() {
	// Follow-up suggestions, Perplexity-style — generated in a detached
	// goroutine, after "done" already shipped, so the turn footer and
	// cost/duration appear the instant the real answer is ready instead
	// of stalling behind a second, invisible LLM call the user never
	// asked for. Detached, not just moved, because the caller's own
	// goroutine (see ws.go) releases this connection's "turn in flight"
	// guard via defer as soon as handleTurn returns — running this
	// inline would keep the connection locked for this call's duration
	// too, silently rejecting a fast follow-up message sent right after
	// "done". Skipped on a stopped generation (ctx.Err() != nil) since
	// suggesting where to go next from an answer the user just cut off
	// isn't useful. assistantMsgID/storageThreadID are already persisted
	// at this point, so this only ever does a post-hoc UPDATE, never
	// blocks the message existing. Also skipped whenever the answer
	// itself already ends in a question — not just the ask_user_question
	// case above, but any ordinary turn where the model asks its own
	// follow-up in plain prose instead of calling the tool. A row of
	// "questions you could ask next" directly under an assistant message
	// that's itself a question reads as more questions piling up, not
	// answer shortcuts — a real, confusing collision caught live.
	if t.ctx.Err() == nil && t.result.Answer != "" && t.result.PendingQuestion == nil && !answerEndsInQuestion(t.result.Answer) {
		t.spawnBackground(func() {
			// Same rationale as ws.go's turn goroutine: this runs outside
			// any call stack net/http recovers, so an unrecovered panic
			// here would take down the whole process instead of just
			// this one enrichment step.
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic generating follow-up suggestions", "thread", t.threadID, "panic", r)
				}
			}()
			sug, sugCost, err := t.s.generateSuggestions(t.cfg, t.modelCfg, t.msg.Content, t.result.Answer)
			if err != nil {
				log.Warn("follow-up suggestions failed", "thread", t.threadID, "err", err)
				t.logEvent(t.storageThreadID, "warn", "suggestions", "follow-up suggestions failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				return
			}
			if len(sug) == 0 {
				// No error, but nothing usable came back either — a
				// reasoning model can spend its whole completion budget
				// on hidden reasoning tokens and never reach visible
				// content (see generateTitle's doc comment for the real
				// case that surfaced this). Otherwise this failure mode
				// is completely silent: no error to log, no suggestions
				// to show, no trace in the event log to explain why.
				t.logEvent(t.storageThreadID, "warn", "suggestions", "model returned no usable suggestions", nil, t.turnID)
				return
			}
			suggestionsJSON, _ := json.Marshal(sug)
			if err := t.s.db.SetMessageSuggestions(t.assistantMsgID, string(suggestionsJSON)); err != nil {
				log.Warn("failed to persist follow-up suggestions", "err", err)
				t.logEvent(t.storageThreadID, "warn", "suggestions", "persisting follow-up suggestions failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				return
			}
			if err := t.s.db.AddTurnCost(t.storageThreadID, t.assistantMsgID, "answer", sugCost); err != nil {
				log.Warn("failed to record follow-up suggestions cost", "err", err)
			}
			t.send(ServerEvent{
				Type:        "suggestions",
				ThreadID:    t.threadID,
				CostUSD:     sugCost,
				Suggestions: sug,
			})
		})
	}
}

// spawnVerification runs the per-claim "found in source" pass, in the
// background or — for WaitVerification callers — synchronously.
func (t *turnRun) spawnVerification() {
	// Per-claim "found in source" verification — same detached,
	// post-"done" shape as follow-up suggestions above, for the same
	// reason (never stall the visible answer behind an extra async pass).
	if t.assistantMsgID != 0 && t.ctx.Err() == nil {
		runAndReportVerification := func() {
			// Guards this whole pass regardless of which path below calls
			// it — the synchronous (WaitVerification) call runs inside
			// handleAsk's normal net/http handler stack, which already
			// recovers a top-level panic on its own, but the detached
			// goroutine path does not (see ws.go's turn goroutine
			// needing its own recover for the exact same reason). Kept
			// here, once, rather than duplicated in both call sites below.
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic during verification", "thread", t.threadID, "panic", r)
				}
			}()
			before := t.agentCtx.JevSpentThisTurn()
			results := runVerification(t.agentCtx, t.result.Answer, t.result.Citations)
			if delta := t.agentCtx.JevSpentThisTurn() - before; delta > 0 {
				if err := t.s.db.AddTurnCost(t.storageThreadID, t.assistantMsgID, "verification", delta); err != nil {
					log.Warn("failed to record verification cost", "err", err)
				}
			}
			marks := filterSupportedMarks(results)
			if len(marks) > 0 {
				marksJSON, err := json.Marshal(marks)
				if err != nil {
					log.Warn("failed to marshal verification marks", "err", err)
					return
				}
				if err := t.s.db.SetMessageVerification(t.assistantMsgID, string(marksJSON)); err != nil {
					log.Warn("failed to persist verification marks", "err", err)
					t.logEvent(t.storageThreadID, "warn", "verification", "persisting verification marks failed", map[string]interface{}{"err": err.Error()}, t.turnID)
					return
				}
			}
			// Sent even when marks is empty when WaitVerification asked
			// for it — a debug/stress-testing caller needs to tell "ran,
			// found nothing" apart from "never ran at all", which a
			// missing event can't distinguish. A real chat turn (never
			// WaitVerification) keeps the old behavior of staying silent
			// when there's nothing to show.
			if len(marks) == 0 && !t.msg.WaitVerification {
				return
			}
			evt := ServerEvent{
				Type:               "verification",
				ThreadID:           t.threadID,
				AssistantMessageID: t.assistantMsgID,
				Verification:       marks,
			}
			if t.msg.WaitVerification {
				evt.VerificationDebug = results
			}
			t.send(evt)
		}
		if t.msg.WaitVerification {
			// Synchronous, not detached — handleAsk (ask.go) blocks on
			// handleTurn's return and only sees events sent before it
			// returns, so the debug/stress-testing path can't use the
			// normal post-"done" async goroutine below at all.
			runAndReportVerification()
		} else {
			t.spawnBackground(runAndReportVerification)
		}
	}
}
