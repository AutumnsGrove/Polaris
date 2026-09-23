package tools

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// catalogOrder is the fixed, deterministic tool order used everywhere a
// tool list is rendered or offered to the model — Defs(), ToolsPrompt(),
// and AllDefs() all iterate in this exact order, so the wire-format tool
// list and the {tools} prompt substitution stay byte-identical across
// requests/restarts (see config.go's OpenRouter provider-pinning comment
// for why: prompt-prefix caching depends on this).
var catalogOrder = []string{
	"think", "calculator", "current_time", "web_search", "web_read", "nearby_search", "youtube_transcript",
	"weather", "reference_lookup", "github_repo", "github_activity", "dictionary", "music", "books", "movies", "code_exec", "fetch_url",
	"image_search", "view_image", "show", "highlight", "ask_user_question", "memory", "search_chats", "stars", "spawn_researchers", "finalize_pulsar_prompt",
	"finalize_daily_items", "search_stars", "read_star", "create_star", "update_star", "link_stars", "compare_sources",
}

// catalogDescriptionsDir is where each tool's YAML file lives — read fresh
// (subject to the per-file mtime cache below) so it's hot-editable, same
// convention as prompt.md/prompts.yaml (see prompts/prompts.go's doc
// comment on Get).
const catalogDescriptionsDir = "tools/descriptions"

// subAgentToolNames is the fixed tool set offered to a Tier 2 Deep
// Research sub-agent (Context.SubAgentRole set) — see offered() below.
var subAgentToolNames = map[string]bool{
	"web_search":       true,
	"web_read":         true,
	"think":            true,
	"reference_lookup": true, // Wikipedia/arXiv — no API key required, same research-only spirit as web_search/web_read
}

// catalogEntry is one tools/descriptions/*.yaml file, parsed.
type catalogEntry struct {
	Name     string `yaml:"name"`
	Requires string `yaml:"requires"`
	// Category is empty for most tools, or "research" for the ones that
	// reach out for external information (web search, page fetches, the
	// recommendation lookups) — see offered() below. Distinct from
	// Requires: Requires is capability-gating (can this tool even run right
	// now), Category is a behavioral grouping used to bulk-exclude tools
	// for chat mode, independent of whether the tool is otherwise usable.
	Category       string `yaml:"category"`
	Description    string `yaml:"description"`
	APIDescription string `yaml:"api_description"`
}

