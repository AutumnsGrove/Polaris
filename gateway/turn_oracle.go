package gateway

import (
	"strings"

	"polaris/prompts"
)

// runOracleStage runs Oracle mode's opt-in pre-read of the message, letting
// its focus-mode pick and injected guidance shape the turn before the agent
// starts. It leaves every t.oracle* field describing what happened.
func (t *turnRun) runOracleStage() {
	// Oracle mode (docs/plans/oracle-mode.md, issue #122) — an opt-in
	// pre-read of this message via Jev, run before agent.Run so its focus-
	// mode pick and injected guidance can shape the turn itself. Left off
	// for a ghost thread, same "no extra background intelligence" reasoning
	// as memory/stars/search_chats above. oracleResult/oracleFocusModeSource
	// are captured in this outer scope for persisting once assistantMsgID
	// exists, after agent.Run returns below.
	// oracleAttempted records that Oracle actually got to run (Jev
	// configured, setting on, within budget) — the gate for the early
	// "oracle" event below, which must not fire for a turn Oracle never
	// touched. oracleResultForEvent is shared with the "done" event further
	// down so its checks-empty-means-nil rule lives in exactly one place.
	// Ghost threads are normally skipped entirely (issue #67's "no extra
	// background intelligence"), but the operator can opt back in with the
	// separate oracle_ghost_enabled setting: Oracle still classifies the
	// message and can pick a focus mode/inject guidance, while the offer
	// chips that create something permanent (pulsar/daily/field) stay
	// withheld — OracleInput.Ghost carries that distinction into RunOracle,
	// and the field-options read below is skipped for a ghost turn so no
	// store read leaks in.
	oracleInGhost := t.ghost && OracleGhostEnabledFromStore(t.s.db)
	if (!t.ghost || oracleInGhost) && !t.msg.NoOracle && OracleEnabledFromStore(t.s.db) && t.s.jev != nil {
		withinOracleBudget := true
		if used, err := t.s.db.JevCostThisMonth(); err != nil {
			log.Warn("oracle: checking jev monthly cost failed, proceeding anyway", "err", err)
		} else if used >= jevMonthlyCapUSD {
			withinOracleBudget = false
		}
		if withinOracleBudget {
			isManualFocus := t.msg.FocusModeSource != "default"
			isFirstMsg, err := t.s.db.IsFirstMessage(t.storageThreadID, t.userMsgID)
			if err != nil {
				log.Warn("oracle: checking first-message status failed", "thread", t.storageThreadID, "err", err)
			}
			priorOracleFocus := ""
			if source, err := t.s.db.LastAssistantFocusModeSource(t.storageThreadID); err == nil && source == "oracle" {
				if thread, err := t.s.db.GetThread(t.threadID); err == nil {
					priorOracleFocus = thread.FocusMode
				}
			}
			// The field chip is only worth offering for a thread that isn't
			// already in a Field — fieldID is final here (a new thread born
			// inside a Field from the composer picker has it set too).
			var fieldOptions, fieldIDs map[string]string
			if t.fieldID == "" && !t.ghost {
				if fields, err := t.s.db.ListFields(); err != nil {
					log.Warn("oracle: listing fields for the field chip failed, skipping it", "err", err)
				} else {
					fieldOptions, fieldIDs = OracleFieldOptions(fields)
				}
			}
			t.oracleAttempted = true
			t.oracleResult = RunOracle(t.ctx, t.s.jev, OracleInput{
				CurrentMessage:       t.msg.Content,
				PrevUserMessage:      t.prevUserMessage,
				IsFirstMessage:       isFirstMsg,
				ActiveFocusMode:      t.msg.FocusMode,
				IsManualFocus:        isManualFocus,
				PriorOracleFocusMode: priorOracleFocus,
				FieldOptions:         fieldOptions,
				FieldIDs:             fieldIDs,
				Ghost:                t.ghost,
				ThreadSource:         t.threadSource,
				// agentCtx.Visuals is already "" for a voice call or an entry
				// point that never offers blocks, so the ui check follows the
				// exact same gate as the base prompt fragment.
				Visuals: t.agentCtx.Visuals,
				Rules:   t.cfg.Oracle,
			})
			// Recorded on the shared Jev ledger the monthly cap sums (issue
			// #125) — only when the call actually completed and billed, the
			// same convention verification.go follows. CostUSD stays zero on
			// a timeout/error, so nothing is logged for a call that never
			// returned.
			if t.oracleResult.CostUSD > 0 {
				if err := t.s.db.LogOracleJevCost(t.oracleResult.CostUSD); err != nil {
					log.Warn("oracle: logging jev cost failed", "err", err)
				}
			}
			// FocusCleared is Oracle retracting a mode it set earlier (see
			// resolveFocus) — the turn runs with no mode at all, and the
			// thread's sticky focus_mode is cleared to match. Handled in the
			// same branch as a pick so both count as "Oracle acted" for the
			// frontend (oracleFocusModeSource == "oracle"), which is what
			// lets the composer badge clear itself as well as set itself.
			if !isManualFocus && (t.oracleResult.FocusMode != "" || t.oracleResult.FocusCleared) {
				appliedOracleFocus := t.oracleResult.FocusMode
				if t.oracleResult.FocusCleared {
					appliedOracleFocus = ""
				}
				t.agentCtx.FocusMode = appliedOracleFocus
				t.oracleFocusModeSource = "oracle"
				// Persists Oracle's own decision as the thread's new sticky
				// focus_mode — either the pick, or "" when Oracle cleared a
				// mode it had set. The earlier SetThreadConfig call above
				// (this function's very first write) only ever wrote
				// msg.FocusMode (whatever the client sent), since it runs
				// before Oracle does. Without this second write,
				// threads.focus_mode never actually reflects an Oracle
				// decision, and LastAssistantFocusModeSource/
				// PriorOracleFocusMode above silently degrades to always
				// empty — breaking the focus check's own sticky/
				// switch_threshold rules (resolveFocus in oracle.go), which
				// depend on knowing what Oracle itself decided last turn.
				// appliedOracleFocus (not OracleResult.FocusMode directly),
				// since a clear must write "" rather than leave the old mode.
				// Best-effort, same as every other post-hoc store write in
				// this handler.
				if err := t.s.db.SetThreadConfig(t.threadID, t.modelCfg.ID, appliedOracleFocus, t.msg.DeepResearch, t.msg.NoResearch); err != nil {
					log.Warn("failed to persist oracle's focus decision as thread config", "thread", t.threadID, "err", err)
				}
			} else if t.msg.FocusMode != "" {
				t.oracleFocusModeSource = carriedFocusModeSource(isManualFocus, t.msg.FocusMode, priorOracleFocus)
			}
			if len(t.oracleResult.Injections) > 0 {
				t.agentCtx.OracleSection = strings.ReplaceAll(prompts.Get().Oracle.Section, "{items}", strings.Join(t.oracleResult.Injections, "\n\n"))
			}
		}
	}
	// Captured once agentCtx.FocusMode has its final value (Oracle's own
	// pick, if applied, otherwise msg.FocusMode) — agent.Run only ever
	// reads this field, never mutates it, so this is stable for the rest
	// of the turn. Persisted post-hoc below alongside the other per-turn
	// stats, once assistantMsgID exists.
	t.appliedFocusMode = t.agentCtx.FocusMode
}

