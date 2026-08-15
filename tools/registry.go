// Package tools implements the agent's tool-use loop: think, web_search,
// web_read, nearby_search, youtube_transcript, weather, reference_lookup,
// github_repo, dictionary, music, books, and reply. Each tool self-registers
// via init(), mirroring her-go's tools/ package convention.
package tools

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"polaris/llm"
	"polaris/logger"
	"polaris/places"
	"polaris/search"
	"polaris/tavily"
)

var log = logger.WithPrefix("tools")

// httpUserAgent is sent on every outbound request this package's tools
// make to a third-party API — a handful of providers (GitHub, Open
// Library among them) reject or rate-limit requests with no User-Agent
// at all, and a consistent identifying string is good citizenship either
// way.
const httpUserAgent = "Polaris/1.0 (personal search assistant)"

// httpGetJSON issues a GET request and returns its raw body plus status
// code — the transport plumbing (request construction, User-Agent, a
// 10-second timeout, reading the body) is identical across every JSON
// API this package's tools call, even though each one's response/error
// shape differs (Last.fm's HTTP-200-with-error-field vs TMDB's non-2xx
// status, for two). Callers own their own status-code and error-shape
// parsing; this only exists so that plumbing isn't copy-pasted anew in
// every *Get function.
func httpGetJSON(ctx context.Context, url string) (body []byte, statusCode int, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", httpUserAgent)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

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

	// GitHubToken is an optional personal access token attached to
	// github_repo's API calls as a bearer token. Empty means "call
	// unauthenticated" — GitHub's REST API works fine without one, just
	// capped at 60 requests/hour instead of 5000, so unlike
	// Foursquare/Tavily this is never a hard requirement for the tool to
	// function at all.
	GitHubToken string

	// LastFMAPIKey is required for the music tool — unlike GitHubToken,
	// there's no unauthenticated fallback (see tools/music.go's package
	// doc comment). Empty means every music call fails with a clear
	// "not configured" error rather than degrading.
	LastFMAPIKey string

	// HardcoverAPIKey is optional, like GitHubToken — the books tool's
	// Open Library fallback works with no key at all (see tools/books.go's
	// package doc comment). Empty, invalid, or expired all degrade to
	// Open Library-only recommendations rather than failing the tool.
	HardcoverAPIKey string

	// TMDBAPIKey is required for the movies tool — like LastFMAPIKey,
	// there's no unauthenticated fallback. Empty means every movies call
	// fails with a clear "not configured" error rather than degrading.
	TMDBAPIKey string

	// Blocklist rejects web_read fetches for blocked domains directly —
	// web_search's own filtering happens inside SearXNG (nil-safe there
	// too), so this only needs plumbing to the one other place a URL can
	// enter the agent loop. Nil means "nothing blocked".
	Blocklist *search.Blocklist

	// DefaultLocation is the static fallback geocoded by nearby_search/
	// weather when a query omits an explicit location and RequestLocation
	// (below) is nil, returns nothing, or isn't set at all — config.yaml's
	// default_location, or the browser's last cached fix if the client
	// sent one with this message (see ClientMessage.UserLocation). Empty
	// means "no fallback at all — location is required."
	DefaultLocation string

	// RequestLocation, when non-nil, asks the connected browser for a
	// live GPS fix right now, blocking until it answers or a timeout
	// passes — the on-demand counterpart to DefaultLocation's static
	// value. See ResolveLocation below for how the two combine. Nil on
	// turns with no live client to ask (e.g. POST /api/ask). Wrapped by
	// handleTurn so it only ever does the actual round trip once per
	// turn, however many location-hungry tool calls ask for it.
	RequestLocation func() (string, bool)

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

	// Cards accumulates structured rich-result items (see Card) a tool
	// wants rendered as their own dedicated block — e.g. music's
	// recommendations carousel — rather than woven into the model's own
	// freeform prose or the citations list. Same concurrency shape as
	// Citations: cardsMu guards it for the same reason (parallel tool
	// dispatch within one turn).
	cardsMu sync.Mutex
	Cards   []Card
}

