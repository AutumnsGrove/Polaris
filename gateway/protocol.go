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
	// VoiceMode, when true, tells the driver this answer will be read
	// aloud (browser TTS) — it nudges the model toward a brief,
	// speakable answer instead of a long markdown-formatted one.
	VoiceMode bool `json:"voice_mode,omitempty"`
}

// ServerEvent is one streamed update. Type drives how the frontend
// renders it:
//
//	"thinking"     — content: a think-tool thought, shown as a collapsible reasoning step
//	"tool_call"     — tool + args: a search/read call just started
//	"tool_result"   — tool + result + citations: that call finished
//	"token"         — content: one chunk of the final answer, appended live
//	"user_message"  — user_message_id: the persisted ID of the user message that started this
//	                  turn, sent as soon as it's saved (even if the turn later errors) so the
//	                  frontend can retry/edit from it
//	"done"          — thread_id + cost_usd: turn complete, persisted, safe to re-enable input
//	"error"         — message: something failed
type ServerEvent struct {
	Type          string           `json:"type"`
	ThreadID      string           `json:"thread_id,omitempty"`
	Content       string           `json:"content,omitempty"`
	Tool          string           `json:"tool,omitempty"`
	Args          map[string]any   `json:"args,omitempty"`
	Result        string           `json:"result,omitempty"`
	Citations     []tools.Citation `json:"citations,omitempty"`
	CostUSD       float64          `json:"cost_usd,omitempty"`
	Message       string           `json:"message,omitempty"`
	UserMessageID int64            `json:"user_message_id,omitempty"`
}
