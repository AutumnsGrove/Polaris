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
//	"user_message"  — user_message_id: the persisted ID of the user message that started this
//	                  turn, sent as soon as it's saved (even if the turn later errors) so the
//	                  frontend can retry/edit from it
//	"done"          — thread_id + cost_usd + context_tokens + suggestions: turn complete,
//	                  persisted, safe to re-enable input; suggestions is up to 3 follow-up
//	                  questions for the just-finished answer, persisted alongside the assistant
//	                  message (see store.Message.Suggestions) so reopening the thread later
//	                  still shows them
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
}
