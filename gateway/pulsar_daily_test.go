package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/testkit"

	"polaris/config"
	"polaris/llm"
	"polaris/models"
	"polaris/store"
)

// dailyOneShotFixture wires a scripted HTTP double as the writer/architect
// backend: the ported one-shots run through CompleteOneShot (which needs a
// real *llm.Client, never a mock), so tests script turns instead of
// responses. modelID is always a real registry id (mimo-pro), keeping the
// router on an explicit route.
func dailyOneShotFixture(t *testing.T, turns ...testkit.ServerTurn) (*testkit.Server, *config.Config, *llm.Client, string) {
	t.Helper()
	srv := testkit.NewServer(t, turns...)
	cfg, err := config.Load(writeTestConfig(t, t.TempDir(), srv.URL), models.Registry)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	modelCfg := cfg.ModelByID("mimo-pro")
	client := llm.NewClient(srv.URL, "k", modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens)
	return srv, cfg, client, modelCfg.ID
}

func toolCallTurn(name, argsJSON string) testkit.ServerTurn {
	return testkit.ServerTurn{Turn: testkit.Turn{
		ToolCalls: []testkit.Call{{ID: "call_1", Name: name, Args: argsJSON}},
		Usage:     &provider.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, CostUSD: 0.001, CostSource: "provider"},
	}}
}

func textTurn(text string) testkit.ServerTurn {
	return testkit.ServerTurn{Turn: testkit.Turn{
		Text:  text,
		Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, CostUSD: 0.001, CostSource: "provider"},
	}}
}

func TestDailyDiffJudge_ParsesStructuredVerdict(t *testing.T) {
	_, cfg, client, modelID := dailyOneShotFixture(t,
		toolCallTurn("record_verdict", `{"verdict":"notable","gist":"A big new development","reasoning":"A new fact appeared that wasn't in yesterday's version"}`))

	v, _, err := (&Server{}).dailyDiffJudge(context.Background(), cfg, client, modelID, "test-session", "Trending Now", "yesterday's content", "today's content")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "notable" || v.Gist != "A big new development" {
		t.Errorf("dailyDiffJudge = %+v, want the parsed tool-call arguments", v)
	}
	if v.Reasoning == "" {
		t.Error("dailyDiffJudge reasoning = \"\", want the model's stated reasoning captured for the trace")
	}
}

// TestDailyDiffJudge_PlainProseFallsBackToNormal covers the model
// answering in plain prose instead of calling the mandated tool — should
// degrade to "normal" (never drop a block outright on a formatting slip)
// rather than erroring the whole block generation out.
func TestDailyDiffJudge_PlainProseFallsBackToNormal(t *testing.T) {
	_, cfg, client, modelID := dailyOneShotFixture(t, textTurn("This seems like a normal update."))

	v, _, err := (&Server{}).dailyDiffJudge(context.Background(), cfg, client, modelID, "test-session", "Headlines", "yesterday", "today")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "normal" {
		t.Errorf("dailyDiffJudge verdict = %q, want %q for a no-tool-call reply", v.Verdict, "normal")
	}
}

func TestDailyDiffJudge_UnrecognizedVerdictFallsBackToNormal(t *testing.T) {
	_, cfg, client, modelID := dailyOneShotFixture(t,
		toolCallTurn("record_verdict", `{"verdict":"huge","gist":"whatever"}`))

	v, _, err := (&Server{}).dailyDiffJudge(context.Background(), cfg, client, modelID, "test-session", "Local", "yesterday", "today")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "normal" {
		t.Errorf("dailyDiffJudge verdict = %q, want %q for an unrecognized verdict string", v.Verdict, "normal")
	}
}

func TestDailyBlockLocation(t *testing.T) {
	tests := []struct {
		name            string
		blockKey        string
		location        string
		weatherLocation string
		want            string
	}{
		{"weather with no override falls back to shared location", "weather", "Beaverton, OR", "", "Beaverton, OR"},
		{"weather with an override uses it instead", "weather", "Beaverton, OR", "Seattle, WA", "Seattle, WA"},
		{"a non-weather block ignores weatherLocation entirely", "picture_of_day", "Beaverton, OR", "Seattle, WA", "Beaverton, OR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dailyBlockLocation(tt.blockKey, tt.location, tt.weatherLocation); got != tt.want {
				t.Errorf("dailyBlockLocation(%q, %q, %q) = %q, want %q", tt.blockKey, tt.location, tt.weatherLocation, got, tt.want)
			}
		})
	}
}

func TestDailyElectTopStory_ReturnsWinnerKey(t *testing.T) {
	_, cfg, client, modelID := dailyOneShotFixture(t,
		toolCallTurn("elect_top_story", `{"winner_key":"trending","reasoning":"Bigger and more consequential than a quiet news day"}`))

	candidates := []dailyRankCandidate{
		{Key: "headlines", Title: "Top Headlines", Gist: "A quiet news day"},
		{Key: "trending", Title: "Trending Now", Gist: "A major datacenter buildout announced"},
	}
	key, reasoning, _, err := (&Server{}).dailyElectTopStory(context.Background(), cfg, client, modelID, "test-session", candidates)
	if err != nil {
		t.Fatalf("dailyElectTopStory: %v", err)
	}
	if key != "trending" {
		t.Errorf("dailyElectTopStory = %q, want %q", key, "trending")
	}
	if reasoning == "" {
		t.Error("dailyElectTopStory reasoning = \"\", want the model's stated reasoning captured for the trace")
	}
}

