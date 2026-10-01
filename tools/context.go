package tools

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"

	"polaris/brave"
	"polaris/embed"
	"polaris/jev"
	"polaris/llm"
	"polaris/parallel"
	"polaris/places"
	"polaris/reddit"
	"polaris/search"
	"polaris/store"
	"polaris/tavily"
)

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
	Tavily     *tavily.Client           // nil if not configured — web_read's JS-render/paywall fallback and its force_tavily argument are both skipped without it
	Brave      *brave.Client            // nil if not configured — web_search's degraded-SearXNG fallback tries this first, ahead of Parallel/Tavily (see tools/web_search.go)
	Reddit     *reddit.Client           // nil is fine — web_read builds an RSS-only client on demand, so Reddit URLs are readable without any key (see the reddit package)
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

	// TavilyUsageThisMonth/IncrementTavilyUsage back the monthly cap on
	// Tavily calls (store.Store's api_usage table) — same shape as
	// BraveUsageThisMonth/IncrementBraveUsage above. One counter shared
	// across every way this deployment can spend a Tavily credit: the
	// SearXNG-degraded Search fallback (tools/web_search.go's
	// tavilyFallback), web_read's own JS-render/paywall Extract fallback,
	// and web_read's explicit force_tavily argument (tools/web_read.go) —
	// unlike Brave/Parallel, nothing enforced a real ceiling on Tavily
	// before this (it was only "scarce" by comment/convention), which
	// mattered less while every Tavily call was a last-resort fallback
	// the model reached only accidentally; force_tavily lets the model
	// spend a credit on purpose, so the cap needs to be real too.
	TavilyUsageThisMonth func() (int, error)
	IncrementTavilyUsage func() error

	// Jev backs compare_sources (tools/compare_sources.go) — nil if
	// OpenRouter itself isn't configured (jev.NewClient's nil-means-
	// unconfigured convention, same as Brave/Parallel/Tavily above), since
	// Jev is reached through OpenRouter's beta endpoint using the exact
	// same api_key that already authenticates the main chat model, not a
	// separate secret. JevCostThisMonth/LogJevCost back the monthly dollar
	// cap (store.Store's jev_usage table) — dollar-based, not call-count-
	// based like Brave/Parallel/Tavily's api_usage, since Jev bills by
	// token and one heavily-chunked comparison could cost more than a
	// typical call. LogJevCost is also how Stats.VerificationCostUSD's
	// breakout gets its data — call it once per real Jev call, after
	// AddCost/AddJevCost below, same "record only what actually went
	// through" convention as IncrementBraveUsage.
	Jev              *jev.Client
	JevCostThisMonth func() (float64, error)
	LogJevCost       func(usd float64) error

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
	WriteMemory  func(name, memType, description, content, occurredAt string) error
	EditMemory   func(name, memType, description, content, occurredAt string) error
	ForgetMemory func(name string) error

	// SearchThreads/ListRecentThreads/ReadThread back the search_chats tool
	// (tools/search_chats.go) — narrow closures over store.Store, same
	// pattern as the memory closures above. All three wired together at the
	// same call sites or not at all — same "one non-nil closure implies the
	// rest are too" convention memory's five closures already establish
	// (see catalog.go's "chat_search" Requires case, gated on
	// SearchThreads != nil). Nil wherever a real chat history shouldn't be
	// searched/read at all (e.g. cmd/benchmark.go's isolated runs — same
	// reasoning as that command deliberately skipping the memory closures
	// too).
	SearchThreads     func(query string, limit int) ([]store.MessageSearchResult, error)
	ListRecentThreads func(cursor string) (threads []store.ThreadSummary, nextCursor string, err error)
	ReadThread        func(threadID string) (*store.ThreadReadResult, error)

	// WeaverRun is true inside any Weaver agent loop — a scheduled
	// shooting-star run (gateway/constellation_weaver.go) or an
	// interactive "Talk to Weaver" session (gateway/turn.go's
	// isWeaverThread, issue #94) — gates the five weaver_run tools
	// (tools/search_stars.go, read_star.go, create_star.go, update_star.go,
	// link_stars.go) so they're never offered on a normal chat/pulse turn,
	// same "requires:" gating shape as pulsar_daily_items/wizard
	// above. The five WeaverX closures below are only ever wired alongside
	// this being true.
	WeaverRun bool
	// WeaverInteractive distinguishes the two WeaverRun cases from each
	// other for agent/driver.go's loadSystemPrompt: false (a shooting
	// star) picks prompts.yaml's weaver.system, the silent-background-
	// extraction framing; true (only ever set by gateway/turn.go) picks
	// weaver.interactive_system instead, framed as a live conversation
	// where the person's own messages are direct instructions to act on
	// rather than raw content to extract facts from. Meaningless unless
	// WeaverRun is also true.
	WeaverInteractive bool

	// WeaverSearchStars/WeaverReadStar/WeaverCreateStar/WeaverUpdateStar/
	// WeaverLinkStars back Weaver's five tools — narrow closures over
	// store.Store plus the current shooting_star_runs.id (create_star/
	// update_star need it to log shooting_star_candidates as a side effect
	// of the call itself — see docs/plans/constellation.md's "Weaver's
	// tools"), same narrow-closure pattern as the memory/search_chats
	// closures above rather than handing Weaver's tools a whole *store.Store.
	WeaverSearchStars func(query string) ([]store.StarSearchResult, error)
	WeaverReadStar    func(starID int64) (*store.Star, error)
	// reasoning on WeaverCreateStar/WeaverUpdateStar is logged into
	// shooting_star_candidates.reasoning — the Review screen's "Why this
	// needs a look" block (see docs/plans/constellation.md's "Reviewing a
	// proposed star") reads exactly this field, previously always logged
	// as "" because neither tool's schema had anywhere for the model to
	// put it.
	WeaverCreateStar func(title, category, summary, body string, tags []string, confidenceClass string, isPersonal bool, reasoning string) (int64, error)
	// WeaverUpdateStar's isPersonal is a *bool, unlike WeaverCreateStar's
	// plain bool: update_star's tool schema doesn't require is_personal, so
	// a model call that omits it must leave the star's existing value
	// alone rather than silently flipping it to false (see
	// store.UpdateStar's doc comment on the same "" == "leave as-is"
	// contract for body/tags/confidenceClass). title shares that same ""
	// == "leave as-is" contract — update_star's schema makes it optional
	// too, since most updates don't change what the star is about.
	WeaverUpdateStar func(starID int64, title, summary, body string, tags []string, confidenceClass string, isPersonal *bool, reasoning string) error
	WeaverLinkStars  func(starIDA, starIDB int64, reasoning string) error

	// StarsSearch/StarsRead back the stars tool (tools/stars.go) — the main
	// assistant's own read-only search over Constellation's library
	// (issue #56), distinct from WeaverSearchStars/WeaverReadStar above:
	// those are Weaver's internal dedup/link-discovery lookups, which
	// deliberately still see rejected/disabled stars (see read_star.go's
	// "that's a stop sign" handling); StarsRead/StarsSearch must not — a
	// rejected or disabled star is exactly what shouldn't resurface to the
	// person through their own assistant. Narrow closures over store.Store,
	// same pattern as the memory/search_chats closures above. Both nil
	// together wherever the library shouldn't be searchable at all — a
	// ghost turn (issue #67), same "no persisted-store reads leaking into
	// an incognito session" reasoning gateway/turn.go already applies to
	// SearchThreads/WriteMemory there — see catalog.go's "stars_library"
	// Requires case, gated on StarsSearch != nil.
	StarsSearch func(query string, limit int) ([]store.Star, error)
	StarsRead   func(starID int64) (*store.Star, error)

	// WeaverCategoriesInUse lists every category value already in the
	// library (store.Store's DistinctCategories, comma-joined) — substituted
	// into weaver.system's own escape-hatch instruction so a category
	// outside the fixed list can actually be checked against what already
	// exists instead of guessed blind (search_stars' results carry no
	// category field). Empty on a from-scratch library, which is fine —
	// the escape hatch just has nothing to reuse yet.
	WeaverCategoriesInUse string

	// WeaverPersonName/WeaverPersonPronouns are optional operator-supplied
	// guidance (the general settings panel's "About you" section, see
	// gateway.PersonNameFromStore/PersonPronounsFromStore — not
	// Constellation-specific, despite the Weaver- prefix here) about who
	// Weaver is writing personal stars about — both "" by default, meaning
	// no guidance to inject. Prepended to weaver.system by agent/driver.go's
	// loadSystemPrompt, not substituted into it, so an empty pair costs
	// nothing (unlike WeaverCategoriesInUse's %s, which is always
	// substituted in). Set independently from PersonName/PersonPronouns
	// below (same underlying setting, two separate reads) since Weaver's
	// tools.Context is built by its own newWeaverToolContext, never
	// touching the main assistant's turn-building path.
	WeaverPersonName     string
	WeaverPersonPronouns string

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

	// Multimodal reports whether this thread's own selected model (ctx.LLM)
	// is itself vision-capable — see config.ModelConfig.Multimodal. Gates
	// view_image's "see" mode (tools/view_image.go): only offered in that
	// tool's mode enum when this is true, since inserting a real image into
	// the conversation (see llm.ChatMessage.ImageURLs) only means anything
	// for a model that can actually look at it. A false value doesn't mean
	// view_image is unavailable — "describe" mode still works via
	// DescribeImage below, same fallback-to-a-configured-vision-model
	// behavior resolveAttachment already has for uploaded images.
	Multimodal bool

	// DescribeImage, when non-nil, asks a vision-capable model (this
	// thread's own, if Multimodal above, else a configured fallback — see
	// gateway's visionClient) to describe an image and return the text —
	// the "describe" half of view_image's mode parameter. instructions
	// mirrors web_read's own optional focus parameter; empty means "a full
	// literal description". nil only when no multimodal model is
	// configured at all (neither this thread's model nor any fallback),
	// matching Brave/Parallel/Tavily's same nil-means-unavailable shape
	// elsewhere on this struct.
	DescribeImage func(ctx context.Context, imageBase64, mimeType, instructions string) (description string, costUSD float64, err error)

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

	// OracleSection is Oracle mode's whole "## Oracle" system-prompt block
	// (docs/plans/oracle-mode.md, issue #122), already built from
	// prompts.yaml's oracle.section template — gateway/turn.go sets this
	// from gateway.RunOracle's result before agent.Run starts. Appended by
	// agent.loadSystemPrompt the same way DeepResearch/NoResearch's own
	// instructions are, and re-injected near the end of the message list
	// by Run itself, same reason FocusMode is (see modeReinforcement's doc
	// comment — a standing instruction at position 0 drifts out of
	// attention as history grows). "" whenever Oracle is off, unconfigured,
	// or fired no checks this turn.
	OracleSection string

	// Wizard, when non-nil, marks this turn as the ephemeral "help me
	// write this" interview (see gateway/wizard.go) rather than a normal
	// chat/pulse turn, and says what the interview is writing (see
	// WizardTarget). Two things key off it: finalize_wizard_prompt's
	// offering (catalog.go's "wizard" Requires case), so that tool can
	// never appear outside this one context even if a caller left
	// NoResearch/DisabledTools unset, and agent/driver.go's
	// loadSystemPrompt, which swaps in the target's own system prompt
	// instead of prompt.md's persona. nil is normal behavior, same
	// safe-default shape as NoResearch/QuickMode above.
	Wizard *WizardTarget

	// PulsarDailyItems, when true, marks this turn as a Pulsar Daily
	// block generation whose content is a list of distinct stories
	// (headlines/trending/custom blocks), not a single narrative — the
	// only thing it gates is finalize_daily_items's offering (catalog.go's
	// "pulsar_daily_items" Requires case), same isolation Wizard
	// gives finalize_wizard_prompt. Only ever set true by
	// gateway/pulsar_daily.go's block-context builder, never in a normal
	// chat/pulse turn. Zero value (false) is normal behavior.
	PulsarDailyItems bool

	// DisabledTools is the settings panel's per-tool on/off list (see
	// gateway.DisabledToolsFromStore) — a tool named here is excluded
	// regardless of Requires or Category, checked first in offered(). Nil
	// (the zero value) means nothing is disabled, so every existing caller
	// that never sets this field is unaffected, same reasoning as
	// NoResearch above. Keyed by tool name, matching catalogOrder.
	DisabledTools map[string]bool

	// CustomInstructions is the settings panel's free-text field (see
	// gateway.CustomInstructionsFromStore) — operator-authored steering
	// ("always answer in French", "I'm a nurse, use clinical terminology")
	// substituted into prompt.md wherever it writes "{custom_instructions}"
	// (see agent/driver.go's applyCustomInstructionsPlaceholder). Empty
	// string (the zero value, and the default until the operator sets one)
	// collapses that placeholder to nothing, same as CustomInstructions
	// being genuinely unset.
	CustomInstructions string

	// PersonName/PersonPronouns are the main assistant's own read of the
	// same "About you" settings-panel fields WeaverPersonName/
	// WeaverPersonPronouns above use — substituted into prompt.md wherever
	// it writes "{person}" (see agent/driver.go's applyPersonPlaceholder),
	// collapsing to nothing when both are unset. Left unset for a ghost
	// turn (gateway/turn.go), same "no personalization" convention
	// CustomInstructions follows there — knowing the operator's name is
	// exactly the kind of thing an incognito turn shouldn't see.
	PersonName     string
	PersonPronouns string

	// ThreadID is the current thread's ID (gateway/turn.go's
	// storageThreadID) — code_exec joins it onto CodeExecWorkspaceDir/
	// CodeExecHostWorkspaceDir to name the thread's persistent workspace
	// directory (see docs/plans/code-execution.md's "File persistence").
	// Empty wherever there's no real thread to key a workspace off of
	// (the benchmark harness's isolated runs, sub-agent turns) — those
	// callers also never set CodeExecEnabled, so this being empty is
	// never reached by code_exec's own handler.
	ThreadID string

	// FieldID is the Field the current thread belongs to (docs/plans/
	// fields.md, issue #119) — empty for an ordinary ungrouped thread, and
	// also for sub-agent/benchmark contexts with no real thread. Read off the
	// root thread's row each turn by gateway/turn.go. Non-empty means three
	// things downstream: code_exec mounts <CodeExecWorkspaceDir>/<FieldID>/
	// read-only at /field, the workspace read paths fall back to that
	// directory for a file the thread's own directory lacks, and
	// save_to_field is offered (catalog.go's "field_workspace" case).
	FieldID string

	// CodeExecEnabled gates the code_exec tool (catalog.go's
	// "docker_only" Requires case) — true only when gateway's
	// deploymentMode() reports "docker" AND
	// config.Config.CodeExec.HostWorkspaceDir is actually set (see
	// gateway/turn.go's wiring). Bare-metal has no container boundary
	// for arbitrary code, so this stays false there unconditionally
	// rather than running generated code as a direct host subprocess —
	// see docs/plans/code-execution.md's "Deployment scope".
	CodeExecEnabled bool

	// CodeExecWorkspaceDir is this container's own view of the
	// per-thread workspace root (config.Config.CodeExec.WorkspaceDir,
	// e.g. "/data/workspaces") — code_exec joins ThreadID onto this for
	// its own file reads/writes. Meaningless to the host-side sandbox
	// runner; see CodeExecHostWorkspaceDir for the path that actually
	// matters to it.
	CodeExecWorkspaceDir string

	// CodeExecHostWorkspaceDir is the real Docker-host filesystem path
	// backing the same directory CodeExecWorkspaceDir names from inside
	// this container (config.Config.CodeExec.HostWorkspaceDir) — written
	// into the sandbox request file so the host-side runner
	// (compose/watcher/codeexec.sh), which runs outside any container,
	// knows what to bind-mount into the ephemeral sandbox container. See
	// docs/plans/code-execution.md's "How Polaris's own container
	// reaches Docker" for why the two paths can't be the same string.
	CodeExecHostWorkspaceDir string

	// CodeExecSignalDir is the bind-mounted directory code_exec uses to
	// hand a request off to the host-side sandbox runner and read its
	// result back — same request/result-file handoff shape as
	// gateway/docker_update.go's update-signal/, chosen specifically so
	// this container never gets Docker socket access itself (see
	// docs/plans/code-execution.md).
	CodeExecSignalDir string

	// CodeExecMemoryLimitMB/CodeExecPidsLimit/CodeExecTimeoutSeconds are
	// the resource ceilings passed through to the host-side script's
	// `docker run` invocation for the sandbox container — see
	// config.Config.CodeExec's doc comment for the measured defaults.
	CodeExecMemoryLimitMB  int
	CodeExecPidsLimit      int
	CodeExecTimeoutSeconds int

	// UITheme is the settings panel's current "dark"/"light" toggle (see
	// gateway/settings.go's ThemeFromStore) — only wired when
	// CodeExecEnabled, to fill CodeExecThemePrompt's chart-styling
	// guidance with the UI's real colors instead of matplotlib's
	// defaults. Empty means "not wired" (bare-metal, tests), which
	// CodeExecThemePrompt treats as "dark" rather than emitting nothing,
	// since dark is this app's own default theme.
	UITheme string

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

	// ImageCandidates is image_search's result pool: every image a search
	// found this turn, numbered 1-based in the order found, *without* being
	// rendered anywhere. Deliberately separate from Cards — Cards is what
	// the frontend displays at end of turn, so an image_search that fed it
	// directly forced the whole result set onto the user before the model
	// had judged any of it (issue #124). view_image, fetch_url and show's
	// image_indices all resolve a model-supplied image number against this
	// pool instead. Same concurrency shape as Cards.
	imageCandidatesMu sync.Mutex
	ImageCandidates   []Card

	// ExtraCostUSD accumulates LLM spend a tool handler incurred on its
	// own — a filter/extraction pass (web_read's instructions param, see
	// FilterExtractedText) — that agent.Run's own
	// per-turn ChatCompletionWithTools/ChatCompletionStreaming calls never
	// see, since that spend happens inside a tool handler's own separate
	// LLM call, not the main loop. Without this, it's real spend (already
	// billed by OpenRouter) that's invisible everywhere Polaris reports a
	// thread's total cost. Same concurrency shape as Citations/Cards:
	// extraCostMu guards it for the same parallel-tool-dispatch reason.
	extraCostMu  sync.Mutex
	ExtraCostUSD float64

	// Evidence accumulates URL -> raw extracted text for every page
	// web_read successfully fetches this turn — a shared prerequisite for
	// both compare_sources and the per-claim verification badge (see
	// docs/plans/source-verification.md); tools.Citation only ever
	// carried title/URL/site/image, never the actual fetched text.
	// Deliberately the *raw* extracted text, not FilterExtractedText's
	// LLM-filtered output — checking a claim against an LLM's own summary
	// would be circular. One URL maps to a *slice* of texts, not one
	// string: a multi-page PDF read across a turn (different `page`
	// arguments, same URL) needs the union of every page actually read,
	// not just the last one — an earlier single-string version silently
	// dropped every page but the last. evidenceMu guards it for the same
	// parallel-tool-dispatch reason Citations/Cards need their own
	// mutexes.
	evidenceMu sync.Mutex
	evidence   map[string][]string

	// jevCostMu/jevCostUSD track this turn's own running Jev spend,
	// separately from ExtraCostUSD (which mixes in every other tool's
	// extra LLM spend too) — see AddJevCost/JevSpentThisTurn. This is what
	// compare_sources checks against the $0.01/turn cap before firing
	// another call, not ExtraCostUSD, which isn't Jev-specific.
	jevCostMu  sync.Mutex
	jevCostUSD float64

	// Chart holds this turn's chart, if any tool produced one (see
	// ChartSpec) — today, only weather.go's deterministic "range" chart
	// (the standalone visualize tool that once also called SetChart was
	// removed in favor of code_exec's general-purpose plotting, see
	// issue #44). Unlike Citations/Cards this is last-write-wins, not an
	// accumulator, in case that ever changes again. chartMu guards it for
	// the same concurrent-dispatch reason Citations/Cards need their own
	// mutexes.
	chartMu sync.Mutex
	Chart   *ChartSpec

	// showMuLock/showURL/showCaption back SetShow/ShowSnapshot — see
	// ShowSnapshot's doc comment below for why this exists alongside
	// show.go's normal ctx.Emit-driven path.
	showMuLock  sync.Mutex
	showURL     string
	showCaption string

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
	// PendingQuestion does — see finalize_wizard_prompt.go and
	// Wizard above. Only ever populated on a Wizard turn,
	// since finalize_wizard_prompt is never offered otherwise.
	// wizardFinalMu guards it for the same concurrent-dispatch reason
	// PendingQuestion's mutex exists.
	wizardFinalMu sync.Mutex
	WizardFinal   *WizardFinal

	// DailyItemsFinal, once set, tells agent.Run to end the turn the same
	// way WizardFinal does — see finalize_daily_items.go and
	// PulsarDailyItems above. Only ever populated on a Pulsar Daily
	// list-block generation, since finalize_daily_items is never offered
	// otherwise. dailyItemsFinalMu guards it for the same concurrent-
	// dispatch reason WizardFinal's mutex exists.
	dailyItemsFinalMu sync.Mutex
	DailyItemsFinal   *DailyItemsFinal

	// PendingImageMessages accumulates synthetic "user" messages carrying a
	// real image (view_image's "see" mode — see llm.ChatMessage.ImageURLs)
	// from this batch of tool calls, for agent.Run to append to the
	// conversation AFTER every "tool" role result message in the batch —
	// never interleaved between them. That ordering isn't a style choice:
	// the OpenAI-compatible wire protocol requires one assistant message
	// carrying every tool call from a turn, immediately followed by ALL of
	// that batch's tool-result messages with nothing else in between (see
	// agent/driver.go's dispatch loop comment — DeepSeek 400s with
	// "insufficient tool messages following tool_calls message" otherwise).
	// An accumulator, not first-write-wins like PendingQuestion/WizardFinal
	// above: more than one view_image "see" call could land in the same
	// batch. pendingImageMu guards it for the same concurrent-dispatch
	// reason Citations/Cards need their own mutexes.
	pendingImageMu       sync.Mutex
	PendingImageMessages []llm.ChatMessage
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

