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
	"think", "calculator", "web_search", "web_read", "nearby_search", "youtube_transcript",
	"weather", "reference_lookup", "github_repo", "dictionary", "music", "books", "movies", "visualize",
	"image_search", "read_attachment", "ask_user_question", "memory", "spawn_researchers", "finalize_pulsar_prompt",
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
	switch e.Requires {
	case "":
		return true
	case "lastfm_api_key":
		return ctx.LastFMAPIKey != ""
	case "tmdb_api_key":
		return ctx.TMDBAPIKey != ""
	case "attachment":
		return len(ctx.AttachmentData) > 0
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
	"dictionary": {Name: "dictionary", Category: "research", Description: "look up a word's definition.",
		APIDescription: "Look up a word's definition, part of speech, and an example sentence."},
	"music": {Name: "music", Requires: "lastfm_api_key", Category: "research", Description: "find real song/album recommendations grounded in Last.fm's similarity data.",
		APIDescription: "Find real music recommendations grounded in actual listening/similarity data (Last.fm)."},
	"books": {Name: "books", Category: "research", Description: "find real book recommendations grounded in curated lists and shared subject data.",
		APIDescription: "Find real book recommendations grounded in readers' curated lists and shared subject/genre data."},
	"movies": {Name: "movies", Requires: "tmdb_api_key", Category: "research", Description: "find real movie/TV show recommendations grounded in TMDB's audience-recommendation data.",
		APIDescription: "Find real movie/TV show recommendations grounded in TMDB's actual audience-recommendation data."},
	"visualize": {Name: "visualize", Description: "render structured data you've already synthesized as a chart instead of prose.",
		APIDescription: "Render data you've synthesized as a chart (line, bar, timeline, or meter) instead of prose or a table."},
	"image_search": {Name: "image_search", Category: "research", Description: "find real photos for a query.",
		APIDescription: "Find real photos for a query and attach them as a gallery."},
	"read_attachment": {Name: "read_attachment", Requires: "attachment", Description: "page through or search this turn's attached PDF.",
		APIDescription: "Page through or search the PDF the user attached to this turn, beyond the short preview already given."},
	"ask_user_question": {Name: "ask_user_question", Requires: "interactive_chat",
		Description:    "ask the user a single focused clarifying question when a genuinely necessary detail is missing.",
		APIDescription: "Ask the user a single focused clarifying question when a genuinely necessary detail is missing — this ends the turn."},
	"memory": {Name: "memory", Requires: "memory_store",
		Description:    "write, edit, view, or forget durable memories about the user or ongoing work.",
		APIDescription: "Write, edit, view, or forget durable memories about the user or ongoing work, carried across threads."},
	"spawn_researchers": {Name: "spawn_researchers", Requires: "deep_research", Category: "research",
		Description:    "fan out to multiple parallel research sub-agents for a genuinely broad Deep Research question.",
		APIDescription: "Fan out to multiple independent research sub-agents running in parallel, each investigating one focused angle, then report back their findings for you to synthesize."},
	"finalize_pulsar_prompt": {Name: "finalize_pulsar_prompt", Requires: "pulsar_wizard",
		Description:    "end the interview and hand back the drafted Pulsar routine prompt.",
		APIDescription: "End the interview and hand back the drafted, ready-to-schedule Pulsar routine prompt — this ends the turn."},
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
var nonToggleable = map[string]bool{"think": true, "ask_user_question": true, "memory": true, "finalize_pulsar_prompt": true}

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
func ToggleableTools() []ToolInfo {
	catalog := loadCatalog()
	out := make([]ToolInfo, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		if nonToggleable[name] {
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