// offered reports whether ctx has whatever key entry.Requires needs — the
// same gating both Defs() and ToolsPrompt() apply, so a tool excluded from
// the wire-format tool list is never mentioned in the system prompt's tool
// list either. An unrecognized Requires value (a typo, or a new tool's
// YAML file shipped before a matching case is added here) fails closed —
// excluded and logged — rather than silently defaulting to "always
// offered", since the whole point of Requires is to keep an unusable tool
// off the model's menu.
func (e catalogEntry) offered(ctx *Context) bool {
	if ctx.DisabledTools[e.Name] {
		// User-preference gate, checked first and unconditionally — a tool
		// disabled from the settings panel stays off regardless of what
		// Requires or Category would otherwise decide. Reading a nil map
		// is safe in Go (returns false), so this needs no nil check for
		// every Context that never sets DisabledTools at all (most tests,
		// the benchmark harness).
		return false
	}
	if ctx.NoResearch && e.Category == "research" {
		// Chat mode: bulk-exclude every tool tagged "research" instead of
		// hardcoding a tool-name list here and in the prompt fragment that
		// explains the situation (agent/driver.go's noResearch branch) —
		// a future research tool just needs category: research in its own
		// YAML file to be included in this, not a second list to remember.
		return false
	}
	if ctx.SubAgentRole != "" && !subAgentToolNames[e.Name] {
		// Tier 2 Deep Research sub-agent: restrict the menu to the fixed
		// research-only set regardless of what Requires/keys/Category
		// gating below would otherwise allow — see SubAgentRole's doc
		// comment in registry.go for why. Checked by name rather than
		// Category so a sub-agent doesn't also lose think (Category ""),
		// which it still needs for its own reasoning.
		return false
	}
	if ctx.WeaverRun && e.Requires != "weaver_run" && e.Requires != "chat_search" {
		// Weaver: restrict the menu to its own five tools plus search_chats
		// — never think/calculator/web_search/anything else from the main
		// catalog, since Weaver's whole job is reading and inferring from
		// already-written chat content, not researching or computing
		// anything new (see docs/plans/constellation.md's "Weaver" design
		// principle: "never Polaris's main chat agent gaining a tool").
		// search_chats is the one deliberate exception, added 2026-09-18:
		// still reading already-written chat content, just from threads
		// other than the one this run is about — useful for checking
		// whether a related fact already came up elsewhere before deciding
		// create_star vs. update_star, the same judgment search_stars
		// already exists to support but scoped to raw conversations
		// instead of the stars library itself. Gated on ctx.SearchThreads
		// != nil below (the "chat_search" case), same as the main
		// assistant — see newWeaverToolContext for where Weaver wires it.
		return false
	}
	switch e.Requires {
	case "":
		return true
	case "lastfm_api_key":
		return ctx.LastFMAPIKey != ""
	case "tmdb_api_key":
		return ctx.TMDBAPIKey != ""
	case "jev":
		// compare_sources needs a real Jev client, which itself needs
		// OpenRouter configured (see jev.NewClient's nil-means-
		// unconfigured convention) — no separate API key of its own.
		return ctx.Jev != nil
	case "interactive_chat":
		// Reuses the exact "is there a live client on the other end of
		// this turn" signal RequestLocation already encodes — nil on
		// POST /api/ask (see gateway/ask.go, which passes
		// requestLocation=nil), non-nil on the WebSocket chat path (see
		// gateway/turn.go). A question that ends the turn and waits for
		// the user's next message is meaningless on a one-shot API call
		// with no thread the caller will ever come back to answer it in.
		return ctx.RequestLocation != nil
	case "memory_store":
		// Gated on WriteMemory rather than a dedicated bool: every wiring
		// site sets all five memory closures together (see gateway/turn.go,
		// cmd/search.go) or none at all (cmd/benchmark.go, deliberately —
		// see registry.go's doc comment on these fields), so any one of them
		// being non-nil already implies the rest are too.
		return ctx.WriteMemory != nil
	case "chat_search":
		// Gated on SearchThreads rather than a dedicated bool, same
		// reasoning as memory_store above: every wiring site sets all
		// three search_chats closures together (see gateway/turn.go,
		// cmd/search.go) or none at all (cmd/benchmark.go).
		return ctx.SearchThreads != nil
	case "stars_library":
		// Gated on StarsSearch rather than a dedicated bool, same
		// reasoning as memory_store/chat_search above: StarsSearch and
		// StarsRead are always wired together or not at all (see
		// gateway/turn.go) — nil for a ghost turn (issue #67), same "no
		// persisted-store reads leaking into an incognito session"
		// reasoning as memory/search_chats.
		return ctx.StarsSearch != nil
	case "docker_only":
		// code_exec requires a real container boundary for arbitrary
		// code — ctx.CodeExecEnabled is a pure capability check (is the
		// sandbox actually configured: HostWorkspaceDir/SignalDir), not a
		// deployment-mode check, so this is offered from a bare-metal dev
		// instance too, as long as the sandbox's own dependencies (Docker
		// daemon + compose/watcher/codeexec.sh) are present — see
		// gateway/turn.go's CodeExecEnabled comment and DEVELOPMENT.md's
		// "Local dev code_exec" section.
		return ctx.CodeExecEnabled
	case "deep_research":
		// Both conditions checked, not just one: DeepResearch alone
		// doesn't imply the closure was ever wired (a config/call path
		// that forgot to), and the closure being wired alone doesn't mean
		// this turn is actually in Deep Research mode (e.g. Tier 1's
		// Researcher focus mode, which must stay single-agent — see
		// docs/plans/deep-research-two-tier.md).
		return ctx.DeepResearch && ctx.SpawnResearchers != nil
	case "pulsar_wizard":
		// The ephemeral "help me write the prompt" interview only — see
		// registry.go's PulsarWizard doc comment. Never offered on a
		// normal chat/pulse turn, regardless of NoResearch/DisabledTools.
		return ctx.PulsarWizard
	case "pulsar_daily_items":
		// A Pulsar Daily list-block generation only — see registry.go's
		// PulsarDailyItems doc comment. Never offered on a normal
		// chat/pulse turn or any other Daily block kind.
		return ctx.PulsarDailyItems
	case "weaver_run":
		// A Weaver shooting-star run only — see registry.go's WeaverRun
		// doc comment. Never offered on a normal chat/pulse turn.
		return ctx.WeaverRun
	default:
		log.Warn("tool description declares an unrecognized requires value, excluding tool until fixed",
			"tool", e.Name, "requires", e.Requires)
		return false
	}
}

