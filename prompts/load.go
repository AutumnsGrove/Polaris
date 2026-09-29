package prompts

import (
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

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
	if s.Agent.NoResearchInstruction == "" {
		s.Agent.NoResearchInstruction = defaults.Agent.NoResearchInstruction
	}
	if s.Agent.DeepResearchInstruction == "" {
		s.Agent.DeepResearchInstruction = defaults.Agent.DeepResearchInstruction
	}
	if s.Agent.SubAgentTask == "" {
		s.Agent.SubAgentTask = defaults.Agent.SubAgentTask
	}
	if s.Agent.ResearchCheckIn == "" {
		s.Agent.ResearchCheckIn = defaults.Agent.ResearchCheckIn
	}
	if s.Agent.StaleStreakWarning == "" {
		s.Agent.StaleStreakWarning = defaults.Agent.StaleStreakWarning
	}
	if s.Agent.EmptyAnswerRetry == "" {
		s.Agent.EmptyAnswerRetry = defaults.Agent.EmptyAnswerRetry
	}
	if s.Agent.MultimodalTrue == "" {
		s.Agent.MultimodalTrue = defaults.Agent.MultimodalTrue
	}
	if s.Agent.MultimodalFalse == "" {
		s.Agent.MultimodalFalse = defaults.Agent.MultimodalFalse
	}
	if s.Agent.QuerySimilarityWarning == "" {
		s.Agent.QuerySimilarityWarning = defaults.Agent.QuerySimilarityWarning
	}
	if s.Agent.CodeExecThemeDark == "" {
		s.Agent.CodeExecThemeDark = defaults.Agent.CodeExecThemeDark
	}
	if s.Agent.CodeExecThemeLight == "" {
		s.Agent.CodeExecThemeLight = defaults.Agent.CodeExecThemeLight
	}
	if s.Agent.PersonNameGuidance == "" {
		s.Agent.PersonNameGuidance = defaults.Agent.PersonNameGuidance
	}
	if s.Agent.PersonPronounsGuidance == "" {
		s.Agent.PersonPronounsGuidance = defaults.Agent.PersonPronounsGuidance
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
	if s.Turn.WeaverTitleSystem == "" {
		s.Turn.WeaverTitleSystem = defaults.Turn.WeaverTitleSystem
	}
	if s.Turn.TitleRegenerateSystem == "" {
		s.Turn.TitleRegenerateSystem = defaults.Turn.TitleRegenerateSystem
	}
	if s.Turn.TitleRegenerateTask == "" {
		s.Turn.TitleRegenerateTask = defaults.Turn.TitleRegenerateTask
	}
	if s.Turn.CompactionSystem == "" {
		s.Turn.CompactionSystem = defaults.Turn.CompactionSystem
	}
	if s.Turn.CompactionTask == "" {
		s.Turn.CompactionTask = defaults.Turn.CompactionTask
	}
	// MemoryChatSystem was missing from this fallback list entirely until
	// now — a real gap: an installed prompts.yaml predating this prompt's
	// addition, or one with the key accidentally deleted, would send the
	// memory-chat model call an empty system prompt (just the raw
	// fmt.Sprintf %s memory index, no instructions at all) instead of
	// falling back to the compiled-in default like every other prompt
	// here does.
	if s.Turn.MemoryChatSystem == "" {
		s.Turn.MemoryChatSystem = defaults.Turn.MemoryChatSystem
	}
	if s.Turn.MemoryExportPrompt == "" {
		s.Turn.MemoryExportPrompt = defaults.Turn.MemoryExportPrompt
	}
	if s.Turn.MemoryImportSystem == "" {
		s.Turn.MemoryImportSystem = defaults.Turn.MemoryImportSystem
	}
	if s.Tools.WebReadFilterSystem == "" {
		s.Tools.WebReadFilterSystem = defaults.Tools.WebReadFilterSystem
	}
	if s.Tools.ThreadReadFilterSystem == "" {
		s.Tools.ThreadReadFilterSystem = defaults.Tools.ThreadReadFilterSystem
	}
	if s.Vision.DescribeImage == "" {
		s.Vision.DescribeImage = defaults.Vision.DescribeImage
	}
	if s.Weaver.System == "" {
		s.Weaver.System = defaults.Weaver.System
	}
	if s.Weaver.RevisitInstruction == "" {
		s.Weaver.RevisitInstruction = defaults.Weaver.RevisitInstruction
	}
	if s.Weaver.ReconcileSystem == "" {
		s.Weaver.ReconcileSystem = defaults.Weaver.ReconcileSystem
	}
	if s.Weaver.PersonNameGuidance == "" {
		s.Weaver.PersonNameGuidance = defaults.Weaver.PersonNameGuidance
	}
	if s.Weaver.PersonPronounsGuidance == "" {
		s.Weaver.PersonPronounsGuidance = defaults.Weaver.PersonPronounsGuidance
	}
	if s.Weaver.InteractiveSystem == "" {
		s.Weaver.InteractiveSystem = defaults.Weaver.InteractiveSystem
	}
	if s.PulsarDaily.ExpandPrefix == "" {
		s.PulsarDaily.ExpandPrefix = defaults.PulsarDaily.ExpandPrefix
	}
	if s.PulsarDaily.ResearchFollowup == "" {
		s.PulsarDaily.ResearchFollowup = defaults.PulsarDaily.ResearchFollowup
	}
	if s.PulsarDaily.CuriosityFollowup == "" {
		s.PulsarDaily.CuriosityFollowup = defaults.PulsarDaily.CuriosityFollowup
	}
	if s.PulsarDaily.MediaFollowup == "" {
		s.PulsarDaily.MediaFollowup = defaults.PulsarDaily.MediaFollowup
	}
	fillWizardDefaults(&s)
	if s.PulsarSuggest.System == "" {
		s.PulsarSuggest.System = defaults.PulsarSuggest.System
	}
	if s.PulsarSuggest.Task == "" {
		s.PulsarSuggest.Task = defaults.PulsarSuggest.Task
	}
	if s.PulsarSuggest.DailySystem == "" {
		s.PulsarSuggest.DailySystem = defaults.PulsarSuggest.DailySystem
	}
	if s.PulsarSuggest.DailyTask == "" {
		s.PulsarSuggest.DailyTask = defaults.PulsarSuggest.DailyTask
	}
	if s.PulsarDaily.PickBlockSystem == "" {
		s.PulsarDaily.PickBlockSystem = defaults.PulsarDaily.PickBlockSystem
	}
	if s.PulsarDaily.DiffJudgeSystem == "" {
		s.PulsarDaily.DiffJudgeSystem = defaults.PulsarDaily.DiffJudgeSystem
	}
	if s.PulsarDaily.TopStoryElectorSystem == "" {
		s.PulsarDaily.TopStoryElectorSystem = defaults.PulsarDaily.TopStoryElectorSystem
	}
	if s.Oracle.Section == "" {
		s.Oracle.Section = defaults.Oracle.Section
	}
	if s.Oracle.QuestionPreamble == "" {
		s.Oracle.QuestionPreamble = defaults.Oracle.QuestionPreamble
	}
	// Checks/Chips fall back whole-map, not per-key — unlike FocusModes'
	// flat map[string]string, each value here is a nested struct with its
	// own thresholds/options/injections, and a prompts.yaml edit to one
	// check is expected to redefine that check completely rather than
	// partially inherit stale fields from defaults.
	if s.Oracle.LinkNudge == "" {
		s.Oracle.LinkNudge = defaults.Oracle.LinkNudge
	}
	if s.Oracle.Checks == nil {
		s.Oracle.Checks = defaults.Oracle.Checks
	}
	if s.Oracle.Chips == nil {
		s.Oracle.Chips = defaults.Oracle.Chips
	}
	return &s
}
