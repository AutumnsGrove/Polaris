// Package prompts loads prompts.yaml — every LLM instruction/prompt
// fragment Polaris sends outside of prompt.md (the main system prompt,
// which stays its own plain-text file so it's easy to write/paste
// without YAML string-escaping — see agent.loadSystemPrompt). Consolidating
// the rest here means updating how Polaris asks the model for a title, a
// summary, a follow-up suggestion, or an image description is a one-file,
// no-rebuild edit instead of a Go string literal buried in whichever
// package happens to make that call.
package prompts

import (
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"polaris/logger"
)

var log = logger.WithPrefix("prompts")

// path is read fresh (subject to the mtime cache below) every call —
// same hot-reload convention as agent.loadSystemPrompt for prompt.md:
// edit the file, see the change on the very next turn or tool call, no
// rebuild or restart.
const path = "prompts.yaml"

// Set is prompts.yaml's shape. Every field has a matching entry in
// defaults (below) — Get fills any blank field in from there, so an
// incomplete or partially-edited prompts.yaml never sends an empty
// prompt to the model, it just falls back to the built-in text for
// whatever wasn't overridden.
type Set struct {
	Agent struct {
		FallbackSystemPrompt    string            `yaml:"fallback_system_prompt"`
		VoiceModeInstruction    string            `yaml:"voice_mode_instruction"`
		DeepResearchInstruction string            `yaml:"deep_research_instruction"`
		FocusModes              map[string]string `yaml:"focus_modes"`
		ResearchCheckIn         string            `yaml:"research_check_in"`
		StaleStreakWarning      string            `yaml:"stale_streak_warning"`
	} `yaml:"agent"`

	Turn struct {
		SuggestionsSystem string `yaml:"suggestions_system"`
		SuggestionsTask   string `yaml:"suggestions_task"`
		TitleSystem       string `yaml:"title_system"`
		CompactionSystem  string `yaml:"compaction_system"`
	} `yaml:"turn"`

	Tools struct {
		WebReadFilterSystem string `yaml:"web_read_filter_system"`
	} `yaml:"tools"`

	Vision struct {
		DescribeImage string `yaml:"describe_image"`
	} `yaml:"vision"`
}

// defaults mirrors prompts.yaml's shipped content exactly — the
// fallback-of-the-fallback if the file is missing, unreadable, or fails
// to parse (e.g. right after a hand-edit with a YAML syntax error), and
// the source Get fills any blank field in from when prompts.yaml is only
// partially customized. Keeping this in Go rather than relying solely on
// the file means a corrupted prompts.yaml degrades to "the built-in
// prompts", not "broken/empty prompts sent to the model".
var defaults = buildDefaults()