// catalogDefaults is the fallback-of-the-fallback if tools/descriptions/
// is missing or a given tool's file can't be loaded even once — mirrors
// prompts/prompts.go's defaults/buildDefaults() double-fallback, so a
// fully broken descriptions directory degrades to the old hardcoded text
// rather than an empty tool list.
var catalogDefaults = map[string]catalogEntry{
	"think": {Name: "think", Description: "reason privately about strategy before acting.",
		APIDescription: "Reason privately about what to do next before acting."},
	"calculator": {Name: "calculator", Description: "evaluate an arithmetic expression exactly instead of computing it silently in free-text generation.",
		APIDescription: "Evaluate an arithmetic expression exactly and return the result. Use this whenever an answer " +
			"involves a computed number (a ratio, a percentage, a sum, a date/time delta) instead of doing the " +
			"arithmetic yourself in free text — LLMs are unreliable at mental math, and this tool removes that " +
			"failure class entirely."},
	"current_time": {Name: "current_time", Description: "check the exact current time of day.",
		APIDescription: "Get the exact current local time (hour, minute, second, timezone and UTC offset). Your system " +
			"prompt only tells you today's date, not the time. Call this when the answer depends on the time right now."},
	"web_search": {Name: "web_search", Category: "research", Description: "search the web via a private SearXNG instance.",
		APIDescription: "Search the web via SearXNG for current information, facts, or sources."},
	"web_read": {Name: "web_read", Category: "research", Description: "fetch a URL and extract its content.",
		APIDescription: "Fetch a URL and extract its clean text content."},
	"nearby_search": {Name: "nearby_search", Category: "research", Description: "find real-world places near a location.",
		APIDescription: "Find real-world places near a location."},
	"youtube_transcript": {Name: "youtube_transcript", Category: "research", Description: "fetch a YouTube video's transcript.",
		APIDescription: "Fetch the transcript of a YouTube video."},
	"weather": {Name: "weather", Category: "research", Description: "current conditions and a short forecast for a location.",
		APIDescription: "Get current weather conditions and a short daily forecast for a location."},
	"reference_lookup": {Name: "reference_lookup", Category: "research", Description: "query Wikipedia or arXiv directly.",
		APIDescription: "Look up a topic directly in a specific reference source."},
	"github_repo": {Name: "github_repo", Category: "research", Description: "look up a GitHub repository's stats and README.",
		APIDescription: "Look up a GitHub repository directly via GitHub's API."},
	"github_activity": {Name: "github_activity", Category: "research", Description: "explore a GitHub repo's recent activity: releases, a specific PR, issues, or commits.",
		APIDescription: "Explore a GitHub repository's activity: recent releases, a specific pull request's detail, recent issue activity, or commits since a date."},
	"dictionary": {Name: "dictionary", Category: "research", Description: "look up a word's definition.",
		APIDescription: "Look up a word's definition, part of speech, and an example sentence."},
	"music": {Name: "music", Requires: "lastfm_api_key", Category: "research", Description: "find real song/album recommendations grounded in Last.fm's similarity data.",
		APIDescription: "Find real music recommendations grounded in actual listening/similarity data (Last.fm)."},
	"books": {Name: "books", Category: "research", Description: "find real book recommendations grounded in curated lists and shared subject data.",
		APIDescription: "Find real book recommendations grounded in readers' curated lists and shared subject/genre data."},
	"movies": {Name: "movies", Requires: "tmdb_api_key", Category: "research", Description: "find real movie/TV show recommendations grounded in TMDB's audience-recommendation data.",
		APIDescription: "Find real movie/TV show recommendations grounded in TMDB's actual audience-recommendation data."},
	"code_exec": {Name: "code_exec", Requires: "docker_only", Category: "compute",
		Description: "run Python in a locked-down, network-less sandbox — numpy/pandas/matplotlib/scipy/scikit-learn/pillow/sympy/seaborn/pyarrow preinstalled. Docker-only.",
		APIDescription: "Run Python code in a sandboxed environment for calculations, data analysis, or file processing. " +
			"numpy, pandas, matplotlib, scipy, scikit-learn, pillow, sympy, seaborn, and pyarrow are preinstalled — no " +
			"other packages can be installed, and there is no network access from inside the sandbox at all. Files " +
			"written to the current directory persist across calls within this conversation. Returns stdout, stderr, " +
			"and the exit code. A resource or time limit hit is reported back as a normal result, not a crash — " +
			"simplify the code or reduce the data size and try again."},
	"fetch_url": {Name: "fetch_url", Requires: "docker_only", Category: "compute",
		Description: "download a URL you've already been shown into your code_exec workspace so code_exec can process it.",
		APIDescription: "Fetch a URL you've already been shown as a citation this conversation, or an image_search result " +
			"by card_index, and save it into your workspace under the filename you choose — so code_exec can load a real " +
			"image, CSV, JSON, Parquet, or SQLite file instead of only synthesizing data from scratch."},
	"image_search": {Name: "image_search", Category: "research", Description: "find real photos for a query.",
		APIDescription: "Find real photos for a query and attach them as a gallery."},
	"view_image": {Name: "view_image", Description: "actually look at a specific image from a prior image_search result.",
		APIDescription: "View a specific image from a prior image_search result by its numbered position (card_index). " +
			"mode: \"describe\" (default) returns a thorough text description. mode: \"see\" (only offered to a " +
			"multimodal model) inserts the actual image as your next message so you can genuinely look at it."},
	"show": {Name: "show", Requires: "docker_only", Description: "display a workspace artifact (e.g. a code_exec chart) large and inline, right where you produced it.",
		APIDescription: "Display an image already in your workspace (e.g. a code_exec-generated chart) large and inline " +
			"in the conversation, right at this point in your reply, with an optional caption. Purely a display action — " +
			"it doesn't let you see the image yourself; use view_image for that."},
	"highlight": {Name: "highlight", Category: "research",
		Description: "turn a handful of items you actually found this turn into cards instead of a paragraph.",
		APIDescription: "Render 1-5 items you actually found this turn as cards instead of describing them in prose " +
			"— a title, a url, and optionally a short free-text price/badge and an image. Every item must come " +
			"from a web_search result or a page you actually read this turn, never from memory."},
	"ask_user_question": {Name: "ask_user_question", Requires: "interactive_chat",
		Description:    "ask the user a single focused clarifying question when a genuinely necessary detail is missing.",
		APIDescription: "Ask the user a single focused clarifying question when a genuinely necessary detail is missing — this ends the turn."},
	"memory": {Name: "memory", Requires: "memory_store",
		Description:    "write, edit, view, or forget durable memories about the user or ongoing work.",
		APIDescription: "Write, edit, view, or forget durable memories about the user or ongoing work, carried across threads."},
	"search_chats": {Name: "search_chats", Requires: "chat_search",
		Description: "search or list your own past conversations, and read one back in full.",
		APIDescription: "Search your own past conversations by keyword, or list your most recent ones when no " +
			"query is given, and read one back in full (filtered to what you ask for, or raw)."},
	"stars": {Name: "stars", Requires: "stars_library",
		Description:    "search the person's Constellation library — facts Weaver has synthesized about them — and read one back in full.",
		APIDescription: "Search the person's Constellation library (facts Weaver has synthesized about them across past conversations) and read one star back in full by ID."},
	"spawn_researchers": {Name: "spawn_researchers", Requires: "deep_research", Category: "research",
		Description:    "fan out to multiple parallel research sub-agents for a genuinely broad Deep Research question.",
		APIDescription: "Fan out to multiple independent research sub-agents running in parallel, each investigating one focused angle, then report back their findings for you to synthesize."},
	"finalize_pulsar_prompt": {Name: "finalize_pulsar_prompt", Requires: "pulsar_wizard",
		Description:    "end the interview and hand back the drafted Pulsar routine prompt.",
		APIDescription: "End the interview and hand back the drafted, ready-to-schedule Pulsar routine prompt — this ends the turn."},
	"finalize_daily_items": {Name: "finalize_daily_items", Requires: "pulsar_daily_items",
		Description:    "end with a structured list of distinct stories instead of one merged paragraph.",
		APIDescription: "End with every distinct story found as its own item (title, summary, source) instead of one merged paragraph — this ends the turn."},
	"search_stars": {Name: "search_stars", Requires: "weaver_run",
		Description:    "keyword-search Constellation's existing stars, for dedup-checking and link-discovery.",
		APIDescription: "Keyword search over Constellation's existing stars by title/summary. Always call this before create_star to check whether this topic already has a star."},
	"read_star": {Name: "read_star", Requires: "weaver_run",
		Description:    "read one star's full card.",
		APIDescription: "Read one star's full card (title, category, tags, status, confidence, summary, body). Mandatory before update_star or link_stars."},
	"create_star": {Name: "create_star", Requires: "weaver_run",
		Description:    "write a new star.",
		APIDescription: "Write a new star capturing something genuinely learned about the person themselves in this thread — a taste, identity fact, circumstance, or habit, not a standalone topic explainer — and not already covered by an existing star."},
	"update_star": {Name: "update_star", Requires: "weaver_run",
		Description:    "merge new content into an existing star.",
		APIDescription: "Merge new content into an existing personal star — rewrite so it reads as one coherent, current entry, never append. Leave title blank unless this update changes what the star is fundamentally about or contradicts something the current title states."},
	"link_stars": {Name: "link_stars", Requires: "weaver_run",
		Description:    "connect two related-but-distinct stars.",
		APIDescription: "Record that two distinct stars relate to each other, with a specific reason why."},
	"compare_sources": {Name: "compare_sources", Requires: "jev", Category: "research",
		Description:    "check whether two or more of your own cited sources actually agree on a specific fact.",
		APIDescription: "Check whether two or more sources you've already read this turn (via web_read) actually agree on a specific fact, using a calibrated comparison rather than your own read of them. Use this when you notice sources might conflict on something specific — not as a routine double-check of everything."},
}