// ResolveLocation is the one place nearby_search and weather figure out
// what location to use, in order: explicit (whatever the query itself
// named — always wins, it's what the user actually asked for), a live
// round trip to the browser via RequestLocation, then DefaultLocation's
// static fallback. Doing the live round trip here, not proactively when
// the turn starts, is the whole point — it's only attempted when a tool
// call is actually about to need a location, not on every message
// regardless of whether one ever gets used.
func (c *Context) ResolveLocation(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if c.RequestLocation != nil {
		if loc, ok := c.RequestLocation(); ok && loc != "" {
			return loc
		}
	}
	return c.DefaultLocation
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

	// ImageURL is an optional thumbnail (album art, a repo's avatar, an
	// article's lead image, etc.) the frontend renders in place of the
	// source list's numbered index badge when present — general-purpose
	// across any tool, not specific to one. Empty means "no image", the
	// normal case; a tool sets this only when it has a real, working image
	// URL in hand already (see tools/music.go's Deezer cover-art
	// enrichment), never a fabricated/guessed one. Usually per-item (an
	// article's own lead photo), but a shared source-identity badge is a
	// legitimate use too — see reference_lookup.go's arxivLogoURL, the
	// same static image on every arXiv citation on purpose, so it reads
	// as "this came from arXiv" at a glance rather than nothing at all.
	ImageURL string `json:"image_url,omitempty"`
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

// Card is a structured rich-result item — an image, a title, an optional
// subtitle, and a link — meant to be rendered as its own visual block
// (e.g. a carousel) rather than woven into the model's prose or listed as
// a text citation. General-purpose: music's recommendation cards are the
// first user, but nothing here is music-specific, so a future tool (repo
// cards, place cards with photos) can populate the same field.
type Card struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	URL      string `json:"url"`
}

// AddCard appends a card unless its URL is already present, same
// dedup-by-URL rationale as AddCitation. Safe to call concurrently.
func (c *Context) AddCard(card Card) {
	c.cardsMu.Lock()
	defer c.cardsMu.Unlock()
	for _, existing := range c.Cards {
		if existing.URL == card.URL {
			return
		}
	}
	c.Cards = append(c.Cards, card)
}

// CardsSnapshot returns a copy of the cards gathered so far — same
// concurrent-read rationale as CitationsSnapshot.
func (c *Context) CardsSnapshot() []Card {
	c.cardsMu.Lock()
	defer c.cardsMu.Unlock()
	out := make([]Card, len(c.Cards))
	copy(out, c.Cards)
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

// toolDefsByName maps each catalogOrder name to its Go-literal ToolDef —
// the lookup Defs()/AllDefs() iterate over. Function.Description on each
// entry is a placeholder (see e.g. thinkDef) — callers overlay the
// catalog's current APIDescription on top of the copy they get back
// (llm.ToolDef is a plain struct, so byName[name] is already a copy,
// safe to mutate) rather than baking it in here, so an edit to a tool's
// api_description in tools/descriptions/*.yaml is reflected on the very
// next call instead of only at the process's original init() time.
func toolDefsByName() map[string]llm.ToolDef {
	return map[string]llm.ToolDef{
		"think": thinkDef, "web_search": webSearchDef, "web_read": webReadDef,
		"nearby_search": nearbySearchDef, "youtube_transcript": youtubeTranscriptDef, "weather": weatherDef,
		"reference_lookup": referenceLookupDef, "github_repo": githubRepoDef, "dictionary": dictionaryDef,
		"music": musicDef, "books": booksDef, "movies": moviesDef,
	}
}

// Defs returns the tool definitions offered to the model every turn,
// excluding any tool whose required API key isn't configured on ctx
// (currently music/movies — see catalog.go's catalogEntry.offered).
// There's no explicit "reply" tool — the loop runs with tool_choice
// "auto", so the model free-flows between calling tools and just
// answering directly once it has enough context.
func Defs(ctx *Context) []llm.ToolDef {
	catalog := loadCatalog()
	byName := toolDefsByName()
	defs := make([]llm.ToolDef, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		entry := catalog[name]
		if !entry.offered(ctx) {
			continue
		}
		def := byName[name]
		def.Function.Description = entry.APIDescription
		defs = append(defs, def)
	}
	return defs
}

// AllDefs returns every tool definition, ungated — for agent/pseudocall.go's
// paramSchemaType, which has no per-request Context (it's a static-analysis
// path over pseudo-tool-call syntax, not a real per-turn tool offer).
func AllDefs() []llm.ToolDef {
	catalog := loadCatalog()
	byName := toolDefsByName()
	defs := make([]llm.ToolDef, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		def := byName[name]
		def.Function.Description = catalog[name].APIDescription
		defs = append(defs, def)
	}
	return defs
}
