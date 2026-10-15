package oraclebench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"polaris/gateway"
	"polaris/jev"
	"polaris/prompts"
)

// stubAsker answers every question it is asked, choosing per key. It records
// concurrency and what it was sent so the runner's wiring can be asserted.
type stubAsker struct {
	model    string
	pick     func(key string, options []string) (string, float64)
	delay    time.Duration
	err      error
	calls    atomic.Int64
	inflight atomic.Int64
	peak     atomic.Int64

	mu        sync.Mutex
	questions map[string]jev.ChoiceQuestion
	states    []string
}

func (s *stubAsker) AskChoice(ctx context.Context, state interface{}, questions map[string]jev.ChoiceQuestion) (*jev.Response, error) {
	s.calls.Add(1)
	n := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	s.mu.Lock()
	s.questions = questions
	s.states = append(s.states, state.(string))
	s.mu.Unlock()

	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	answers := map[string]jev.ChoiceAnswer{}
	for key, q := range questions {
		opts := make([]string, 0, len(q.Criteria))
		for o := range q.Criteria {
			opts = append(opts, o)
		}
		sort.Strings(opts)
		choice, p := opts[0], 0.9
		if s.pick != nil {
			choice, p = s.pick(key, opts)
		}
		answers[key] = jev.ChoiceAnswer{Choice: choice, Probabilities: map[string]float64{choice: p}}
	}
	return &jev.Response{Answers: answers, Usage: jev.Usage{CostUSD: 0.0002}, Model: s.model}, nil
}

func pickMedical(key string, opts []string) (string, float64) {
	switch key {
	case "high_stakes":
		return "medical", 0.95
	case "chip_field":
		return "Cooking", 0.9
	}
	for _, quiet := range []string{"no", "none", "off", "standard", "answer", "any", "evergreen", "general"} {
		for _, o := range opts {
			if o == quiet {
				return o, 0.9
			}
		}
	}
	return opts[0], 0.9
}

func twoCases() []Case {
	return []Case{
		{ID: "dose", Track: "high_stakes", Rationale: "r", State: State{Latest: "max dose of tylenol"},
			Expect: map[string]Expectation{"high_stakes": {Answer: "medical"}}},
		{ID: "follow", Track: "intent", Rationale: "r", State: State{Previous: "weather in tokyo", Latest: "what about next week", PriorOracleFocus: "safari"},
			Expect: map[string]Expectation{"intent": {Answer: "weather"}}},
	}
}

func TestBuildInput(t *testing.T) {
	in := BuildInput(twoCases()[0])
	if !in.IsFirstMessage || in.PrevUserMessage != "" || in.ThreadSource != "web" || in.Visuals != "normal" {
		t.Errorf("first-message input = %+v", in)
	}
	if !in.OffersDeepResearch {
		t.Error("without OffersDeepResearch the deep_research check is never asked, so a costly false promotion could never be measured")
	}
	if in.Ghost || in.IsManualFocus || in.ActiveFocusMode != "" {
		t.Errorf("the bench environment is fixed: no ghost, no manual focus, no preset mode: %+v", in)
	}
	in = BuildInput(twoCases()[1])
	if in.IsFirstMessage || in.PrevUserMessage != "weather in tokyo" || in.PriorOracleFocusMode != "safari" {
		t.Errorf("follow-up input = %+v", in)
	}
	for name := range FixtureFields {
		if in.FieldIDs[name] == "" {
			t.Errorf("field %q has no id: RunOracle drops a field chip with no id, so chip_field could never offer", name)
		}
	}
	c := twoCases()[0]
	c.State.ThreadSource = "pulsar"
	if BuildInput(c).ThreadSource != "pulsar" {
		t.Error("thread_source must pass through")
	}
}