var (
	catalogMu       sync.Mutex
	catalogCache    = map[string]catalogEntry{}
	catalogModTimes = map[string]time.Time{}
)

// loadCatalog returns the current tools/descriptions/*.yaml content,
// re-reading only files whose mtime changed since the last call — same
// hot-reload contract as prompts.Get() for prompts.yaml, just applied
// per-file since this is 12 files instead of 1. On any load error for a
// given tool (missing/unreadable/unparseable file, or a name: mismatch
// against the filename), keeps serving the last-known-good entry for that
// tool, falling back to catalogDefaults if none has ever loaded.
func loadCatalog() map[string]catalogEntry {
	catalogMu.Lock()
	defer catalogMu.Unlock()

	for _, name := range catalogOrder {
		path := catalogDescriptionsDir + "/" + name + ".yaml"
		info, err := os.Stat(path)
		if err != nil {
			if _, ok := catalogCache[name]; !ok {
				log.Warn("tool description file missing, using built-in default", "tool", name, "err", err)
				catalogCache[name] = catalogDefaults[name]
			}
			continue
		}
		if mt, ok := catalogModTimes[name]; ok && mt.Equal(info.ModTime()) {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn("reading tool description file failed, using last-known description", "tool", name, "err", err)
			if _, ok := catalogCache[name]; !ok {
				catalogCache[name] = catalogDefaults[name]
			}
			continue
		}

		var entry catalogEntry
		if err := yaml.Unmarshal(data, &entry); err != nil {
			log.Warn("parsing tool description file failed, using last-known description", "tool", name, "err", err)
			if _, ok := catalogCache[name]; !ok {
				catalogCache[name] = catalogDefaults[name]
			}
			continue
		}
		if entry.Name != name {
			log.Warn("tool description file's name doesn't match its filename, using last-known description",
				"tool", name, "declared_name", entry.Name)
			if _, ok := catalogCache[name]; !ok {
				catalogCache[name] = catalogDefaults[name]
			}
			continue
		}

		catalogCache[name] = entry
		catalogModTimes[name] = info.ModTime()
	}

	return catalogCache
}

