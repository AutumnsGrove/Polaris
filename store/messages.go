package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

type Message struct {
	ID          int64   `json:"id"`
	ThreadID    string  `json:"thread_id"`
	Role        string  `json:"role"`
	Content     string  `json:"content"`
	Citations   string  `json:"citations"`   // JSON-encoded []tools.Citation
	Suggestions string  `json:"suggestions"` // JSON-encoded []string
	CostUSD     float64 `json:"cost_usd"`
	// TurnID joins this message to the events (see store.Event.TurnID)
	// logged while the turn that produced it ran, so a reopened thread
	// can reconstruct that turn's tool calls/thinking steps.
	TurnID string `json:"turn_id"`
	// DurationMs is how long agent.Run took to produce this answer —
	// 0 for user messages, and for assistant messages until
	// SetMessageDuration runs (see its doc comment for why that's a
	// separate post-hoc update rather than part of AddMessage itself).
	DurationMs int64 `json:"duration_ms"`
	// AttachmentFilename/AttachmentContentType are set only on a user
	// message that carried an upload — see SetMessageAttachment.
	AttachmentFilename    string `json:"attachment_filename,omitempty"`
	AttachmentContentType string `json:"attachment_content_type,omitempty"`
	// WorkspaceFileID is the exact addressable filename (a short generated
	// id plus its extension, e.g. "a1b2c3d4e5.pdf") for that same upload
	// inside the thread's workspace directory — see the schema comment
	// above workspace_file_id and SetMessageWorkspaceFileID. This is
	// literally the last path segment GET /api/workspace/:thread_id/
	// :filename expects, so the frontend can build a download link
	// directly from it. "" for a message with no attachment, or one
	// predating this column.
	WorkspaceFileID string `json:"workspace_file_id,omitempty"`
	// Attachments is JSON-encoded []Attachment — see SetMessageAttachments
	// and the schema comment above messages.attachments. Always populated
	// on read, even for a message written before this column existed:
	// GetMessages synthesizes a one-element array from the three legacy
	// fields above in that case, so a caller only ever needs to look at
	// this field, never the legacy ones. "[]" for a message with no
	// upload.
	Attachments string `json:"attachments"`
	// Cards is JSON-encoded []tools.Card — see SetMessageCards.
	Cards string `json:"cards"`
	// Chart is JSON-encoded *tools.ChartSpec — see SetMessageChart. ""
	// (omitted) for every message no tool call produced a chart for.
	Chart string `json:"chart,omitempty"`
	// PendingQuestion is JSON-encoded *tools.PendingQuestion, set only on
	// an assistant message that ended its turn via ask_user_question —
	// see SetMessagePendingQuestion. "" for every other message.
	PendingQuestion string `json:"pending_question,omitempty"`
	// TTSAudioFileID is the addressable filename of a persisted read-aloud
	// synthesis for this assistant message — see the schema comment above
	// messages.tts_audio_file_id and SetMessageTTSAudioFileID. "" for a
	// message with no persisted read-aloud audio.
	TTSAudioFileID string `json:"tts_audio_file_id,omitempty"`
	// Verification is JSON-encoded []gateway.VerificationMark — see the
	// schema comment above messages.verification and
	// SetMessageVerification. "[]" for a message with no verification
	// pass run, or nothing found supported at/above the confidence
	// threshold.
	Verification string `json:"verification"`
	// Transcript is the turn's exact wire messages (see the schema
	// comment) — history-building only, never sent to the frontend: it
	// can run to hundreds of KB for a researched turn.
	Transcript string `json:"-"`
	// PromptTokens/CacheReadTokens are this turn's own input tokens and
	// prompt-cache hits — previously write-only (only ThreadCacheUsage's
	// SUM read them back); exposed per-message here for the Oracle mode
	// turn-info sheet's "tokens in/out" (docs/plans/oracle-mode.md).
	PromptTokens    int `json:"prompt_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	// OracleResult is JSON-encoded gateway.OracleResult — see the schema
	// comment above messages.oracle_result and SetMessageOracleResult.
	// "" for a message with Oracle off or unconfigured.
	OracleResult string `json:"oracle_result,omitempty"`
	// FocusModeSource is "manual"/"default"/"oracle" — see the schema
	// comment above messages.focus_mode_source.
	FocusModeSource string `json:"focus_mode_source,omitempty"`
	// AppliedFocusMode is this turn's own resolved focus mode — see the
	// schema comment above messages.applied_focus_mode.
	AppliedFocusMode string `json:"applied_focus_mode,omitempty"`
	// AppliedModel is this turn's own requested model id — see the schema
	// comment above messages.applied_model.
	AppliedModel string `json:"applied_model,omitempty"`
	// CompletionTokens is this turn's summed output tokens — see the
	// schema comment above messages.completion_tokens.
	CompletionTokens int `json:"completion_tokens,omitempty"`
	// LastPromptTokens/LLMCalls are the turn's final call's input size and
	// its model-call count — see the schema comment above
	// messages.last_prompt_tokens.
	LastPromptTokens int `json:"last_prompt_tokens,omitempty"`
	LLMCalls         int `json:"llm_calls,omitempty"`
	// CostAnswerUSD/CostVerificationUSD/CostOracleUSD are CostUSD's
	// three-tier split — see the schema comment above
	// messages.cost_answer_usd.
	CostAnswerUSD       float64 `json:"cost_answer_usd"`
	CostVerificationUSD float64 `json:"cost_verification_usd"`
	CostOracleUSD       float64 `json:"cost_oracle_usd"`
	// TTFTMs/TokensPerSecond/ToolCallCount — see the schema comments above
	// messages.ttft_ms/tokens_per_second/tool_call_count.
	TTFTMs          int64     `json:"ttft_ms,omitempty"`
	TokensPerSecond float64   `json:"tokens_per_second,omitempty"`
	ToolCallCount   int       `json:"tool_call_count,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// Attachment is one file included with a user message — see
// messages.attachments. JSON field names intentionally match
// UploadedAttachment's shape on the frontend (web/src/lib/types.ts) so
// the two round-trip without translation.
type Attachment struct {
	Filename        string `json:"filename"`
	ContentType     string `json:"content_type"`
	WorkspaceFileID string `json:"workspace_file_id"`
}

// AddMessage inserts a message and bumps the thread's running cost and
// updated_at in one transaction, so ListThreads' ordering and the
// header's cost display stay consistent. Returns the new message's ID,
// which the frontend needs later to retry/edit from this point.
func (s *Store) AddMessage(threadID, role, content, citationsJSON, suggestionsJSON string, costUSD float64, turnID string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, cost_answer_usd, turn_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		threadID, role, content, citationsJSON, suggestionsJSON, costUSD, costUSD, turnID,
	)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		costUSD, threadID,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// IsFirstMessage reports whether id is the earliest message in threadID —
// used by handleTurn to tell an edit/retry of the thread's opening
// question (which should regenerate the title, since the question the
// old title was based on no longer exists) apart from an edit/retry
// further into the conversation (which shouldn't: the title already
// describes an established thread, not just this one turn).
func (s *Store) IsFirstMessage(threadID string, id int64) (bool, error) {
	var minID int64
	err := s.db.QueryRow(`SELECT MIN(id) FROM messages WHERE thread_id = ?`, threadID).Scan(&minID)
	if err != nil {
		return false, err
	}
	return minID == id, nil
}

// MessageIndex returns the 0-based position of message id within
// threadID's own message list — the atIndex ForkThread needs to know how
// much of the shared prefix to copy for an edit/retry landing on this
// message.
func (s *Store) MessageIndex(threadID string, id int64) (int, error) {
	var index int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND id < ?`, threadID, id).Scan(&index)
	return index, err
}

func (s *Store) GetMessages(threadID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms,
			attachment_filename, attachment_content_type, workspace_file_id, attachments, cards, chart, pending_question, tts_audio_file_id, verification, transcript, created_at,
			prompt_tokens, cache_read_tokens, oracle_result, focus_mode_source, cost_answer_usd, cost_verification_usd, cost_oracle_usd, ttft_ms, tokens_per_second, tool_call_count, applied_focus_mode, applied_model, completion_tokens, last_prompt_tokens, llm_calls
		FROM messages WHERE thread_id = ? ORDER BY id ASC`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Citations, &m.Suggestions, &m.CostUSD, &m.TurnID, &m.DurationMs,
			&m.AttachmentFilename, &m.AttachmentContentType, &m.WorkspaceFileID, &m.Attachments, &m.Cards, &m.Chart, &m.PendingQuestion, &m.TTSAudioFileID, &m.Verification, &m.Transcript, &m.CreatedAt,
			&m.PromptTokens, &m.CacheReadTokens, &m.OracleResult, &m.FocusModeSource, &m.CostAnswerUSD, &m.CostVerificationUSD, &m.CostOracleUSD, &m.TTFTMs, &m.TokensPerSecond, &m.ToolCallCount, &m.AppliedFocusMode, &m.AppliedModel, &m.CompletionTokens, &m.LastPromptTokens, &m.LLMCalls); err != nil {
			return nil, err
		}
		m.Attachments = withLegacyAttachmentFallback(m.Attachments, m.AttachmentFilename, m.AttachmentContentType, m.WorkspaceFileID)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetMessageByID looks up a single message by its primary key, regardless
// of which thread/fork it currently belongs to — used by handleTurn's
// retry/edit path (ClientMessage.EditFromID) to carry the original
// message's attachments forward onto its replacement, since a retry can't
// resend them itself: the upload's staging file is already gone by then,
// moved into the workspace by the original turn's resolveAttachments.
func (s *Store) GetMessageByID(id int64) (Message, error) {
	var m Message
	err := s.db.QueryRow(
		`SELECT id, thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms,
			attachment_filename, attachment_content_type, workspace_file_id, attachments, cards, chart, pending_question, tts_audio_file_id, verification, created_at,
			prompt_tokens, cache_read_tokens, oracle_result, focus_mode_source, cost_answer_usd, cost_verification_usd, cost_oracle_usd, ttft_ms, tokens_per_second, tool_call_count, applied_focus_mode, applied_model, completion_tokens, last_prompt_tokens, llm_calls
		FROM messages WHERE id = ?`,
		id,
	).Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Citations, &m.Suggestions, &m.CostUSD, &m.TurnID, &m.DurationMs,
		&m.AttachmentFilename, &m.AttachmentContentType, &m.WorkspaceFileID, &m.Attachments, &m.Cards, &m.Chart, &m.PendingQuestion, &m.TTSAudioFileID, &m.Verification, &m.CreatedAt,
		&m.PromptTokens, &m.CacheReadTokens, &m.OracleResult, &m.FocusModeSource, &m.CostAnswerUSD, &m.CostVerificationUSD, &m.CostOracleUSD, &m.TTFTMs, &m.TokensPerSecond, &m.ToolCallCount, &m.AppliedFocusMode, &m.AppliedModel, &m.CompletionTokens, &m.LastPromptTokens, &m.LLMCalls)
	if err != nil {
		return Message{}, err
	}
	m.Attachments = withLegacyAttachmentFallback(m.Attachments, m.AttachmentFilename, m.AttachmentContentType, m.WorkspaceFileID)
	return m, nil
}

