package store

// SetMessageCacheUsage records a turn's summed prompt tokens and how many
// of them were prompt-cache reads — see the prompt_tokens schema comment.
// Post-hoc UPDATE, same shape as SetMessageDuration below.
func (s *Store) SetMessageCacheUsage(messageID int64, promptTokens, cacheReadTokens int) error {
	_, err := s.db.Exec(`UPDATE messages SET prompt_tokens = ?, cache_read_tokens = ? WHERE id = ?`, promptTokens, cacheReadTokens, messageID)
	return err
}

// SetMessageCompletionTokens records a turn's summed output tokens — see
// the completion_tokens schema comment. Kept as its own setter (not folded
// into SetMessageCacheUsage above) since it was added later, alongside
// Oracle mode's other turn-info sheet stats.
func (s *Store) SetMessageCompletionTokens(messageID int64, completionTokens int) error {
	_, err := s.db.Exec(`UPDATE messages SET completion_tokens = ? WHERE id = ?`, completionTokens, messageID)
	return err
}

// SetMessageCallStats records the turn's last-call input size and model-call
// count — see the last_prompt_tokens schema comment for why the summed
// prompt_tokens alone misleads about context size.
func (s *Store) SetMessageCallStats(messageID int64, lastPromptTokens, llmCalls int) error {
	_, err := s.db.Exec(`UPDATE messages SET last_prompt_tokens = ?, llm_calls = ? WHERE id = ?`, lastPromptTokens, llmCalls, messageID)
	return err
}

// SetMessageTranscript records a turn's exact wire transcript — see the
// transcript schema comment. Post-hoc UPDATE, same shape as
// SetMessageCacheUsage.
func (s *Store) SetMessageTranscript(messageID int64, transcriptJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET transcript = ? WHERE id = ?`, transcriptJSON, messageID)
	return err
}

// SetMessageDuration records how long agent.Run took to produce a given
// assistant message — a separate post-hoc UPDATE rather than a column
// set at AddMessage time, since the duration isn't known until agent.Run
// has already returned (and thus after the message's ID exists to attach
// it to). Mirrors SetContextTokens' same shape for the same reason.
func (s *Store) SetMessageDuration(messageID int64, durationMs int64) error {
	_, err := s.db.Exec(`UPDATE messages SET duration_ms = ? WHERE id = ?`, durationMs, messageID)
	return err
}

// SetMessageCards records a tool's structured rich-result items (see
// tools.Card) after the assistant message was already persisted — same
// post-hoc-UPDATE shape as SetMessageDuration above, since cards (like
// citations) are only known once agent.Run has already returned.
func (s *Store) SetMessageCards(messageID int64, cardsJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET cards = ? WHERE id = ?`, cardsJSON, messageID)
	return err
}

// SetMessageChart records a turn's chart (see tools.ChartSpec) after the
// assistant message was already persisted — same post-hoc-UPDATE shape as
// SetMessageCards above.
func (s *Store) SetMessageChart(messageID int64, chartJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET chart = ? WHERE id = ?`, chartJSON, messageID)
	return err
}

// SetMessagePendingQuestion records a turn-ending ask_user_question call
// (see tools.PendingQuestion) after the assistant message was already
// persisted — same post-hoc-UPDATE shape as SetMessageCards above.
func (s *Store) SetMessagePendingQuestion(messageID int64, pendingQuestionJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET pending_question = ? WHERE id = ?`, pendingQuestionJSON, messageID)
	return err
}

// SetMessageSuggestions records follow-up suggestions generated after the
// assistant message was already persisted — generateSuggestions now runs
// after handleTurn sends "done" (so the turn footer doesn't wait on it),
// so this is a post-hoc UPDATE rather than part of the original AddMessage
// insert, same shape as SetMessageDuration above.
func (s *Store) SetMessageSuggestions(messageID int64, suggestionsJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET suggestions = ? WHERE id = ?`, suggestionsJSON, messageID)
	return err
}

// SetMessageVerification records per-claim "found in source" results (see
// the messages.verification schema comment) after the assistant message was
// already persisted — same post-hoc-UPDATE shape as SetMessageSuggestions
// above, since verification runs in its own detached goroutine after "done"
// ships.
func (s *Store) SetMessageVerification(messageID int64, verificationJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET verification = ? WHERE id = ?`, verificationJSON, messageID)
	return err
}

