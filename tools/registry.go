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

	"golang.org/x/sync/singleflight"

	"polaris/brave"
	"polaris/embed"
	"polaris/llm"
	"polaris/logger"
	"polaris/parallel"
	"polaris/places"
	"polaris/search"
	"polaris/store"
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
	Brave      *brave.Client            // nil if not configured — web_search's degraded-SearXNG fallback tries this first, ahead of Parallel/Tavily (see tools/web_search.go)
	Parallel   *parallel.Client         // nil if not configured — web_search's degraded-SearXNG fallback (tried after Brave, before Tavily) is skipped without it
	LLM        llm.ChatClient           // the model selected for this thread; reused by web_read's optional filter pass

	// Embed is a local Ollama client used only by agent.Run's
	// query-similarity stale-search signal (see agent/query_similarity.go)
	// — nil disables that one signal, same optional-dependency shape as
	// Brave/Parallel/Tavily above. Never used for anything web_search
	// itself does; the tool package only carries it because Context is
	// where agent.Run reaches for every per-turn dependency.
	Embed *embed.Client

	// BraveUsageThisMonth/IncrementBraveUsage back the monthly cap on
	// Brave calls (store.Store's api_usage table), same shape and same
	// reasoning as ParallelUsageThisMonth/IncrementParallelUsage below —
	// Brave has no ongoing free tier at all (just a one-time signup
	// credit), so this cap matters even more than Parallel's.
	BraveUsageThisMonth func() (int, error)
	IncrementBraveUsage func() error

	// ParallelUsageThisMonth/IncrementParallelUsage back the monthly cap
	// on Parallel calls (store.Store's api_usage table) — narrow closures
	// rather than handing tools a full *store.Store, same pattern as
	// RequestLocation below. Both nil whenever Parallel itself is nil;
	// web_search checks Parallel != nil first, so neither is called in
	// that case. ParallelUsageThisMonth is read before every Parallel
	// call to enforce the cap; IncrementParallelUsage is called only
	// after a call that actually went through, so a request that errored
	// out before reaching Parallel doesn't count against the budget.
	ParallelUsageThisMonth func() (int, error)
	IncrementParallelUsage func() error

	// PinnedProvider, when non-empty, forces web_search to a single
	// provider on every call instead of the normal SearXNG-first,
	// fallback-on-degraded chain — see handleWebSearch in
	// tools/web_search.go. Only "brave" is implemented, for the benchmark
	// harness (cmd/benchmark.go): a reproducible run needs every result to
	// come from the same index across the whole run, not whichever
	// provider happened to answer that particular call. Empty (the
	// default) means normal behavior for every other caller.
	PinnedProvider string

	// ListMemories/GetMemory/WriteMemory/EditMemory/ForgetMemory back the
	// memory tool (tools/memory.go) — narrow closures over store.Store
	// rather than handing tools the whole store, same pattern as
	// BraveUsageThisMonth/IncrementBraveUsage above. All nil together
	// wherever memory shouldn't be offered at all (e.g. the benchmark
	// harness's isolated runs, which want reproducible tool availability,
	// not a real memory store growing from bench queries) — see catalog.go's
	// "memory_store" Requires case, gated on WriteMemory != nil.
	ListMemories func() ([]store.MemoryIndexEntry, error)
	GetMemory    func(name string) (*store.Memory, error)
	WriteMemory  func(name, memType, description, content string) error
	EditMemory   func(name, memType, description, content string) error
	ForgetMemory func(name string) error

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

	// AttachmentData is the raw bytes of this turn's uploaded PDF
	// attachment, when there is one — read_attachment's raw material for
	// paging/searching beyond the short preview resolveAttachment already
	// folded into the message up front (see gateway/attachments.go). Nil
	// for every other case: no attachment, or an image attachment, which
	// is already fully described upfront with no pagination story of its
	// own. Only ever populated for the single turn the attachment was
	// uploaded on — never persisted, so read_attachment has nothing to
	// work with on a later turn in the same thread.
	AttachmentData []byte

	// AttachmentFilename mirrors the uploaded file's display name, for
	// read_attachment's tool_call event payloads. Empty whenever
	// AttachmentData is.
	AttachmentFilename string

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

	// SubAgentRole, when non-empty, marks this Context as belonging to a
	// Tier 2 Deep Research sub-agent (see
	// docs/plans/deep-research-two-tier.md) rather than the orchestrator
	// or an ordinary chat turn. catalogEntry.offered() (catalog.go) uses
	// this to restrict the tool menu to web_search/web_read/think/
	// reference_lookup only (see catalog.go's subAgentToolNames),
	// regardless of what Requires/keys/Category gating would otherwise
	// allow — narrower tool-selection accuracy past ~15-20 tools, fewer
	// tokens per call compounding across N parallel agents, and a
	// sub-agent ingesting untrusted fetched web content (a
	// prompt-injection surface) shouldn't simultaneously hold
	// write-capable tools. The value itself (e.g. "researcher") is
	// currently unused beyond "is this a sub-agent" — reserved for future
	// per-role tool sets rather than a single fixed one for every
	// sub-agent. Empty (the zero value) means normal behavior, so every
	// existing caller that never sets this field is unaffected.
	SubAgentRole string

	// ResearchBudget, when non-nil, is the session-wide search-call budget
	// shared by every sub-agent in one Tier 2 Deep Research fan-out (see
	// ResearchBudget's doc comment in research_budget.go) — one instance
	// created per session and threaded into each sub-agent's Context so
	// they share a single count instead of each tracking its own. Nil
	// (the zero value) means no session-level budget applies, which is
	// correct for every non-sub-agent caller.
	ResearchBudget *ResearchBudget

	// SearchDedup, when non-nil, is the session-wide singleflight.Group
	// shared by every sub-agent in one Tier 2 Deep Research fan-out —
	// dedupedCall (search_dedup.go) uses it so two sub-agent goroutines
	// issuing the same or near-identical query concurrently trigger one
	// real search call and share its result, instead of each paying for
	// its own. One instance created per session, threaded into each
	// sub-agent's Context, same lifecycle as ResearchBudget above. Nil
	// (the zero value) means no dedup applies — correct for every
	// non-sub-agent caller.
	SearchDedup *singleflight.Group

	// SpawnResearchers, when non-nil, runs a Tier 2 Deep Research
	// multi-agent fan-out — wired by gateway/turn.go to
	// agent.SpawnResearchers, which owns the actual goroutine/semaphore/
	// RunSubAgent orchestration. Lives here as a closure (not a direct
	// import) because package tools can't import package agent — agent
	// already imports tools, so that direction would be a cycle. Gated
	// by catalog.go's offered() on ctx.DeepResearch as well as this being
	// non-nil (see its "deep_research" Requires case), so the
	// spawn_researchers tool never appears outside Deep Research mode
	// even if a caller left this wired.
	SpawnResearchers func(ctx *Context, tasks []SubAgentTask) []SubAgentReport

	// QuickMode, when true, tells web_read to skip its optional filter LLM
	// pass entirely (always return raw extracted text, ignoring
	// Instructions) — set for Atlas's Quick Answer, where a fast answer
	// matters more than each individual page read being tightly targeted.
	// Doesn't touch web_search or the tool-calling loop itself — see
	// tools/web_read.go's use of this field for the actual gate.
	QuickMode bool

	// NoResearch, when true, is the composer's "Research" toggle switched
	// off — chat mode. Bulk-excludes every tool tagged category: research
	// (see catalog.go's offered()) and, on top of that, tells the model via
	// an appended prompt fragment (agent.no_research_instruction) that it's
	// in a plain conversational mode and can ask to turn research back on
	// for one reply via ask_user_question's wants_web_search flag rather
	// than silently trying to search anyway. Zero value (false) is normal
	// behavior — every existing caller that never sets this field keeps
	// full tool access, same safe-default shape as VoiceMode/DeepResearch/
	// QuickMode above.
	NoResearch bool

	// PulsarWizard, when true, marks this turn as the ephemeral "help me
	// write the prompt" interview (see gateway/pulsar_wizard.go) rather
	// than a normal chat/pulse turn — the only thing it gates is
	// finalize_pulsar_prompt's offering (catalog.go's "pulsar_wizard"
	// Requires case), so that tool can never appear outside this one
	// context even if a caller left NoResearch/DisabledTools unset. Zero
	// value (false) is normal behavior, same safe-default shape as
	// NoResearch/QuickMode above.
	PulsarWizard bool

	// DisabledTools is the settings panel's per-tool on/off list (see
	// gateway.DisabledToolsFromStore) — a tool named here is excluded
	// regardless of Requires or Category, checked first in offered(). Nil
	// (the zero value) means nothing is disabled, so every existing caller
	// that never sets this field is unaffected, same reasoning as
	// NoResearch above. Keyed by tool name, matching catalogOrder.
	DisabledTools map[string]bool

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

	// Chart holds this turn's chart, if any tool produced one (see
	// ChartSpec). Unlike Citations/Cards this is last-write-wins, not an
	// accumulator — a turn produces at most one chart, so a second
	// SetChart call (e.g. Tier-1 weather auto-chart plus a Tier-2
	// visualize call in the same turn) simply overwrites the first rather
	// than needing dedup logic. chartMu guards it for the same
	// concurrent-dispatch reason Citations/Cards need their own mutexes.
	chartMu sync.Mutex
	Chart   *ChartSpec

	// PendingQuestion, once set, tells agent.Run to end the turn right
	// after this batch of tool calls instead of looping back to the
	// model — see ask_user_question.go. Unlike Citations/Cards this is
	// first-write-wins, not an accumulator: the tool's own description
	// tells the model to ask one focused question per turn, and only the
	// first call in a batch should ever actually end it. pendingQuestionMu
	// guards it for the same concurrent-dispatch reason Citations/Cards
	// need their own mutexes.
	pendingQuestionMu sync.Mutex
	PendingQuestion   *PendingQuestion

	// WizardFinal, once set, tells agent.Run to end the turn the same way
	// PendingQuestion does — see finalize_pulsar_prompt.go and
	// PulsarWizard above. Only ever populated on a PulsarWizard turn,
	// since finalize_pulsar_prompt is never offered otherwise.
	// wizardFinalMu guards it for the same concurrent-dispatch reason
	// PendingQuestion's mutex exists.
	wizardFinalMu sync.Mutex
	WizardFinal   *WizardFinal
}