func TestRun_CollectsEveryCallThroughTheRealOraclePath(t *testing.T) {
	asker := &stubAsker{model: "typesafe/jev-1.14-20261014", pick: pickMedical, delay: 5 * time.Millisecond}
	var events, lastDone, lastTotal int
	got, err := Run(context.Background(), RunConfig{
		Cases: twoCases(), Models: []Model{{ID: "jev", Asker: asker}},
		Repeats: 3, Timeout: time.Second, Concurrency: 2,
		OnCall:     func(CallEvent) { events++ },
		OnProgress: func(done, total int) { lastDone, lastTotal = done, total },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Runs) != 2 || len(got[0].Runs[0].Samples) != 3 {
		t.Fatalf("shape = %d models, want 1 with 2 cases x 3 repeats", len(got))
	}
	// 2 cases x 3 repeats; the probe call is not part of the results.
	if events != 6 || lastDone != 6 || lastTotal != 6 {
		t.Errorf("events/progress = %d, %d/%d, want 6", events, lastDone, lastTotal)
	}
	if peak := asker.peak.Load(); peak > 2 {
		t.Errorf("peak in-flight = %d with concurrency 2: latency numbers would be measured at the wrong load", peak)
	}

	s := got[0].Runs[0].Samples[0]
	if s.Err != ErrNone || s.Latency < 5*time.Millisecond {
		t.Errorf("sample = err %q latency %v", s.Err, s.Latency)
	}
	if s.Result.ResolvedModel != "typesafe/jev-1.14-20261014" || got[0].ResolvedModels()[0] != "typesafe/jev-1.14-20261014" {
		t.Errorf("resolved model not captured: %q", s.Result.ResolvedModel)
	}
	// The real RunOracle rules ran: high_stakes=medical at 0.95 clears 0.75 and nudges.
	hs := findOutcome(s.Result, "high_stakes")
	if hs == nil || !nudgeDelivered("high_stakes", hs) {
		t.Errorf("high_stakes outcome = %+v, want a delivered nudge", hs)
	}
	// And the fixture Fields were offered with ids, so the chip can fire at all.
	if f := findOutcome(s.Result, "chip_field"); f == nil || !f.Fired || f.Winner != "Cooking" {
		t.Errorf("chip_field outcome = %+v, want Cooking offered", f)
	}
	// The follow-up case sent Jev the previous message the way production does.
	var sawPrevious bool
	for _, st := range asker.states {
		sawPrevious = sawPrevious || strings.HasPrefix(st, "Previous message: weather in tokyo\n\nLatest message: what about next week")
	}
	if !sawPrevious {
		t.Errorf("state sent for the follow-up case was never the two-message form: %q", asker.states)
	}
}

func TestRun_ModelThatRejectsChoiceQuestionsIsNotApplicableNotZero(t *testing.T) {
	respan := &stubAsker{err: errors.New(`jev error (status 400): Respan only accepts noul questions whose instructions and criteria are plain strings`)}
	ok := &stubAsker{pick: pickMedical}
	var events int
	got, err := Run(context.Background(), RunConfig{
		Cases: twoCases(), Models: []Model{{ID: "respan/span-01", Asker: respan}, {ID: "jev", Asker: ok}},
		Repeats: 2, Timeout: time.Second, Concurrency: 2, OnCall: func(CallEvent) { events++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].NA == "" || got[1].NA != "" {
		t.Errorf("NA flags = %q / %q, want only the first", got[0].NA, got[1].NA)
	}
	if respan.calls.Load() != 1 {
		t.Errorf("an incapable model should cost one probe call, made %d", respan.calls.Load())
	}
	if events != 4 {
		t.Errorf("events = %d, want 4 (the capable model's 2x2 only)", events)
	}
}

func TestRun_RefusalsAndTimeoutsAreRecordedNotRetried(t *testing.T) {
	refuse := &stubAsker{err: errors.New(`jev error (status 502): OpenAI refused to answer question "focus"`)}
	slow := &stubAsker{delay: time.Second, pick: pickMedical}
	got, err := Run(context.Background(), RunConfig{
		Cases: twoCases()[:1], Models: []Model{{ID: "luna", Asker: refuse}, {ID: "slow", Asker: slow}},
		Repeats: 1, Timeout: 30 * time.Millisecond, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if k := got[0].Runs[0].Samples[0].Err; k != ErrRefusal {
		t.Errorf("luna sample = %q, want refusal", k)
	}
	if k := got[1].Runs[0].Samples[0].Err; k != ErrTimeout {
		t.Errorf("slow sample = %q, want timeout", k)
	}
	// Probe + one real call each for the refusing model: no retry loop.
	if refuse.calls.Load() != 2 {
		t.Errorf("refusing model was called %d times, want exactly probe + 1", refuse.calls.Load())
	}
}

func TestRun_InterruptionIsAnErrorNotAScoredRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	asker := &stubAsker{pick: pickMedical, delay: 20 * time.Millisecond}
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := Run(ctx, RunConfig{Cases: twoCases(), Models: []Model{{ID: "m", Asker: asker}}, Repeats: 50, Timeout: time.Second, Concurrency: 1})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a cancellation error so half-finished results are never scored", err)
	}
}

func TestRun_NeedsCasesAndModels(t *testing.T) {
	if _, err := Run(context.Background(), RunConfig{}); err == nil {
		t.Error("an empty run is a wiring bug")
	}
}

func entry(model string, headline, budgeted float64, p50, p95 time.Duration, dq ...string) Entry {
	return Entry{Model: model, Score: ModelScore{Headline: headline, HeadlineLo: headline - 5, HeadlineHi: headline + 5,
		Budgeted: budgeted, LatencyP50: p50, LatencyP95: p95, Disqualified: dq}}
}

func TestLeaderboard_RanksEligibleFirstThenDisqualifiedThenNA(t *testing.T) {
	l := BuildLeaderboard(Leaderboard{Budget: 5 * time.Second, Entries: []Entry{
		{Model: "respan", NA: "rejects Choice questions"},
		entry("refuser", 99, 99, time.Second, 2*time.Second, "error/refusal rate above 5%"),
		entry("mid", 70, 70, time.Second, 2*time.Second),
		entry("best", 80, 80, time.Second, 2*time.Second),
		entry("slow-best", 80, 60, time.Second, 2*time.Second),
	}})
	var order []string
	for _, e := range l.Entries {
		order = append(order, e.Model)
	}
	want := []string{"best", "slow-best", "mid", "refuser", "respan"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (a 99 that refuses is still disqualified; ties go to the better within-budget score)", order, want)
	}
}

func TestLeaderboard_RenderMarksTiesAndDisqualifications(t *testing.T) {
	l := BuildLeaderboard(Leaderboard{Cases: 281, Repeats: 3, Concurrency: 2, Budget: 5 * time.Second, WeightsHash: "abc123", Entries: []Entry{
		entry("leader", 80, 80, time.Second, 2*time.Second),
		entry("tied", 78, 78, 2*time.Second, 3*time.Second),
		entry("behind", 60, 60, time.Second, 2*time.Second),
		entry("refuser", 95, 95, time.Second, 2*time.Second, "error/refusal rate above 5%"),
		{Model: "respan", NA: "rejects Choice questions"},
	}})
	var buf bytes.Buffer
	l.Render(&buf)
	out := buf.String()
	for _, want := range []string{"281 cases x 3 repeats", "weights abc123", "1 ", "2≈", "3 ", "x ", "N/A (rejects Choice questions)", "refuser: error/refusal rate above 5%"} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "3≈") {
		t.Errorf("a model 20 points behind is not a tie:\n%s", out)
	}
}

func TestLatencyChart_RanksFastestFirstAndFlagsBudgetOverruns(t *testing.T) {
	chart := LatencyChart([]Entry{
		entry("slow", 70, 70, 4*time.Second, 7*time.Second),
		entry("fast", 70, 70, 800*time.Millisecond, 1500*time.Millisecond),
		{Model: "respan", NA: "x"},
		entry("mid", 70, 70, 2*time.Second, 3*time.Second),
	}, 5*time.Second, 40)

	lines := strings.Split(strings.TrimSpace(chart), "\n")
	if len(lines) != 4 { // header + 3 models; the N/A model has no latency to chart
		t.Fatalf("chart has %d lines, want 4:\n%s", len(lines), chart)
	}
	for i, name := range []string{"fast", "mid", "slow"} {
		if !strings.Contains(lines[i+1], name) {
			t.Errorf("line %d = %q, want %s (fastest first)", i+1, lines[i+1], name)
		}
	}
	if !strings.Contains(lines[3], "over budget at p95") || strings.Contains(lines[1], "over budget") {
		t.Errorf("only the model whose p95 passes 5s is flagged:\n%s", chart)
	}
	if !strings.Contains(chart, "|") || !strings.Contains(chart, "█") || !strings.Contains(chart, "░") {
		t.Errorf("bar needs p50 fill, p95 shade, and the budget marker:\n%s", chart)
	}
	if LatencyChart(nil, time.Second, 40) != "" {
		t.Error("an empty chart renders nothing")
	}
}

func TestLeaderboard_JSONRoundTrips(t *testing.T) {
	l := BuildLeaderboard(Leaderboard{Budget: 5 * time.Second, WeightsHash: "h", Entries: []Entry{entry("a", 70, 60, time.Second, 2*time.Second)}})
	js, err := l.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Leaderboard
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	if back.Entries[0].Score.Headline != 70 || back.Budget != 5*time.Second {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestTracking_RecordsRunsAndRefusesToMergeTwoInOneSecond(t *testing.T) {
	tr, err := OpenTracking(filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	meta := RunMeta{RunAt: "2026-10-16T12:03:01Z", Label: "t", WeightsHash: "abc", Cases: 2, Repeats: 3, Concurrency: 2,
		Budget: 5 * time.Second, Seed: 7, JudgeModel: "deepseek", Models: []string{"jev"}}
	run, err := tr.StartRun(meta)
	if err != nil {
		t.Fatal(err)
	}
	if run.Table != "run_20261016T120301Z_oraclebench" {
		t.Errorf("table = %q", run.Table)
	}
	if _, err := tr.StartRun(meta); err == nil {
		t.Error("a second run in the same second must fail loudly, not merge into the first")
	}

	cr := CaseRun{Case: twoCases()[0], Samples: []Sample{
		{Latency: 2 * time.Second, Result: gateway.OracleResult{CostUSD: 0.0002}},
		{Latency: 1 * time.Second, Result: gateway.OracleResult{CostUSD: 0.0002}},
		{Err: ErrRefusal, ErrText: "refused to answer"},
	}, Judgments: map[string]Judgment{"high_stakes": {CostUSD: 0.0001}}}
	cs := ScoreCase(mustWeights(t), prompts.Get(), cr, 0)
	if err := run.RecordCase("jev", []string{"typesafe/jev-1.14-20261014"}, cr, cs); err != nil {
		t.Fatal(err)
	}
	if err := run.SaveLeaderboard([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	var model, resolved, errText, checks string
	var calls, p50 int
	var subject, judge float64
	if err := tr.db.QueryRow(`SELECT model, resolved_model, calls, latency_p50_ms, subject_cost_usd, judge_cost_usd, error, checks_json FROM "`+run.Table+`"`).
		Scan(&model, &resolved, &calls, &p50, &subject, &judge, &errText, &checks); err != nil {
		t.Fatal(err)
	}
	if model != "jev" || resolved != "typesafe/jev-1.14-20261014" || calls != 3 || p50 != 1000 {
		t.Errorf("row = %s %s calls=%d p50=%dms (p50 is over the calls that completed)", model, resolved, calls, p50)
	}
	near(t, "subject cost", subject, 0.0004)
	near(t, "judge cost", judge, 0.0001)
	if !strings.HasPrefix(errText, "refusal:") || !strings.Contains(checks, "high_stakes") {
		t.Errorf("error %q checks %q", errText, checks)
	}

	var weights, board string
	var budget int
	if err := tr.db.QueryRow(`SELECT weights_hash, budget_ms, leaderboard FROM run_index WHERE table_name = ?`, run.Table).Scan(&weights, &budget, &board); err != nil {
		t.Fatal(err)
	}
	if weights != "abc" || budget != 5000 || board != `{"ok":true}` {
		t.Errorf("run_index = %q %d %q", weights, budget, board)
	}
}