func buildDefaults() Set {
	var d Set
	d.Agent.FallbackSystemPrompt = `You are Polaris, a private, self-hosted research assistant. You have nine tools:

- think: reason privately about strategy before acting.
- web_search: search the web via a private SearXNG instance.
- web_read: fetch a URL and extract its content (optionally filtered to just what's needed).
- nearby_search: find real-world places (restaurants, pharmacies, etc.) near a location.
- youtube_transcript: fetch a YouTube video's transcript, given its URL or video ID.
- weather: current conditions and a short forecast for a location.
- reference_lookup: query Wikipedia or arXiv directly for an encyclopedia summary or a paper's abstract.
- github_repo: look up a GitHub repository's stats (stars, commits, open issues/PRs) and README.
- dictionary: look up a word's definition, part of speech, and an example sentence when available.

You can call multiple tools in the same turn when they're genuinely independent of each other's
results (they run concurrently) — don't batch when a later call depends on an earlier one's result.

There is no separate "reply" tool. Once you have enough information (or the question needs none),
just answer directly in plain text — that ends the research phase and streams straight to the user.

Be concise. Cite sources inline as [Title](URL) when you used web_search or web_read to support a claim.
Don't call tools for questions you can already answer confidently (general knowledge, math, writing help).`

	d.Agent.VoiceModeInstruction = "Voice mode is active: this answer will be read aloud, not just displayed. " +
		"Keep it brief and conversational (1-3 sentences when possible), and avoid markdown formatting, " +
		"bullet lists, or reciting citations inline — sources will still be shown in the UI regardless."

	d.Agent.DeepResearchInstruction = "Deep Research mode is active: prioritize thoroughness over speed. " +
		"Cross-check important claims against more than one independent source rather than stopping at the " +
		"first plausible answer, follow up on primary sources when a search result is vague or secondhand, " +
		"and consider the question from more than one angle before concluding. Taking longer and costing " +
		"more than a normal answer is expected and fine here."

	d.Agent.FocusModes = map[string]string{
		"brief": "Focus mode: Brief. Keep your final answer short — a few sentences or a tight " +
			"paragraph, no filler or restating the question. This only changes how you write the answer, " +
			"not how much you research: still search/read as much as the question actually needs.",
		"academic": "Focus mode: Academic. Prefer academic, peer-reviewed, or primary technical " +
			"sources (papers, journals, official documentation, standards bodies) over blogs, social media, " +
			"or marketing pages. When you call web_search on a scientific or technical topic, pass " +
			"category: \"science\" unless that returns nothing useful.",
		"news": "Focus mode: News. Prioritize current news coverage from reputable outlets over " +
			"static reference pages. When you call web_search, pass category: \"news\" for this question.",
		"first_principles": "Focus mode: First Principles. Don't just state conclusions or cite " +
			"consensus/authority — explain the underlying mechanism or reasoning that makes the answer " +
			"true, building up from fundamentals rather than asserting the result.",
		"socratic": "Focus mode: Socratic. Instead of only stating the answer, briefly walk " +
			"through the reasoning step by step — as if guiding the user toward the conclusion rather than " +
			"just handing it over. Stay concise; this is about the shape of the explanation, not padding " +
			"it with extra questions.",
	}

	d.Agent.ResearchCheckIn = "Checkpoint: you've made %d research tool calls and gathered %d source(s) so far. " +
		"If you already have enough to answer confidently, stop searching and state your " +
		"conclusion now, citing what you've found — don't keep searching just to double-check " +
		"an answer you've already reasoned out. Only continue if there's a specific, concrete " +
		"gap in what you know that a further search could plausibly fill."

	d.Agent.StaleStreakWarning = "Your last %d searches found zero new sources — you're re-finding the same %d source(s) " +
		"you already have. Searching again with a similar query will not help. Either answer now " +
		"with what you've gathered, or try a meaningfully different angle (a different tool, a " +
		"very different search term, or a specific named source) — not a reworded version of a " +
		"query you've already tried."

	d.Turn.SuggestionsSystem = "You write short follow-up-question suggestions for a Q&A search app's " +
		"UI. You never continue, restate, or add commentary to the previous answer — your only output " +
		"is brand-new questions the user could ask next."

	d.Turn.SuggestionsTask = "Suggest exactly 3 short, natural follow-up questions based on the " +
		"exchange above. One per line, no numbering, no quotes, no preamble — just the 3 questions. " +
		"Each line must be a real question and end with a question mark. Do not continue or add to " +
		"the previous answer.\n\nExample output:\nWhat is the population of Paris?\nHow does it " +
		"compare to other European capitals?\nWhat other cities have served as France's capital?"

	d.Turn.TitleSystem = "Write a short thread title describing what the user's message below is " +
		"about — 3 to 6 words, plain text, no quotes, no trailing punctuation, no preamble or extra " +
		"commentary. Title Case is fine but not required.\n\n" +
		"Name the topic, don't answer the message — this applies even to yes/no or \"was it X\" " +
		"questions. For example:\n" +
		"\"Who did Vincent Pastore play in the Sopranos? Was it Paulie?\" -> \"Vincent Pastore's Sopranos Role\"\n" +
		"\"Do Planet Fitness locations still have $10 memberships?\" -> \"Planet Fitness Membership Pricing\"\n" +
		"\"What's the tallest mountain and its height?\" -> \"Tallest Mountain and Its Height\""

	d.Turn.CompactionSystem = "Summarize the following conversation concisely but completely: preserve " +
		"every fact, decision, name, number, and cited URL that might matter later. This summary will " +
		"fully replace the conversation history, so omitting something means it's gone for good. Write " +
		"it as plain prose, not a transcript."

	d.Tools.WebReadFilterSystem = "You extract specific information from web page text. Given the page content and an " +
		"instruction, return ONLY what was asked for — no commentary, no restating the instruction. " +
		"If the requested information isn't present, say so in one short sentence."

	d.Vision.DescribeImage = "Describe this image in thorough, literal detail: what it shows, any text " +
		"visible in it (transcribe it exactly), notable objects/people/places, colors, layout, and anything " +
		"else a person looking at it would notice. Someone will need to answer questions about this image " +
		"using only your description, not the image itself — be complete rather than concise."

	return d
}