// WizardFinal is the tuned prompt the model drafted once it decided the
// Pulsar prompt wizard interview had enough to go on — see
// finalize_pulsar_prompt.go. Unlike PendingQuestion this is never
// persisted anywhere: the whole wizard session is ephemeral, held only in
// gateway/pulsar_wizard.go's in-memory session map.
type WizardFinal struct {
	Prompt string `json:"prompt"`
	// Name is an optional suggested routine name — left blank if the
	// model didn't propose one, in which case the frontend leaves
	// whatever the user already typed (if anything) alone.
	Name string `json:"name,omitempty"`
}

// SetWizardFinal records the turn-ending drafted prompt, if none has been
// recorded yet this turn — same first-write-wins reasoning as
// SetPendingQuestion.
func (c *Context) SetWizardFinal(f *WizardFinal) {
	c.wizardFinalMu.Lock()
	defer c.wizardFinalMu.Unlock()
	if c.WizardFinal == nil {
		c.WizardFinal = f
	}
}

// PendingQuestion is a clarifying question the model asked instead of
// answering — see ask_user_question.go. Persisted as part of the
// assistant message that asked it (store.Message.PendingQuestion) so it
// survives reloads and restarts: answering it is just sending the next
// ordinary chat message in the thread, not a live round trip, so there's
// nothing else to keep alive in memory.
type PendingQuestion struct {
	Question      string   `json:"question"`
	Options       []string `json:"options,omitempty"`
	WantsLocation bool     `json:"wants_location,omitempty"`
	// WantsWebSearch mirrors WantsLocation's shape for a different missing
	// capability: set when the model wants to ask whether to turn research
	// back on for chat mode (NoResearch above) — shows an "enable web
	// search" action alongside the text input, same as WantsLocation's
	// "share my location". See ask_user_question.go.
	WantsWebSearch bool `json:"wants_web_search,omitempty"`

	// Plan, when set, is a Tier 2 Deep Research plan-confirmation question
	// (docs/plans/deep-research-two-tier.md's "Confirm" step) — the
	// orchestrator's proposed spawn_researchers fan-out, attached purely
	// so the frontend can render a richer plan card instead of parsing it
	// back out of Question's prose. The plan's content is also written
	// into Question itself, so a client that doesn't render Plan
	// specially still shows the full plan as normal text — this is an
	// enhancement, not the source of truth.
	Plan *ResearchPlan `json:"plan,omitempty"`
}