// SetMessageOracleResult records Oracle mode's classification result for a
// turn (docs/plans/oracle-mode.md, issue #122) — a post-hoc UPDATE, same
// shape as SetMessageVerification, since gateway/oracle.go's RunOracle
// finishes before the assistant message's ID exists but focusModeSource is
// only known once "manual always wins" is resolved against it. Both are
// written together since they're always decided at the same point in
// gateway/turn.go's turn setup.
func (s *Store) SetMessageOracleResult(messageID int64, oracleResultJSON, focusModeSource string) error {
	_, err := s.db.Exec(`UPDATE messages SET oracle_result = ?, focus_mode_source = ? WHERE id = ?`, oracleResultJSON, focusModeSource, messageID)
	return err
}

// SetMessageFocusModeSource records only focus_mode_source, for a turn
// where Oracle produced no result to persist via SetMessageOracleResult
// but the turn's focus mode provenance still matters to the next turn.
func (s *Store) SetMessageFocusModeSource(messageID int64, focusModeSource string) error {
	_, err := s.db.Exec(`UPDATE messages SET focus_mode_source = ? WHERE id = ?`, focusModeSource, messageID)
	return err
}

// SetMessageAppliedFocusMode records this turn's own resolved focus mode —
// see the schema comment above messages.applied_focus_mode. Written
// unconditionally for every assistant turn (unlike SetMessageOracleResult,
// which only fires when Oracle got a live answer) since a turn can have an
// applied focus mode from a manual/default pick with Oracle off or quiet
// this turn — the frontend's "kept your X" margin note needs that case
// too, not just the ones where Oracle itself picked something.
func (s *Store) SetMessageAppliedFocusMode(messageID int64, focusMode string) error {
	_, err := s.db.Exec(`UPDATE messages SET applied_focus_mode = ? WHERE id = ?`, focusMode, messageID)
	return err
}

// SetMessageAppliedModel records this turn's own requested model id — see
// the schema comment above messages.applied_model. Same
// always-written-regardless-of-Oracle shape as SetMessageAppliedFocusMode.
func (s *Store) SetMessageAppliedModel(messageID int64, model string) error {
	_, err := s.db.Exec(`UPDATE messages SET applied_model = ? WHERE id = ?`, model, messageID)
	return err
}

// SetMessageTurnStats records the answer-stats fields the Oracle mode
// turn-info sheet shows regardless of whether Oracle itself is on — same
// post-hoc-UPDATE shape as SetMessageDuration, since none of these are
// known until agent.Run has already returned.
func (s *Store) SetMessageTurnStats(messageID int64, ttftMs int64, tokensPerSecond float64, toolCallCount int) error {
	_, err := s.db.Exec(`UPDATE messages SET ttft_ms = ?, tokens_per_second = ?, tool_call_count = ? WHERE id = ?`, ttftMs, tokensPerSecond, toolCallCount, messageID)
	return err
}

// SetMessageAttachment records the display filename/content-type for a
// user message that carried an upload — same post-hoc-UPDATE shape as
// SetMessageDuration, since AddMessage needs to run first for the
// message's ID to exist.
func (s *Store) SetMessageAttachment(messageID int64, filename, contentType string) error {
	_, err := s.db.Exec(`UPDATE messages SET attachment_filename = ?, attachment_content_type = ? WHERE id = ?`,
		filename, contentType, messageID)
	return err
}

// SetMessageWorkspaceFileID records the addressable filename an uploaded
// file was given once it's actually landed in the thread's workspace
// directory — a separate post-hoc UPDATE from SetMessageAttachment above
// because that filename isn't generated until resolveAttachment runs,
// later in the same turn (gateway/attachments.go), after the display
// filename/content-type were already recorded.
func (s *Store) SetMessageWorkspaceFileID(messageID int64, fileID string) error {
	_, err := s.db.Exec(`UPDATE messages SET workspace_file_id = ? WHERE id = ?`, fileID, messageID)
	return err
}

// SetMessageAttachments records every file included with a user message
// (JSON-encoded []Attachment) — the multi-valued successor to
// SetMessageAttachment/SetMessageWorkspaceFileID above (issue #71), which
// only ever recorded one. Those two setters and their columns are now
// frozen: nothing calls them for a new message after this shipped.
func (s *Store) SetMessageAttachments(messageID int64, attachmentsJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET attachments = ? WHERE id = ?`, attachmentsJSON, messageID)
	return err
}

// SetMessageTTSAudioFileID records the addressable filename a persisted
// read-aloud synthesis was written to inside the thread's workspace
// directory — same post-hoc-UPDATE shape as SetMessageWorkspaceFileID,
// since the file isn't written (and its generated id doesn't exist) until
// after the client has already requested read-aloud for this message.
func (s *Store) SetMessageTTSAudioFileID(messageID int64, fileID string) error {
	_, err := s.db.Exec(`UPDATE messages SET tts_audio_file_id = ? WHERE id = ?`, fileID, messageID)
	return err
}
