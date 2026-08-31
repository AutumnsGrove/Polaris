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
	"think", "web_search", "web_read", "nearby_search", "youtube_transcript",
	"weather", "reference_lookup", "github_repo", "dictionary", "music", "books", "movies", "read_attachment",
	"ask_user_question", "memory",
}

// catalogDescriptionsDir is where each tool's YAML file lives — read fresh
// (subject to the per-file mtime cache below) so it's hot-editable, same
// convention as prompt.md/prompts.yaml (see prompts/prompts.go's doc
// comment on Get).
const catalogDescriptionsDir = "tools/descriptions"

// catalogEntry is one tools/descriptions/*.yaml file, parsed.
type catalogEntry struct {
	Name           string `yaml:"name"`
	Requires       string `yaml:"requires"`
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
	"web_search": {Name: "web_search", Description: "search the web via a private SearXNG instance.",
		APIDescription: "Search the web via SearXNG for current information, facts, or sources."},
	"web_read": {Name: "web_read", Description: "fetch a URL and extract its content.",
		APIDescription: "Fetch a URL and extract its clean text content."},
	"nearby_search": {Name: "nearby_search", Description: "find real-world places near a location.",
		APIDescription: "Find real-world places near a location."},
	"youtube_transcript": {Name: "youtube_transcript", Description: "fetch a YouTube video's transcript.",
		APIDescription: "Fetch the transcript of a YouTube video."},
	"weather": {Name: "weather", Description: "current conditions and a short forecast for a location.",
		APIDescription: "Get current weather conditions and a short daily forecast for a location."},
	"reference_lookup": {Name: "reference_lookup", Description: "query Wikipedia or arXiv directly.",
		APIDescription: "Look up a topic directly in a specific reference source."},
	"github_repo": {Name: "github_repo", Description: "look up a GitHub repository's stats and README.",
		APIDescription: "Look up a GitHub repository directly via GitHub's API."},
	"dictionary": {Name: "dictionary", Description: "look up a word's definition.",
		APIDescription: "Look up a word's definition, part of speech, and an example sentence."},
	"music": {Name: "music", Requires: "lastfm_api_key", Description: "find real song/album recommendations grounded in Last.fm's similarity data.",
		APIDescription: "Find real music recommendations grounded in actual listening/similarity data (Last.fm)."},
	"books": {Name: "books", Description: "find real book recommendations grounded in curated lists and shared subject data.",
		APIDescription: "Find real book recommendations grounded in readers' curated lists and shared subject/genre data."},
	"movies": {Name: "movies", Requires: "tmdb_api_key", Description: "find real movie/TV show recommendations grounded in TMDB's audience-recommendation data.",
		APIDescription: "Find real movie/TV show recommendations grounded in TMDB's actual audience-recommendation data."},
	"read_attachment": {Name: "read_attachment", Requires: "attachment", Description: "page through or search this turn's attached PDF.",
		APIDescription: "Page through or search the PDF the user attached to this turn, beyond the short preview already given."},
	"ask_user_question": {Name: "ask_user_question", Requires: "interactive_chat",
		Description:    "ask the user a single focused clarifying question when a genuinely necessary detail is missing.",
		APIDescription: "Ask the user a single focused clarifying question when a genuinely necessary detail is missing — this ends the turn."},
	"memory": {Name: "memory", Requires: "memory_store",
		Description:    "write, edit, view, or forget durable memories about the user or ongoing work.",
		APIDescription: "Write, edit, view, or forget durable memories about the user or ongoing work, carried across threads."},
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
