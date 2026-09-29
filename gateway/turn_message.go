package gateway

import (
	"encoding/json"

	"polaris/store"
)

// persistUserMessage stores the user's message before the agent runs so it
// (and its id, needed for retry/edit) survives a failed turn.
func (t *turnRun) persistUserMessage() bool {
	// Persist the user message before running the agent, not after — so
	// it (and its ID, needed for retry/edit) survives even if the turn
	// below errors out. Previously a failed turn left no record at all.
	// SttCostUSD folds in push-to-talk transcription cost, if this
	// message originated from a voice memo.
	//
	// Always a plain AddMessage now, even for a retry/edit — ForkThread
	// above already left storageThreadID with exactly the shared prefix
	// and nothing else, so there's nothing left to delete the way
	// DeleteMessagesFromAndAddMessage used to.
	userMsgID, err := t.s.db.AddMessage(t.storageThreadID, "user", t.msg.Content, "[]", "[]", t.msg.SttCostUSD, t.turnID)
	t.userMsgID = userMsgID
	if err != nil {
		t.logEvent(t.storageThreadID, "error", "turn", "persisting user message failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		t.send(ServerEvent{Type: "error", ThreadID: t.threadID, Message: err.Error()})
		return false
	}
	// See TouchUpdatedAt's doc comment: AddMessage above just bumped
	// storageThreadID's own updated_at, which is invisible to ListThreads
	// once storageThreadID is a forked variant (post edit/retry) rather
	// than threadID itself — without this, the thread silently stops
	// climbing the sidebar's recency order the moment it's ever been
	// edited, even while actively being used.
	if err := t.s.db.TouchUpdatedAt(t.threadID); err != nil {
		log.Warn("failed to bump thread recency", "thread", t.threadID, "err", err)
	}
	t.send(ServerEvent{Type: "user_message", ThreadID: t.threadID, UserMessageID: t.userMsgID})
	return true
}

// resolveTurnMessage builds the model-facing message: the user's text plus
// pointer notes for any attachments (or the ones a retry/edit carries over).
func (t *turnRun) resolveTurnMessage() {
	// turnMessage is what the agent actually sees — msg.Content plus a
	// short pointer note naming each file, if there are attachments (see
	// resolveAttachments). The persisted user message above stays as
	// exactly what the user typed; only the in-flight prompt to the model
	// is augmented, so reopening this thread later shows the original
	// question, not the pointer notes glued onto it.
	t.turnMessage = t.msg.Content
	switch {
	case len(t.msg.Attachments) > 0:
		// threadID (the stable, client-facing root id), not storageThreadID
		// — the workspace directory is one persistent resource shared by
		// every fork/variant of this conversation, unlike messages/events
		// which are correctly fork-scoped. Using storageThreadID here would
		// file a retry/edit's attachment under that fork's own throwaway
		// id, a directory nothing else ever looks in again.
		resolvedMessage, resolved := resolveAttachments(t.cfg, t.msg, t.threadID, func(ref AttachmentRef, err error) {
			log.Warn("resolving attachment failed, continuing without it", "filename", ref.Filename, "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "resolving attachment failed", map[string]interface{}{"filename": ref.Filename, "err": err.Error()}, t.turnID)
		})
		t.turnMessage = resolvedMessage
		if len(resolved) > 0 {
			attachmentsJSON, err := json.Marshal(resolved)
			if err != nil {
				log.Warn("encoding attachments failed", "err", err)
			} else if err := t.s.db.SetMessageAttachments(t.userMsgID, string(attachmentsJSON)); err != nil {
				log.Warn("recording attachments failed", "err", err)
				t.logEvent(t.storageThreadID, "warn", "turn", "recording attachments failed", map[string]interface{}{"err": err.Error()}, t.turnID)
			}
		}
	case t.msg.EditFromID != 0:
		// A retry/edit's ClientMessage never carries the original
		// attachments itself (state.svelte.ts's retry()/editMessage() only
		// resend content) — and even if it tried to, the upload's staging
		// file resolveAttachments moved out of cfg.Attachments.Dir the
		// first time around is long gone, so re-resolving isn't possible
		// anyway. The file already lives in the workspace under its
		// original short ID, so this just carries the old message's
		// already-resolved attachments forward onto the replacement
		// message verbatim, rebuilding the same pointer notes
		// resolveAttachments would have produced. A real gap found live:
		// without this, every retry of a message that had a file attached
		// silently dropped it, and the model had no idea any file existed.
		orig, err := t.s.db.GetMessageByID(t.msg.EditFromID)
		if err != nil {
			log.Warn("looking up original message for retry/edit attachments failed", "err", err)
			break
		}
		var atts []store.Attachment
		if err := json.Unmarshal([]byte(orig.Attachments), &atts); err != nil || len(atts) == 0 {
			break
		}
		for _, a := range atts {
			t.turnMessage += attachmentNote(a.Filename, a.WorkspaceFileID)
		}
		if err := t.s.db.SetMessageAttachments(t.userMsgID, orig.Attachments); err != nil {
			log.Warn("carrying attachments forward on retry/edit failed", "err", err)
			t.logEvent(t.storageThreadID, "warn", "turn", "carrying attachments forward on retry/edit failed", map[string]interface{}{"err": err.Error()}, t.turnID)
		}
	}
}

// logTurnStart records the turn's start in the event log and flags a thread
// that has used Transponder.
func (t *turnRun) logTurnStart() {
	t.logEvent(t.storageThreadID, "info", "turn", "turn started", map[string]interface{}{
		"model":         t.modelCfg.ID,
		"is_new_thread": t.isNewThread,
		"voice_mode":    t.msg.VoiceMode,
		"is_retry":      t.msg.EditFromID != 0,
	}, t.turnID)

	// Transponder usage tracking (docs/plans/transponder.md) — a plain
	// informational flag, not thread config, so a failure here shouldn't
	// fail the turn; just log and move on.
	if t.msg.VoiceMode {
		if err := t.s.db.MarkThreadUsedTransponder(t.storageThreadID); err != nil {
			log.Warn("marking thread used_transponder failed", "err", err)
		}
	}
}

// appendPulsarReport folds a routine's previous report into the model-facing
// message so a recurring pulse states only what's new.
func (t *turnRun) appendPulsarReport() {
	// Folded into turnMessage (what the model sees), not msg.Content (what
	// gets persisted/shown as this pulse's own question) — same "augment
	// the model-facing text, leave the visible transcript alone" split
	// resolveAttachment's own doc comment describes. Lets a recurring
	// routine actually know what it already told the user instead of
	// restating the same still-true report every single run — the
	// concrete case that motivated this: a weekly "Guild Wars 3 news"
	// routine with no memory of its own last pulse just re-describes
	// whatever's still true from before, forever.
	if t.msg.PulsarPreviousReport != "" {
		// Not an absolute "never mention this again" — a flat ban risks
		// permanently dropping something still unfolding (an ongoing beta
		// event, an open incident) the moment it's been mentioned once,
		// which is worse than the staleness problem this exists to fix.
		// The distinction asked for is "restate" vs. "update": a settled
		// fact from last time shouldn't be redescribed, but something
		// still in motion deserves a short status line even with nothing
		// new to add, rather than vanishing from every future report.
		t.turnMessage += "\n\n---\nFor reference, here is what you reported the last time this routine ran " +
			"(" + t.msg.PulsarPreviousReportAt + "):\n\n" + t.msg.PulsarPreviousReport +
			"\n\nDon't redescribe anything from that report as if it were new — a settled fact you already " +
			"covered doesn't need restating. But if something from that report is still ongoing or " +
			"developing, it's fine to give it a brief one-line status update (even just \"still ongoing, " +
			"no major updates\") rather than dropping it entirely. Focus most of your answer on what's " +
			"genuinely new or has meaningfully changed since then. If truly nothing has changed at all, say " +
			"so briefly instead of restating the old report in full."
	}
}