// AddCost records LLM spend a tool handler incurred internally — see
// ExtraCostUSD's doc comment for why this exists at all. Safe to call
// concurrently from multiple tool handlers dispatched in parallel, same
// reasoning as AddCitation/AddCard. agent.Run reads ctx.ExtraCostUSD
// directly (not via a snapshot method) when building its Result, the same
// way it reads ctx.Citations/ctx.Cards directly — safe because that read
// only ever happens after a turn's dispatch has fully joined, provably
// sequential with every AddCost call that ran during it.
func (c *Context) AddCost(usd float64) {
	c.extraCostMu.Lock()
	defer c.extraCostMu.Unlock()
	c.ExtraCostUSD += usd
}

// AddJevCost records real Jev spend against this turn's own running total
// and returns the new total — see jevCostUSD's doc comment for why this is
// separate from AddCost/ExtraCostUSD. Safe to call concurrently.
func (c *Context) AddJevCost(usd float64) float64 {
	c.jevCostMu.Lock()
	defer c.jevCostMu.Unlock()
	c.jevCostUSD += usd
	return c.jevCostUSD
}

// JevSpentThisTurn returns this turn's running Jev spend so far — checked
// against the per-turn cap before firing another call. Safe to call
// concurrently.
func (c *Context) JevSpentThisTurn() float64 {
	c.jevCostMu.Lock()
	defer c.jevCostMu.Unlock()
	return c.jevCostUSD
}
