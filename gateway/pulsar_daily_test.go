package gateway

import (
	"context"
	"testing"
	"time"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/store"
)

func toolCallResponse(name, argsJSON string) *llm.ChatResponse {
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      name,
			Arguments: argsJSON,
		},
	}}}
}

func TestDailyDiffJudge_ParsesStructuredVerdict(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: toolCallResponse("record_verdict", `{"verdict":"notable","gist":"A big new development"}`)},
	}}

	v, _, err := dailyDiffJudge(context.Background(), mock, "Tech & Science", "yesterday's content", "today's content")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "notable" || v.Gist != "A big new development" {
		t.Errorf("dailyDiffJudge = %+v, want the parsed tool-call arguments", v)
	}
}

// TestDailyDiffJudge_PlainProseFallsBackToNormal covers the model
// answering in plain prose instead of calling the mandated tool — should
// degrade to "normal" (never drop a block outright on a formatting slip)
// rather than erroring the whole block generation out.
func TestDailyDiffJudge_PlainProseFallsBackToNormal(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "This seems like a normal update."}},
	}}

	v, _, err := dailyDiffJudge(context.Background(), mock, "Headlines", "yesterday", "today")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "normal" {
		t.Errorf("dailyDiffJudge verdict = %q, want %q for a no-tool-call reply", v.Verdict, "normal")
	}
}

func TestDailyDiffJudge_UnrecognizedVerdictFallsBackToNormal(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: toolCallResponse("record_verdict", `{"verdict":"huge","gist":"whatever"}`)},
	}}

	v, _, err := dailyDiffJudge(context.Background(), mock, "Local", "yesterday", "today")
	if err != nil {
		t.Fatalf("dailyDiffJudge: %v", err)
	}
	if v.Verdict != "normal" {
		t.Errorf("dailyDiffJudge verdict = %q, want %q for an unrecognized verdict string", v.Verdict, "normal")
	}
}

func TestDailyElectTopStory_ReturnsWinnerKey(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: toolCallResponse("elect_top_story", `{"winner_key":"tech_science"}`)},
	}}

	candidates := []dailyRankCandidate{
		{Key: "headlines", Title: "Top Headlines", Gist: "A quiet news day"},
		{Key: "tech_science", Title: "Tech & Science", Gist: "A major datacenter buildout announced"},
	}
	key, _, err := dailyElectTopStory(context.Background(), mock, candidates)
	if err != nil {
		t.Fatalf("dailyElectTopStory: %v", err)
	}
	if key != "tech_science" {
		t.Errorf("dailyElectTopStory = %q, want %q", key, "tech_science")
	}
}

// TestDailyElectTopStory_UnknownKeyFallsBackToFirstCandidate covers the
// model naming a key outside the given candidate list — should still
// elect something (the plan doc's ranking pass is only ever invoked when
// at least one real candidate exists) rather than returning an empty
// winner.
func TestDailyElectTopStory_UnknownKeyFallsBackToFirstCandidate(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: toolCallResponse("elect_top_story", `{"winner_key":"nonexistent_block"}`)},
	}}

	candidates := []dailyRankCandidate{
		{Key: "headlines", Title: "Top Headlines", Gist: "gist a"},
		{Key: "trending", Title: "Trending Now", Gist: "gist b"},
	}
	key, _, err := dailyElectTopStory(context.Background(), mock, candidates)
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
			cfg:  &store.PulsarDailyConfig{TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, loc)},
			want: true,
		},
		{
			name: "created after today's scheduled time already passed — must not fire immediately on save",
			cfg:  &store.PulsarDailyConfig{TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 5, 7, 30, 0, 0, loc)},
			want: false,
		},
		{
			name: "already generated today, not due again",
			cfg: &store.PulsarDailyConfig{TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc),
				LastGeneratedAt: timePtr(time.Date(2026, 9, 5, 7, 0, 0, 0, loc))},
			want: false,
		},
		{
			name: "last generated yesterday, due again today",
			cfg: &store.PulsarDailyConfig{TimeOfDay: "07:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc),
				LastGeneratedAt: timePtr(time.Date(2026, 9, 4, 7, 0, 0, 0, loc))},
			want: true,
		},
		{
			// TimeOfDay is 20:00 and "now" is 08:00 the same day, so the most
			// recent scheduled instant is *yesterday's* 20:00 — not due only
			// if nothing was actually missed, i.e. this config didn't exist
			// yet at that point.
			name: "scheduled time later today hasn't arrived, and nothing earlier was missed",
			cfg:  &store.PulsarDailyConfig{TimeOfDay: "20:00", CreatedAt: time.Date(2026, 9, 5, 0, 1, 0, 0, loc)},
			want: false,
		},
		{
			// Same time_of_day, but CreatedAt predates yesterday's 20:00
			// occurrence with no generation since — this is the "catch up on
			// restart" case isRoutineDue already establishes, not a bug.
			name: "scheduled time later today hasn't arrived, but yesterday's occurrence was missed",
			cfg:  &store.PulsarDailyConfig{TimeOfDay: "20:00", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc)},
			want: true,
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
