// protocol.go defines the WebSocket message shapes exchanged between
// the SvelteKit frontend and this backend. Kept as plain structs (not
// hidden behind a client SDK) so the frontend's TypeScript types can
// mirror this file 1:1.
package gateway

import "polaris/tools"

// ClientMessage is sent by the browser over /ws to start (or continue) a turn.
// ThreadID empty means "start a new thread".
//
// EditFromID turns this into a retry/edit instead of a fresh message: the
// server deletes every message in the thread with id >= EditFromID (the
// original user message plus its answer and anything after) before
// treating Content as the new user message at that point. Retry re-sends
// the original content unchanged; editing sends the revised text.
type ClientMessage struct {
	Type       string `json:"type"` // always "message" for now
	ThreadID   string `json:"thread_id,omitempty"`
	Content    string `json:"content"`
	Model      string `json:"model"` // config.ModelConfig.ID
	EditFromID int64  `json:"edit_from_id,omitempty"`
	// VoiceMode, when true, tells the driver this answer is likely to be
	// read aloud — nudges the model toward a brief, speakable answer
	// instead of a long markdown-formatted one. Not wired to any UI toggle
	// yet (that's the planned full voice-mode session, built later); for
	// now, read-aloud is a per-message opt-in that doesn't set this.
	VoiceMode bool `json:"voice_mode,omitempty"`
	// SttCostUSD carries the transcription cost from a push-to-talk memo
	// (already billed via /api/transcribe) so it gets folded into the
	// thread's running total instead of being tracked nowhere.
	SttCostUSD float64 `json:"stt_cost_usd,omitempty"`
	// Source tags a brand-new thread's origin (see store.Thread.Source) —
	// empty means "web", the normal chat UI. Only read on thread creation;
	// ignored on every later turn in the same thread. The WebSocket client
	// never sets this; it's populated by handleAsk for API-originated threads.
	Source string `json:"source,omitempty"`
	// UserLocation is "lat, lon" from the browser's Geolocation API,
	// cached client-side in a cookie so it's resent with every message
	// without re-prompting each turn (see web/src/lib/geolocation.ts).
	// Empty if the browser never granted permission, or on non-web
	// sources. Used as nearby_search's location when neither the user's
	// message nor the model's tool call names one explicitly — see
	// handleTurn's defaultLocation precedence.
	UserLocation string `json:"user_location,omitempty"`
	// FocusMode is set from the composer's "+" menu (see
	// web/src/lib/components/ComposerMenu.svelte) — one of
	// agent.FocusMode's values, or empty for normal behavior. Shapes the
	// system prompt for this turn only; see agent.focusModeInstruction.
	FocusMode string `json:"focus_mode,omitempty"`
	// DeepResearch, when true, raises this turn's research budget and
	// check-in leniency — see agent.Run's maxTurns/researchCheckInInterval
	// handling.
	DeepResearch bool `json:"deep_research,omitempty"`
	// AttachmentID/AttachmentFilename/AttachmentContentType describe a
	// file uploaded via POST /api/upload ahead of this message (see
	// gateway/attachments.go) — same two-step shape as push-to-talk voice
	// memos. AttachmentID is the opaque name handleUpload saved the file
	// under (config.Attachments.Dir/<id>); the other two are only for
	// display and content-type dispatch, both already known to the
	// frontend from the upload response, so the server doesn't need a
	// side table to look them back up. Empty AttachmentID means no
	// attachment on this message.
	AttachmentID          string `json:"attachment_id,omitempty"`
	AttachmentFilename    string `json:"attachment_filename,omitempty"`
	AttachmentContentType string `json:"attachment_content_type,omitempty"`
}

// ServerEvent is one streamed update. Type drives how the frontend
// renders it:
//
//	"thinking"     — content: a think-tool thought, shown as a collapsible reasoning step
//	"reasoning"     — content: one chunk of a reasoning-capable model's native "thinking" stream
//	                  (DeepSeek/MiMo-style), appended live — distinct from the think tool, which
//	                  the model calls explicitly; this is the model's own hidden reasoning pass
//	"tool_call"     — tool + args: a search/read call just started
//	"tool_result"   — tool + result + citations: that call finished
//	"token"         — content: one chunk of the final answer, appended live
//	"commentary"    — content: what the model said before deciding to call a tool (or before an
//	                  aborted attempt got discarded) — sent once, with the full text, right before
//	                  that turn's tool_call events; the frontend clears whatever it had streamed
//	                  live as "token" for that turn and shows this as its own timeline item instead,
//	                  so it's positioned between the tool calls that came before and after it rather
//	                  than getting silently appended to the real final answer (see agent.emitCommentary)
//	"user_message"  — user_message_id: the persisted ID of the user message that started this
//	                  turn, sent as soon as it's saved (even if the turn later errors) so the
//	                  frontend can retry/edit from it
//	"done"          — thread_id + cost_usd + context_tokens + duration_ms: turn complete,
//	                  persisted, safe to re-enable input; duration_ms is how long agent.Run took
//	                  (see store.Message.DurationMs), shown next to cost in the turn footer.
//	                  Deliberately does NOT wait on follow-up suggestions — those are a separate
//	                  LLM call that runs after this event ships, so the turn footer appears the
//	                  moment the answer itself is ready instead of stalling behind it (see
//	                  "suggestions" below and handleTurn's comment on why generateSuggestions
//	                  moved after this send).
//	"suggestions"   — thread_id + cost_usd + suggestions: sent once, shortly after "done", once up
//	                  to 3 follow-up questions for the just-finished answer are ready; persisted
//	                  alongside the assistant message (see store.Message.Suggestions) so reopening
//	                  the thread later still shows them. cost_usd is this call's own cost, added to
//	                  the running total the same as "done"'s — not a replacement for it. May never
//	                  arrive if generation fails or the answer was stopped early; the frontend
//	                  should treat "no suggestions" as a normal, silent outcome, not an error.
//	"compacted"     — thread_id + content: the thread just crossed the context-window threshold
//	                  and was auto-summarized; content is the summary, shown as a collapsible
//	                  timeline note like a tool call, not a normal answer
//	"error"         — message: something failed
type ServerEvent struct {
	Type      string           `json:"type"`
	ThreadID  string           `json:"thread_id,omitempty"`
	Content   string           `json:"content,omitempty"`
	Tool      string           `json:"tool,omitempty"`
	Args      map[string]any   `json:"args,omitempty"`
	Result    string           `json:"result,omitempty"`
	Citations []tools.Citation `json:"citations,omitempty"`
	// CostUSD and ContextTokens deliberately lack omitempty: 0 is a
	// legitimate value for both (a stopped turn that never reached an LLM
	// call costs exactly $0), and omitempty would drop the field from the
	// JSON entirely in that case rather than sending 0. The frontend's
	// `this.totalCost += e.cost_usd` would then add `undefined`, silently
	// and permanently turning totalCost into NaN for the rest of the
	// session — this bit us once already with the analogous "token"
	// event's content field (see streamSniffer.resolve in agent/pseudocall.go).
	CostUSD       float64  `json:"cost_usd"`
	ContextTokens int      `json:"context_tokens"`
	Message       string   `json:"message,omitempty"`
	UserMessageID int64    `json:"user_message_id,omitempty"`
	Suggestions   []string `json:"suggestions,omitempty"`
	// DurationMs is how long agent.Run took to produce the answer — unlike
	// CostUSD/ContextTokens above, omitempty is fine here: a real LLM call
	// always takes measurably more than 0ms, so there's no legitimate zero
	// value being silently dropped.
	DurationMs int64 `json:"duration_ms,omitempty"`
}
