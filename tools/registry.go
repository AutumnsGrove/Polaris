// Package tools implements the agent's tool-use loop: think, web_search,
// web_read, nearby_search, youtube_transcript, weather, reference_lookup,
// and reply. Each tool self-registers via init(), mirroring her-go's
// tools/ package convention.
package tools

import (
	"context"
	"sync"

	"polaris/llm"
	"polaris/logger"
	"polaris/places"
	"polaris/search"
	"polaris/tavily"
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
	Tavily     *tavily.Client           // nil if not configured — web_read's JS-render/paywall fallback is skipped without it
	LLM        llm.ChatClient           // the model selected for this thread; reused by web_read's optional filter pass

	// Blocklist rejects web_read fetches for blocked domains directly —
	// web_search's own filtering happens inside SearXNG (nil-safe there
	// too), so this only needs plumbing to the one other place a URL can
	// enter the agent loop. Nil means "nothing blocked".
	Blocklist *search.Blocklist

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

	// FocusMode is one of agent.FocusMode's values (or empty for normal
	// behavior), set from the composer's "+" menu — see
	// agent.focusModeInstruction for what each one actually changes.
	FocusMode string

	// DeepResearch, when true, raises this turn's research budget and
	// check-in leniency — see agent.Run.
	DeepResearch bool

	Emit func(eventType string, payload map[string]interface{})

	// Citations accumulates every {title, url} surfaced by search/read/
	// nearby_search calls during this turn, so the gateway can attach
	// them to the final answer once the model replies. citationsMu guards
	// it — agent.Run dispatches every tool call from one model turn
	// concurrently (see dispatchToolCallsConcurrently), so two handlers
	// can call AddCitation at the same instant.
	citationsMu sync.Mutex
	Citations   []Citation
}

type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// SiteName is the publisher/site label from the page's own
	// og:site_name meta tag (e.g. "The Hollywood Reporter"), when web_read
	// fetched the page and it set one — empty for citations that never
	// went through a page fetch (web_search hits, Maps places, weather).
	// The frontend falls back to a hostname-derived label when this is
	// empty, see web/src/lib/citations.ts.
	SiteName string `json:"site_name,omitempty"`
}

// AddCitation appends a citation unless its URL is already present —
// web_search and web_read routinely surface the same URL (a search hit
// that then gets read in full), and duplicate source badges in the UI
// look like a bug rather than an accurate source list. Safe to call
// concurrently from multiple tool handlers dispatched in parallel.
func (c *Context) AddCitation(cit Citation) {
	c.citationsMu.Lock()
	defer c.citationsMu.Unlock()
	for _, existing := range c.Citations {
		if existing.URL == cit.URL {
			return
		}
	}
	c.Citations = append(c.Citations, cit)
}

// CitationsSnapshot returns a copy of the citations gathered so far. Tool
// handlers use this — not a direct ctx.Citations read — when building an
// emit payload mid-dispatch, since with parallel tool calls another
// goroutine's AddCitation could be appending at that exact instant; an
// unsynchronized read there would race with it. A direct read of
// ctx.Citations is still fine once all of a turn's dispatches have joined
// (see agent.Run, after its sync.WaitGroup.Wait returns) — that point is
// provably sequential with every AddCitation that ran before it.
func (c *Context) CitationsSnapshot() []Citation {
	c.citationsMu.Lock()
	defer c.citationsMu.Unlock()
	out := make([]Citation, len(c.Citations))
	copy(out, c.Citations)
	return out
}

type HandlerFunc func(argsJSON string, ctx *Context) string

var registry = map[string]HandlerFunc{}

func Register(name string, fn HandlerFunc) {
	registry[name] = fn
}

func Dispatch(name, argsJSON string, ctx *Context) string {
	fn, ok := registry[name]
	if !ok {
		result := "error: unknown tool " + name
		log.Warn("model called unknown tool", "tool", name)
		ctx.Emit("tool_call", map[string]interface{}{"tool": name})
		ctx.Emit("tool_result", map[string]interface{}{"tool": name, "result": result})
		return result
	}
	return fn(argsJSON, ctx)
}

// emitToolError reports a tool call that failed before doing any real work
// (bad JSON args, a missing required field) — emitting both "tool_call" and
// "tool_result" here, not just returning the error string, so it still
// reaches the durable event log the same way a call that failed partway
// through does (see gateway.logTurnEvent). Without this, an argument
// validation failure was invisible in the event trail: Dispatch's return
// value went straight back to the model with no record a call was ever
// attempted.
func emitToolError(ctx *Context, tool string, args map[string]interface{}, result string) string {
	ctx.Emit("tool_call", map[string]interface{}{"tool": tool, "args": args})
	ctx.Emit("tool_result", map[string]interface{}{"tool": tool, "result": result})
	return result
}

// Defs returns the tool definitions offered to the model every turn.
// There's no explicit "reply" tool — the loop runs with tool_choice
// "auto", so the model free-flows between calling tools and just
// answering directly once it has enough context.
func Defs() []llm.ToolDef {
	return []llm.ToolDef{thinkDef, webSearchDef, webReadDef, nearbySearchDef, youtubeTranscriptDef, weatherDef, referenceLookupDef}
}