// announceOracle sends Oracle's verdict live, ahead of the answer, so the
// composer updates the moment Oracle resolves.
func (t *turnRun) announceOracle() {
	// Oracle mode: its whole verdict — every check's winner, the focus mode
	// it did or didn't apply, its own cost — is known right here, before
	// agent.Run has even been called (RunOracle runs synchronously above).
	// Sending it now rather than waiting for "done" is what lets the
	// composer's focus badge and its "reading" ring update the moment
	// Oracle actually resolves, instead of seconds later when the answer
	// finishes streaming (the answer's first token can lag Oracle by many
	// seconds). "done" still carries the same fields again — that copy is
	// the persisted one a reload replays — so this is a live-only early
	// duplicate, never persisted, exactly like "cost_update" (see
	// protocol.go's "oracle" doc comment). Absent entirely for a turn
	// Oracle never ran on; the frontend treats that as normal.
	if t.oracleAttempted {
		if len(t.oracleResult.Checks) > 0 {
			t.oracleResultForEvent = &t.oracleResult
		}
		t.send(ServerEvent{
			Type:                  "oracle",
			ThreadID:              t.threadID,
			OracleResult:          t.oracleResultForEvent,
			OracleFocusModeSource: t.oracleFocusModeSource,
			AppliedFocusMode:      t.appliedFocusMode,
			CostOracleUSD:         t.oracleResult.CostUSD,
		})
	}
}

// carriedFocusModeSource labels a focus mode Oracle didn't pick this turn.
// A mode the operator picked is "manual"; one that's merely riding along as
// the composer's standing value is "default" — except when that value is
// exactly the mode Oracle itself set earlier in this thread (the frontend
// re-sends the thread's sticky mode on every message, flagged as
// non-manual). That case must stay "oracle": recording it as "default"
// made LastAssistantFocusModeSource forget after a single quiet turn that
// the mode was Oracle's, silently disabling sticky/switch_threshold and
// Oracle's ability to retract its own pick on any later message.
func carriedFocusModeSource(isManualFocus bool, focusMode, priorOracleFocus string) string {
	switch {
	case isManualFocus:
		return "manual"
	case priorOracleFocus != "" && focusMode == priorOracleFocus:
		return "oracle"
	default:
		return "default"
	}
}