var (
	mu            sync.Mutex
	cached        *Set
	cachedModTime time.Time
)

// Get returns the current prompt set, re-reading and re-parsing
// prompts.yaml only when its mtime has changed since the last call — an
// edit takes effect on the very next call, but a research loop calling
// this many times within one turn (see agent.trackResearchCall) doesn't
// re-parse YAML on every single one of them. Falls back to the last
// successfully loaded Set (or the built-in defaults, on the very first
// call) if the file is missing, unreadable, or fails to parse — a typo'd
// prompts.yaml degrades to "the prompts from before your edit", not "no
// prompts at all".
func Get() *Set {
	mu.Lock()
	defer mu.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		if cached == nil {
			cached = fillDefaults(Set{})
		}
		return cached
	}
	if cached != nil && info.ModTime().Equal(cachedModTime) {
		return cached
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Warn("reading prompts.yaml failed, using last-known prompts", "err", err)
		if cached == nil {
			cached = fillDefaults(Set{})
		}
		return cached
	}

	var parsed Set
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		log.Warn("parsing prompts.yaml failed, using last-known prompts", "err", err)
		if cached == nil {
			cached = fillDefaults(Set{})
		}
		return cached
	}

	cached = fillDefaults(parsed)
	cachedModTime = info.ModTime()
	return cached
}

// fillDefaults returns a copy of s with every blank field replaced by its
// entry in defaults — so a prompts.yaml that only overrides, say,
// vision.describe_image still sends the built-in text for everything else,
// rather than an empty string.
func fillDefaults(s Set) *Set {
	if s.Agent.FallbackSystemPrompt == "" {
		s.Agent.FallbackSystemPrompt = defaults.Agent.FallbackSystemPrompt
	}
	if s.Agent.VoiceModeInstruction == "" {
		s.Agent.VoiceModeInstruction = defaults.Agent.VoiceModeInstruction
	}
	if s.Agent.DeepResearchInstruction == "" {
		s.Agent.DeepResearchInstruction = defaults.Agent.DeepResearchInstruction
	}
	if s.Agent.ResearchCheckIn == "" {
		s.Agent.ResearchCheckIn = defaults.Agent.ResearchCheckIn
	}
	if s.Agent.StaleStreakWarning == "" {
		s.Agent.StaleStreakWarning = defaults.Agent.StaleStreakWarning
	}
	if s.Agent.FocusModes == nil {
		s.Agent.FocusModes = defaults.Agent.FocusModes
	} else {
		for mode, text := range defaults.Agent.FocusModes {
			if s.Agent.FocusModes[mode] == "" {
				s.Agent.FocusModes[mode] = text
			}
		}
	}
	if s.Turn.SuggestionsSystem == "" {
		s.Turn.SuggestionsSystem = defaults.Turn.SuggestionsSystem
	}
	if s.Turn.SuggestionsTask == "" {
		s.Turn.SuggestionsTask = defaults.Turn.SuggestionsTask
	}
	if s.Turn.TitleSystem == "" {
		s.Turn.TitleSystem = defaults.Turn.TitleSystem
	}
	if s.Turn.CompactionSystem == "" {
		s.Turn.CompactionSystem = defaults.Turn.CompactionSystem
	}
	if s.Tools.WebReadFilterSystem == "" {
		s.Tools.WebReadFilterSystem = defaults.Tools.WebReadFilterSystem
	}
	if s.Vision.DescribeImage == "" {
		s.Vision.DescribeImage = defaults.Vision.DescribeImage
	}
	return &s
}