// nonToggleable are tools the settings panel never offers a per-tool
// switch for — think and ask_user_question are reasoning/interaction
// primitives the model needs regardless of research preferences, not user
// preferences themselves, and memory already has its own dedicated
// settings section (see gateway/memories.go) rather than a plain on/off
// switch.
var nonToggleable = map[string]bool{
	"think": true, "ask_user_question": true, "memory": true,
	"finalize_pulsar_prompt": true, "finalize_daily_items": true,
}

// ToolInfo is one individually toggleable tool's identity, for the
// settings panel — see ToggleableTools.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToggleableTools lists every tool a user can individually enable/disable
// from the settings panel, in catalogOrder, with its current
// human-readable description — gateway/settings.go's handleGetSettings
// surfaces this so the frontend doesn't hardcode tool names/descriptions
// that otherwise only live in tools/descriptions/*.yaml.
//
// "weaver_run" (search_stars/read_star/create_star/update_star/
// link_stars) is excluded here even though it's not in nonToggleable
// above, for the same underlying reason: nonToggleable is for tools the
// model always needs regardless of user preference (reasoning/
// interaction primitives), while this is excluded because the toggle
// itself would be structurally inert, not because the tool is mandatory.
// These are never offered to the main assistant in any configuration;
// only a Weaver shooting-star run's own restricted tool menu ever
// reaches them (see offered()'s WeaverRun exclusion clause above).
func ToggleableTools() []ToolInfo {
	catalog := loadCatalog()
	out := make([]ToolInfo, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		if nonToggleable[name] {
			continue
		}
		if catalog[name].Requires == "weaver_run" {
			continue
		}
		out = append(out, ToolInfo{Name: name, Description: catalog[name].Description})
	}
	return out
}

// ToolsPrompt renders the {tools} placeholder's replacement text: one
// "- name: description" line per tool currently offered to ctx (same
// gating as Defs()), in catalogOrder, joined by newlines — the single
// source every one of prompt.md / prompts.yaml's fallback_system_prompt /
// buildDefaults()'s Go literal renders through, so all three stay
// textually identical wherever their prose says {tools}.
func ToolsPrompt(ctx *Context) string {
	catalog := loadCatalog()
	var sb strings.Builder
	first := true
	for _, name := range catalogOrder {
		entry := catalog[name]
		if !entry.offered(ctx) {
			continue
		}
		if !first {
			sb.WriteString("\n")
		}
		first = false
		fmt.Fprintf(&sb, "- %s: %s", name, entry.Description)
	}
	return sb.String()
}