// TestDailyElectTopStory_UnknownKeyFallsBackToFirstCandidate covers the
// model naming a key outside the given candidate list — should still
// elect something (the plan doc's ranking pass is only ever invoked when
// at least one real candidate exists) rather than returning an empty
// winner.
func TestDailyElectTopStory_UnknownKeyFallsBackToFirstCandidate(t *testing.T) {
	_, cfg, client, modelID := dailyOneShotFixture(t,
		toolCallTurn("elect_top_story", `{"winner_key":"nonexistent_block"}`))

	candidates := []dailyRankCandidate{
		{Key: "headlines", Title: "Top Headlines", Gist: "gist a"},
		{Key: "trending", Title: "Trending Now", Gist: "gist b"},
	}
	key, _, _, err := (&Server{}).dailyElectTopStory(context.Background(), cfg, client, modelID, "test-session", candidates)
	if err != nil {
		t.Fatalf("dailyElectTopStory: %v", err)
	}
	if key != "headlines" {
		t.Errorf("dailyElectTopStory = %q, want fallback to first candidate %q", key, "headlines")
	}
}

func TestIsDailyDue(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, loc) // 08:00

	tests := []struct {
		name string
		cfg  *store.PulsarDailyConfig
		want bool
	}{
		{
			name: "never generated, scheduled time already passed today",
			cfg:  &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, loc)},
			want: true,
		},
		{
			name: "created after today's scheduled time already passed — must not fire immediately on save",
			cfg:  &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 5, 7, 30, 0, 0, loc)},
			want: false,
		},
		{
			name: "already generated today, not due again",
			cfg: &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc),
				LastGeneratedAt: timePtr(time.Date(2026, 9, 5, 7, 0, 0, 0, loc))},
			want: false,
		},
		{
			name: "last generated yesterday, due again today",
			cfg: &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc),
				LastGeneratedAt: timePtr(time.Date(2026, 9, 4, 7, 0, 0, 0, loc))},
			want: true,
		},
		{
			// TimeOfDay is 20:00 and "now" is 08:00 the same day, so the most
			// recent scheduled instant is *yesterday's* 20:00 — not due only
			// if nothing was actually missed, i.e. this config didn't exist
			// yet at that point.
			name: "scheduled time later today hasn't arrived, and nothing earlier was missed",
			cfg:  &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "20:00", CreatedAt: time.Date(2026, 9, 5, 0, 1, 0, 0, loc)},
			want: false,
		},
		{
			// Same time_of_day, but CreatedAt predates yesterday's 20:00
			// occurrence with no generation since — this is the "catch up on
			// restart" case isRoutineDue already establishes, not a bug.
			name: "scheduled time later today hasn't arrived, but yesterday's occurrence was missed",
			cfg:  &store.PulsarDailyConfig{Enabled: true, TimeOfDay: "20:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc)},
			want: true,
		},
		{
			// The global on/off switch — otherwise identical to the first
			// case above (which wants true), confirming Enabled gates before
			// any time-of-day math runs at all.
			name: "disabled, even though the schedule itself is due",
			cfg:  &store.PulsarDailyConfig{Enabled: false, TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, loc)},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDailyDue(tt.cfg, now); got != tt.want {
				t.Errorf("isDailyDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDailyBlockRegistry_TopStoryNotIndependentlyToggleable(t *testing.T) {
	if _, ok := dailyBlockSpecByKey("top_story"); ok {
		t.Error(`"top_story" must not be a registry entry — it's Stage B's elevation of a Watch block, not independently toggleable content`)
	}
	if _, ok := dailyBlockSpecByKey("weather"); !ok {
		t.Error(`"weather" should be a registered block`)
	}
}

func TestAppendCustomInstruction(t *testing.T) {
	base := "Give me a short rundown of today's headlines."

	if got := appendCustomInstruction(base, ""); got != base {
		t.Errorf("appendCustomInstruction with blank instruction changed the task: %q", got)
	}
	if got := appendCustomInstruction(base, "   "); got != base {
		t.Errorf("appendCustomInstruction with whitespace-only instruction changed the task: %q", got)
	}

	got := appendCustomInstruction(base, "focus on AI and climate policy")
	want := base + " The reader specifically wants: focus on AI and climate policy."
	if got != want {
		t.Errorf("appendCustomInstruction() = %q, want %q", got, want)
	}
}

func TestAppendPickHistoryExclusion(t *testing.T) {
	base := "Pick one interesting word."

	if got := appendPickHistoryExclusion(base, nil); got != base {
		t.Errorf("appendPickHistoryExclusion with no history changed the task: %q", got)
	}
	if got := appendPickHistoryExclusion(base, []string{}); got != base {
		t.Errorf("appendPickHistoryExclusion with empty history changed the task: %q", got)
	}

	got := appendPickHistoryExclusion(base, []string{"Numinous", "Petrichor"})
	want := base + " Already used before — don't repeat any of these: Numinous | Petrichor."
	if got != want {
		t.Errorf("appendPickHistoryExclusion() = %q, want %q", got, want)
	}
}

func TestDailyBlockRegistry_FreshPickBlocksAreNotWatch(t *testing.T) {
	freshPicks := []string{"word_of_day", "weather", "on_this_day", "quote", "picture_of_day"}
	for _, key := range freshPicks {
		spec, ok := dailyBlockSpecByKey(key)
		if !ok {
			t.Errorf("block %q not found in registry", key)
			continue
		}
		if spec.Watch {
			t.Errorf("block %q is marked Watch=true, want false — fresh-pick blocks always render and skip the diff-judge", key)
		}
	}
}
