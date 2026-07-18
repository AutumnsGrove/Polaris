// protocol.go defines the WebSocket message shapes exchanged between
// the SvelteKit frontend and this backend. Kept as plain structs (not
// hidden behind a client SDK) so the frontend's TypeScript types can
// mirror this file 1:1.
package gateway

import "polaris/tools"

// ClientMessage is sent by the browser over /ws to start (or continue) a turn.
// ThreadID empty means "start a new thread".
type ClientMessage struct {
	Type     string `json:"type"` // always "message" for now
	ThreadID string `json:"thread_id,omitempty"`
	Content  string `json:"content"`
	Model    string `json:"model"` // config.ModelConfig.ID
}

// ServerEvent is one streamed update. Type drives how the frontend
// renders it:
//
//	"thinking"     — content: a think-tool thought, shown as a collapsible reasoning step
//	"tool_call"     — tool + args: a search/read call just started
//	"tool_result"   — tool + result + citations: that call finished
//	"token"         — content: one chunk of the final answer, appended live
//	"done"          — thread_id + cost_usd: turn complete, persisted, safe to re-enable input
//	"error"         — message: something failed
type ServerEvent struct {
	Type      string            `json:"type"`
	ThreadID  string            `json:"thread_id,omitempty"`
	Content   string            `json:"content,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	Args      map[string]any    `json:"args,omitempty"`
	Result    string            `json:"result,omitempty"`
	Citations []tools.Citation  `json:"citations,omitempty"`
	CostUSD   float64           `json:"cost_usd,omitempty"`
	Message   string            `json:"message,omitempty"`
}