// ResearchPlan is PendingQuestion's structured Deep Research plan — see
// its doc comment above.
type ResearchPlan struct {
	// SubAgentObjectives is one entry per sub-agent the orchestrator is
	// proposing to spawn, matching what it intends to pass to
	// spawn_researchers if confirmed.
	SubAgentObjectives []string `json:"sub_agent_objectives"`
	// EstimatedSearchCalls is an optional rough total-call estimate for
	// the whole plan — 0 means the orchestrator didn't provide one, not a
	// claim of "zero calls needed".
	EstimatedSearchCalls int `json:"estimated_search_calls,omitempty"`
}

// SetPendingQuestion records the turn-ending question, if none has been
// recorded yet this turn. First-write-wins rather than overwriting or
// erroring on a second call — dispatchToolCallsConcurrently could in
// principle run two ask_user_question calls from the same batch in
// parallel (the model was told not to, but nothing enforces that), and
// silently keeping whichever one landed first is a safer failure mode
// than a data race or a nondeterministic "last one wins".
func (c *Context) SetPendingQuestion(q *PendingQuestion) {
	c.pendingQuestionMu.Lock()
	defer c.pendingQuestionMu.Unlock()
	if c.PendingQuestion == nil {
		c.PendingQuestion = q
	}
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
	// Kind selects which frontend treatment renders this card. Empty/
	// omitted means "media" — today's carousel behavior, unchanged for
	// every existing caller (music/movies/books never set this field).
	// image_search is the only "image" caller — see its doc comment.
	Kind string `json:"kind,omitempty"` // "" (media, default) | "image"
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

// ChartSpec is a structured chart a tool wants rendered instead of (or
// alongside) its prose answer — either attached deterministically by a
// tool whose own response is already a time series (Tier 1, e.g. weather),
// or built by the model itself via the visualize tool (Tier 2). See
// docs/plans/visualize-and-image-search.md.
type ChartSpec struct {
	Kind   string        `json:"kind"` // "line" | "bar" | "timeline" | "meter"
	Title  string        `json:"title"`
	XLabel string        `json:"x_label,omitempty"`
	YLabel string        `json:"y_label,omitempty"`
	Series []ChartSeries `json:"series,omitempty"` // line, bar
	Events []ChartEvent  `json:"events,omitempty"` // timeline
	Value  *ChartValue   `json:"value,omitempty"`  // meter
}

type ChartSeries struct {
	Label  string       `json:"label"`
	Points []ChartPoint `json:"points"`
}

type ChartPoint struct {
	X interface{} `json:"x"` // string (date/category) or number
	Y float64     `json:"y"`
}

// ChartEvent is "timeline"'s own shape — deliberately not a ChartPoint. A
// timeline has no numeric value to put in Y; wedging it into ChartPoint
// with a mandatory-but-meaningless Y was the first draft's mistake.
type ChartEvent struct {
	Date  string `json:"date"`
	Label string `json:"label"`
}

type ChartValue struct {
	Current float64 `json:"current"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Label   string  `json:"label"`
}

// SetChart replaces this turn's chart. Last-write-wins, not append — see
// the Chart field's doc comment above for why. Safe to call concurrently.
func (c *Context) SetChart(spec ChartSpec) {
	c.chartMu.Lock()
	defer c.chartMu.Unlock()
	c.Chart = &spec
}

// ChartSnapshot returns this turn's chart, if any — same concurrent-read
// rationale as CitationsSnapshot/CardsSnapshot.
func (c *Context) ChartSnapshot() *ChartSpec {
	c.chartMu.Lock()
	defer c.chartMu.Unlock()
	return c.Chart
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
		"think": thinkDef, "calculator": calculatorDef, "web_search": webSearchDef, "web_read": webReadDef,
		"nearby_search": nearbySearchDef, "youtube_transcript": youtubeTranscriptDef, "weather": weatherDef,
		"reference_lookup": referenceLookupDef, "github_repo": githubRepoDef, "dictionary": dictionaryDef,
		"music": musicDef, "books": booksDef, "movies": moviesDef, "visualize": visualizeDef,
		"image_search": imageSearchDef, "read_attachment": readAttachmentDef,
		"ask_user_question": askUserQuestionDef, "memory": memoryDef, "spawn_researchers": spawnResearchersDef,
		"finalize_pulsar_prompt": finalizePulsarPromptDef,
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
