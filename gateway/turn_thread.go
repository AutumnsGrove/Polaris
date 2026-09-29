package gateway

import (
	"polaris/llm"
)

// resolveStorageThread works out which thread this turn's messages are
// written into, forking one for an edit/retry. Returns false after telling
// the client why when any step fails.
func (t *turnRun) resolveStorageThread() bool {
	// storageThreadID is where this turn's messages/events actually get
	// persisted — threadID itself for a brand-new thread or a plain
	// continuation, but a freshly forked thread for an edit/retry. Every
	// DB write from here on uses storageThreadID; threadID stays reserved
	// for what the client sees (ServerEvent.ThreadID) and root-level
	// concerns (the title). Keeping the client-facing thread id stable
	// across edits/regenerates is the whole point: the sidebar entry, the
	// URL, ThreadMenu — none of it needs to know a fork ever happened.
	//
	// Retry/edit no longer deletes anything (the old DeleteMessagesFromAnd
	// AddMessage behavior) — the reply about to be replaced gets a
	// permanent home of its own first (ForkThread), so it stays reachable
	// afterward via the variant switcher instead of being gone for good.
	//
	// isFirstMessageEdit is checked against the pre-fork effective thread,
	// since nothing downstream can prove this was the thread's opening
	// question once ForkThread's copy has run. Only regenerating the title
	// in this case (not on every retry) matters because the title
	// describes the thread as a whole: editing turn 5 of an established
	// conversation shouldn't retitle it around just that turn, but editing
	// turn 1 means the question the current title was generated from
	// doesn't exist anymore.

	t.storageThreadID = t.threadID
	t.isFirstMessageEdit = false
	if !t.isNewThread {
		effectiveID, err := t.s.db.EffectiveThreadID(t.threadID)
		if err != nil {
			t.logEvent(t.threadID, "error", "turn", "resolving active variant failed", map[string]interface{}{"err": err.Error()}, t.turnID)
			t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
			return false
		}
		if t.msg.EditFromID != 0 {
			t.isFirstMessageEdit, err = t.s.db.IsFirstMessage(effectiveID, t.msg.EditFromID)
			if err != nil {
				t.logEvent(t.threadID, "error", "turn", "checking edit/retry position failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
				return false
			}
			atIndex, err := t.s.db.MessageIndex(effectiveID, t.msg.EditFromID)
			if err != nil {
				t.logEvent(t.threadID, "error", "turn", "locating edit/retry position failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
				return false
			}
			forkID, err := t.s.db.ForkThread(t.threadID, effectiveID, atIndex)
			if err != nil {
				t.logEvent(t.threadID, "error", "turn", "forking thread for edit/retry failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
				return false
			}
			if err := t.s.db.SetActiveVariant(t.threadID, forkID); err != nil {
				t.logEvent(t.threadID, "error", "turn", "activating forked variant failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
				return false
			}
			t.storageThreadID = forkID
		} else {
			t.storageThreadID = effectiveID
		}
	}
	return true
}

// resolveThreadFlags derives the per-turn Weaver/ghost/field facts, re-read
// from the root thread's row on every continuation turn.
func (t *turnRun) resolveThreadFlags() {
	// isWeaverThread (issue #94, "Talk to Weaver") — a manually-started or
	// continued Weaver session, as opposed to the background scheduler's
	// own shooting-star runs (gateway/constellation_weaver.go), which never
	// go through handleTurn at all. On a brand-new thread this is exactly
	// msg.Source, same as any other client-supplied source (Pulsar Daily's
	// "pulsar-daily" is the existing precedent). On a continuation there's
	// no client-supplied source to trust — the frontend never resends it
	// on later turns — so this reads the thread's own persisted Source
	// back via GetThreadRaw, keyed on the root threadID (not
	// storageThreadID/a fork) since Weaver-ness is a property of the
	// conversation as a whole, matching RunShootingStar's own root-vs-
	// variant reasoning.
	//
	// ghost is resolved the exact same way, off the exact same call: true
	// on a brand-new thread only when the client asked for it (msg.
	// Anonymous), but re-read off the thread's own persisted `ghost`
	// column on every continuation turn — never trusted from the client
	// past creation. This is what makes promotion (clearing that column)
	// sufficient on its own to restore normal tool access on the very
	// next turn, with no other client-side signaling.
	t.isWeaverThread = t.msg.Source == "weaver"
	t.ghost = t.isNewThread && t.msg.Anonymous
	// fieldID is read the same way, off the same root-thread row — never
	// a fork's own row, since ForkThread's hidden variants don't carry
	// field_id (see store/fields.go's DeleteField). Re-read every
	// turn rather than cached, because a thread can be moved between
	// fields between turns (SetThreadField) and the next turn's
	// code_exec mount must follow it. Empty for a brand-new thread here —
	// a thread born inside a field is bound at creation, not read back.
	t.fieldID = ""
	if !t.isNewThread {
		if rawThread, err := t.s.db.GetThreadRaw(t.threadID); err == nil {
			t.isWeaverThread = rawThread.Source == "weaver"
			t.ghost = rawThread.Ghost
			if rawThread.FieldID != nil {
				t.fieldID = *rawThread.FieldID
			}
		}
	}
	if t.ghost && t.noteGhostThread != nil {
		t.noteGhostThread(t.threadID)
	}
}

// resolveField loads the Field this turn belongs to, if any. Returns false
// when a brand-new thread names a Field that no longer exists.
func (t *turnRun) resolveField() bool {
	// field is the one lookup everything field-shaped below reads: the
	// instructions block, the memory-mode gate, the default-model fallback.
	// A ghost turn never has one — same "no persisted-store reads leaking
	// into an incognito session" rule as the global custom instructions and
	// memory below. For a brand-new thread the field comes from the
	// message and is validated HERE, before a thread row exists, so a stale
	// picker (field deleted since the page loaded) errors cleanly instead
	// of leaving an orphan thread behind — same reasoning as the pulse
	// linking below, just checked earlier.
	if !t.ghost {
		switch {
		case t.isNewThread && t.msg.FieldID != "":
			p, err := t.s.db.GetField(t.msg.FieldID)
			if err != nil {
				t.send(ServerEvent{Type: "error", Message: "That field no longer exists."})
				return false
			}
			t.field = p
		case t.fieldID != "":
			if p, err := t.s.db.GetField(t.fieldID); err == nil {
				t.field = p
			}
		}
	}
	t.fieldID = ""
	if t.field != nil {
		t.fieldID = t.field.ID
	}
	return true
}

// chooseModel picks the requested model (client choice, then the Field's
// default, then the global default) and builds the turn's LLM client.
func (t *turnRun) chooseModel() {
	t.requestedModel = t.msg.Model
	if t.requestedModel == "" && t.field != nil && t.field.DefaultModel != "" {
		// Only when the client named no model at all (a bare API caller —
		// the web composer always sends its own, seeded from the field's
		// default by the frontend): a field's default_model is a standing
		// default like the global one, not an override of an explicit choice.
		t.requestedModel = t.field.DefaultModel
	}
	if t.requestedModel == "" {
		t.requestedModel = t.s.effectiveDefaultModel(t.cfg)
	}
	t.modelCfg = t.cfg.ModelByID(t.requestedModel)
	t.client = llm.NewClient(t.cfg.OpenRouter.BaseURL, t.cfg.OpenRouter.APIKey, t.modelCfg.Model, t.modelCfg.Temperature, t.modelCfg.MaxTokens).
		// AllowFallbacks(true), not false: OpenRouter still prefers
		// modelCfg.Provider's curated order while any entry in it is
		// healthy (see llm.Client.provider's doc comment on why caching
		// stays stable in practice) — this only matters as an escape
		// valve for the case every pinned provider is down at once. Real
		// incident, 2026-09-06: deepseek's then-2-entry list (Baidu,
		// DeepInfra) both returned 429 together, and the user hit the
		// *same* dead end again on the very next turn — evidence the
		// saturation wasn't a one-provider fluke but affected the whole
		// pinned set, which a longer curated list (see models/models.go)
		// narrows but can never fully rule out. With AllowFallbacks
		// false, OpenRouter has no permission to reach any of that
		// model's ~29 other endpoints no matter how exhausted our list
		// is, so it 429s instead of ever finding a working one.
		WithProvider(&llm.ProviderRouting{Order: t.modelCfg.Provider, AllowFallbacks: boolPtr(true)}).
		// WithSessionID is a no-op for provider stickiness here: every
		// model in the registry sets Provider above, and OpenRouter
		// ignores session_id sticky routing whenever provider.order is
		// present (see llm.Client's sessionID doc comment). Cache
		// stability across the thread comes from OpenRouter preferring
		// Provider[0] while it's healthy, not from this call.
		WithSessionID(t.threadID)
	if rc := t.modelCfg.Reasoning; rc != nil && rc.Enabled {
		t.client = t.client.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}
}

// ensureThread creates a brand-new thread's row (and its Field/pulse links),
// or resets the placeholder title after an edit of the opening message.
// Returns false when a hard-failing write leaves the thread unusable.
func (t *turnRun) ensureThread() bool {
	if t.isNewThread {
		// TitleSeed, not msg.Content, when set — see its doc comment: a
		// synthetic wrapper message (Pulsar Daily's expand-to-chat) makes
		// a confusing placeholder too, not just a confusing generateTitle
		// input.
		title := t.msg.Content
		if t.msg.TitleSeed != "" {
			title = t.msg.TitleSeed
		}
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		source := t.msg.Source
		if source == "" {
			source = "web"
		}
		createThread := t.s.db.CreateThread
		if t.ghost {
			createThread = t.s.db.CreateGhostThread
		}
		if err := createThread(t.threadID, title, t.modelCfg.ID, source); err != nil {
			t.logEvent(t.threadID, "error", "turn", "creating thread failed", map[string]interface{}{"err": err.Error()}, t.turnID)
			t.send(ServerEvent{Type: "error", Message: err.Error()})
			return false
		}
		if t.field != nil {
			// Hard failure, same as the pulse link below: a thread that was
			// meant to be in a field but silently isn't would run this very
			// turn without the field's instructions/mount and then sit
			// ungrouped forever, with no repair path.
			if err := t.s.db.SetThreadField(t.threadID, &t.field.ID); err != nil {
				t.logEvent(t.threadID, "error", "turn", "linking thread to field failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
				return false
			}
		}
		if t.msg.PulsarRoutineID != 0 {
			// A hard failure here, not a log-and-continue: an unlinked
			// pulse thread isn't invisible (it still shows up in the
			// normal sidebar list, since pulsar-sourced threads aren't
			// filtered like Atlas's are), but it's permanently excluded
			// from ListPulsarPulses/UnreadPulseCounts — both filter on
			// pulsar_routine_id — with no repair path once this turn
			// finishes. Aborting before spending an LLM turn on a thread
			// that can't function as a pulse from /pulsar's perspective
			// costs nothing extra either way: firePulse already stamps
			// last_run_at before calling in here (deliberately, so a
			// crash mid-turn can't trigger a same-minute re-fire — see
			// its doc comment), so this routine simply produces no pulse
			// for this scheduled slot and tries again at the next one —
			// the same outcome a turn failure further downstream would
			// already have.
			if err := t.s.db.SetThreadPulsarRoutine(t.threadID, t.msg.PulsarRoutineID); err != nil {
				t.logEvent(t.threadID, "error", "turn", "linking pulse thread to routine failed", map[string]interface{}{"err": err.Error()}, t.turnID)
				t.send(ServerEvent{Type: "error", Message: err.Error()})
				return false
			}
		}
	} else if t.isFirstMessageEdit {
		// Same truncated-placeholder treatment as a brand-new thread above
		// — the old title (placeholder or generated) described the
		// question that just got deleted, so the sidebar shouldn't keep
		// showing it while this turn runs. generateTitle below replaces
		// this with a real title once the new answer lands, same as for
		// a new thread.
		title := t.msg.Content
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		if err := t.s.db.SetThreadTitle(t.threadID, title); err != nil {
			log.Warn("failed to reset placeholder title for first-message edit", "err", err)
			t.logEvent(t.threadID, "warn", "title", "resetting placeholder title for first-message edit failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}
	return true
}

// loadTurnContext persists the thread's sticky config, loads the history the
// model will see, announces any compaction that finished since the last turn,
// and captures the previous user message for Oracle.
func (t *turnRun) loadTurnContext() bool {
	// Write through this turn's config as the thread's new sticky default
	// — always threadID (the root), same target SetThreadTitle uses, so
	// reopening the thread later (handleGetThread reads by root id) shows
	// what this turn actually ran with regardless of whether an edit/retry
	// forked storageThreadID above. Best-effort: a failure here shouldn't
	// abort an otherwise-working turn, same reasoning as TouchUpdatedAt.
	if err := t.s.db.SetThreadConfig(t.threadID, t.modelCfg.ID, t.msg.FocusMode, t.msg.DeepResearch, t.msg.NoResearch); err != nil {
		log.Warn("failed to persist thread turn config", "thread", t.threadID, "err", err)
		t.logEvent(t.threadID, "warn", "turn", "persisting thread turn config failed", map[string]interface{}{"err": err.Error()}, t.turnID)
	}

	history, err := t.s.loadHistory(t.storageThreadID)
	t.history = history
	if err != nil {
		t.logEvent(t.storageThreadID, "error", "turn", "loading history failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
		return false
	}

	// Announce a compaction that completed since this thread's last turn
	// — the delivery half of the detached compaction goroutine below (see
	// its comment for why the notice can't be sent at the moment it
	// actually happens). Taken, not read: a second turn must not announce
	// — or charge — the same compaction twice, so TakeCompactionNotice
	// clears it in the same transaction it reads it in.
	//
	// Emitted here, tagged with THIS turn's turnID, so the live event and
	// the row a reload replays land on the same turn's timeline. The
	// summary comes first in the timeline because it arrives before
	// anything else this turn does, which is also how it reads: the
	// history this turn was built from is already the compacted one.
	if summary, compactCost, ok, noticeErr := t.s.db.TakeCompactionNotice(t.storageThreadID); noticeErr != nil {
		// Not fatal to the turn: the summary is only a notice, and the
		// compacted history it describes is already in `history` above.
		log.Warn("failed to take pending compaction notice", "thread", t.threadID, "err", noticeErr)
	} else if ok {
		t.send(ServerEvent{Type: "compacted", ThreadID: t.threadID, Content: summary, CostUSD: compactCost})
		t.logEvent(t.storageThreadID, "info", "compaction", "compaction notice shown", map[string]interface{}{
			"summary":  summary,
			"cost_usd": compactCost,
		}, t.turnID)
	}

	// Captured before AddMessage below inserts the new user message, so
	// this naturally holds the prior one — Oracle mode's classification
	// state (docs/plans/oracle-mode.md) wants the current message plus the
	// thread's previous user message, if any. Best-effort: a lookup
	// failure just means Oracle classifies without that extra context,
	// same as an empty thread would.
	prevUserMessage, err := t.s.db.LastUserMessage(t.storageThreadID)
	t.prevUserMessage = prevUserMessage
	if err != nil {
		log.Warn("oracle: loading previous user message failed", "thread", t.storageThreadID, "err", err)
	}
	return true
}
