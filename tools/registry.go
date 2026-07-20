// Package tools implements the agent's tool-use loop: think, web_search,
// web_read, nearby_search, and reply. Each tool self-registers via init(),
// mirroring her-go's tools/ package convention.
package tools

import (
	"context"

	"polaris/llm"
	"polaris/logger"
	"polaris/places"
	"polaris/search"
)

var log = logger.WithPrefix("tools")

// Context carries dependencies shared across a single turn's tool calls,
// plus an Emit callback the gateway uses to stream progress events
// (thinking/tool_call/tool_result) to the browser over the websocket.
type Context struct {
	// Ctx is the request-scoped context for this turn — cancelled when the
	// user hits "stop" mid-generation. Tool handlers thread it into their
	// outbound HTTP calls so a stop actually aborts in-flight network
	// requests instead of only taking effect at the next LLM call.
	Ctx context.Context

	SearXNG    *search.SearXNGClient
	Foursquare *places.FoursquareClient // nil if not configured — nearby_search falls back to SearXNG
	LLM        llm.ChatClient           // the model selected for this thread; reused by web_read's optional filter pass

	// DefaultLocation is geocoded by nearby_search when a query omits an
	// explicit location. Empty means "no fallback — location is required."
	DefaultLocation string

	// MaxTurns bounds one turn's tool-use loop — see config.Config.MaxAgentTurns.
	// Zero means "caller didn't set it", which agent.Run treats as its own
	// fallback default rather than looping forever.
	MaxTurns int

	// VoiceMode, when true, tells the driver to keep the final answer
	// short and speakable — it's about to be read aloud via the browser's
	// TTS, not just displayed.
	VoiceMode bool

	Emit func(eventType string, payload map[string]interface{})

	// Citations accumulates every {title, url} surfaced by search/read/
	// nearby_search calls during this turn, so the gateway can attach
	// them to the final answer once the model replies.
	Citations []Citation
}

type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// AddCitation appends a citation unless its URL is already present —
// web_search and web_read routinely surface the same URL (a search hit
// that then gets read in full), and duplicate source badges in the UI
// look like a bug rather than an accurate source list.
func (c *Context) AddCitation(cit Citation) {
	for _, existing := range c.Citations {
		if existing.URL == cit.URL {
			return
		}
	}
	c.Citations = append(c.Citations, cit)
}

type HandlerFunc func(argsJSON string, ctx *Context) string

var registry = map[string]HandlerFunc{}

func Register(name string, fn HandlerFunc) {
	registry[name] = fn
}

func Dispatch(name, argsJSON string, ctx *Context) string {
	fn, ok := registry[name]
	if !ok {
		return "error: unknown tool " + name
	}
	return fn(argsJSON, ctx)
}

// Defs returns the tool definitions offered to the model every turn.
// There's no explicit "reply" tool — the loop runs with tool_choice
// "auto", so the model free-flows between calling tools and just
// answering directly once it has enough context.
func Defs() []llm.ToolDef {
	return []llm.ToolDef{thinkDef, webSearchDef, webReadDef, nearbySearchDef}
}