// withLegacyAttachmentFallback synthesizes a one-element attachments JSON
// array from the frozen singular attachment_filename/attachment_content_
// type/workspace_file_id columns, for any message row written before the
// attachments column existed. attachmentsJSON is returned unchanged
// whenever it's already non-empty (anything but "[]"), which is every row
// written after this shipped.
func withLegacyAttachmentFallback(attachmentsJSON, filename, contentType, workspaceFileID string) string {
	if attachmentsJSON != "[]" || filename == "" {
		return attachmentsJSON
	}
	encoded, err := json.Marshal([]Attachment{{Filename: filename, ContentType: contentType, WorkspaceFileID: workspaceFileID}})
	if err != nil {
		return attachmentsJSON
	}
	return string(encoded)
}

// ThreadCacheUsage sums prompt_tokens/cache_read_tokens over every message
// in threadID — the all-time thread-level hit rate issue #107 asks for,
// computed on read so it can't drift from the messages it summarizes.
func (s *Store) ThreadCacheUsage(threadID string) (promptTokens, cacheReadTokens int, err error) {
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(cache_read_tokens), 0) FROM messages WHERE thread_id = ?`,
		threadID,
	).Scan(&promptTokens, &cacheReadTokens)
	return promptTokens, cacheReadTokens, err
}

// LastUserMessage returns the most recent user-role message's content in
// threadID, "" if there is none — used by gateway/turn.go to build Oracle
// mode's classification state (current message + previous user message,
// docs/plans/oracle-mode.md) before the new user message is itself
// inserted, so this naturally returns the prior one rather than the one
// about to be added.
func (s *Store) LastUserMessage(threadID string) (string, error) {
	var content string
	err := s.db.QueryRow(`SELECT content FROM messages WHERE thread_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`, threadID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

// LastAssistantFocusModeSource returns the most recent assistant message's
// focus_mode_source in threadID, "" if there is none — Oracle mode's
// stickiness/switch-threshold logic (gateway.RunOracle's PriorOracleFocusMode)
// uses this to tell whether the thread's current sticky focus mode
// (threads.focus_mode) was itself Oracle's own pick, as opposed to a
// manual or default one.
func (s *Store) LastAssistantFocusModeSource(threadID string) (string, error) {
	var source string
	err := s.db.QueryRow(`SELECT focus_mode_source FROM messages WHERE thread_id = ? AND role = 'assistant' ORDER BY id DESC LIMIT 1`, threadID).Scan(&source)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return source, err
}
