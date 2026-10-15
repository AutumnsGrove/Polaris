package oraclebench

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polaris/gateway"
	"polaris/prompts"
)

// Expected values in these tests are worked out by hand from the default
// weights (alpha 0.6; high_stakes fp 3.0 / fn 2.0, credit = fn = 2.0), not
// copied from the scorer's output.

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func mustWeights(t *testing.T) Weights {
	t.Helper()
	w, err := DefaultWeights()
	if err != nil {
		t.Fatalf("DefaultWeights: %v", err)
	}
	return w
}

// outcome is a check outcome that delivered a nudge.
func nudged(key, winner string, probs map[string]float64) gateway.CheckOutcome {
	return gateway.CheckOutcome{Key: key, Winner: winner, Probabilities: probs, Fired: true, Nudge: "nudge text"}
}

// quietWin is a winner clearing its bar with nothing to inject — what
// high_stakes=none looks like. Fired is true here, which is exactly why the
// scorer can't treat Fired as "nudge delivered".
func quietWin(key, winner string, probs map[string]float64) gateway.CheckOutcome {
	return gateway.CheckOutcome{Key: key, Winner: winner, Probabilities: probs, Fired: true}
}

func sample(latency time.Duration, checks ...gateway.CheckOutcome) Sample {
	return Sample{Latency: latency, Result: gateway.OracleResult{Checks: checks}}
}

func oneCase(id string, expect map[string]Expectation, quiet ...string) Case {
	return Case{ID: id, Track: "high_stakes", Rationale: "r", State: State{Latest: "x"}, Expect: expect, Quiet: quiet}
}

func score(t *testing.T, c Case, budget time.Duration, samples ...Sample) CaseScore {
	t.Helper()
	return ScoreCase(mustWeights(t), prompts.Get(), CaseRun{Case: c, Samples: samples}, budget)
}

func TestScoreCase_CorrectNudgeEarnsFullCreditShape(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	cs := score(t, c, 0, sample(time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.9, "none": 0.1})))

	ck := cs.Checks[0]
	near(t, "verdict", ck.Verdict, 1)
	near(t, "closeness", ck.Closeness, 0.9)
	// 2.0 * (0.6*1 + 0.4*0.9)
	near(t, "earned", ck.Earned, 1.92)
	// (0.1-0)^2 + (0.9-1)^2
	near(t, "brier", ck.Brier, 0.02)
	near(t, "case score", cs.Score, 1.92)
	near(t, "case max", cs.Max, 2)
	if ck.FN || ck.FP {
		t.Errorf("a delivered, wanted nudge is neither FN nor FP: %+v", ck)
	}
}

func TestScoreCase_FalseNudgeOnQuietLabelIsPenalised(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "none"}})
	cs := score(t, c, 0, sample(time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.9, "none": 0.1})))

	ck := cs.Checks[0]
	near(t, "verdict", ck.Verdict, 0)
	near(t, "closeness", ck.Closeness, 0.1)
	near(t, "earned", ck.Earned, 2*0.4*0.1)
	if !ck.FP {
		t.Fatalf("a medical nudge where none was labeled must be a false positive: %+v", ck)
	}
	near(t, "penalty", ck.Penalty, 3)
	near(t, "case score", cs.Score, 0.08-3)
}

// gateway sets Fired for a quiet winner too; that must not read as a nudge.
func TestScoreCase_QuietWinnerClearingItsBarIsNotANudge(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "none"}})
	cs := score(t, c, 0, sample(time.Second, quietWin("high_stakes", "none", map[string]float64{"none": 0.95, "medical": 0.05})))
	if ck := cs.Checks[0]; ck.FP || ck.Nudged || ck.Penalty != 0 {
		t.Errorf("none at 0.95 delivers nothing, got %+v", ck)
	}
}

func TestScoreCase_RightAnswerBelowTheBarIsAMissedNudge(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	below := gateway.CheckOutcome{Key: "high_stakes", Winner: "medical", Probabilities: map[string]float64{"medical": 0.7}}
	cs := score(t, c, 0, sample(time.Second, below))

	ck := cs.Checks[0]
	near(t, "verdict", ck.Verdict, 1)
	// 2.0 * (0.6 + 0.4*0.7)
	near(t, "earned", ck.Earned, 1.76)
	if !ck.FN {
		t.Fatalf("the model was right but under the production bar, so no nudge reached the user: %+v", ck)
	}
	near(t, "case score", cs.Score, 1.76-2)
}

func TestScoreCase_QuietListNudgeIsAFalsePositive(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "none"}}, "emotional")
	cs := score(t, c, 0, sample(time.Second,
		quietWin("high_stakes", "none", map[string]float64{"none": 0.95}),
		nudged("emotional", "yes", map[string]float64{"yes": 0.9})))

	var quiet CheckScore
	for _, ck := range cs.Checks {
		if ck.Key == "emotional" {
			quiet = ck
		}
	}
	if !quiet.Quiet || !quiet.FP {
		t.Fatalf("emotional=yes on a quiet-listed check is a false positive: %+v", quiet)
	}
	near(t, "penalty", quiet.Penalty, 3)
	// A quiet-listed check earns no credit and adds nothing to Max.
	near(t, "max", cs.Max, 2)
}

func TestScoreCase_SuppressedCheckIsNotBlamedOnTheModel(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"format": {Answer: "table"}})
	held := gateway.CheckOutcome{Key: "format", Winner: "table", Probabilities: map[string]float64{"table": 0.9}, Suppressed: true}
	cs := score(t, c, 0, sample(time.Second, held))
	if ck := cs.Checks[0]; ck.FN || ck.Penalty != 0 || ck.Verdict != 1 {
		t.Errorf("a sibling held this nudge back; raw answer still scores, trigger must not: %+v", ck)
	}
}

func TestScoreCase_NoValidAnswerIsAMiss(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	cs := score(t, c, 0, sample(time.Second)) // call succeeded, no answer for the check
	ck := cs.Checks[0]
	if ck.Answered || ck.Verdict != 0 || !ck.FN {
		t.Errorf("an unanswered wanted nudge is a miss: %+v", ck)
	}
}

func TestScoreCase_MixedAcceptSetLeavesTriggerUnscored(t *testing.T) {
	// none doesn't nudge, medical does: neither FN nor FP is defensible.
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "none", Accept: []string{"none", "medical"}}})
	for _, s := range []Sample{
		sample(time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.9})),
		sample(time.Second, quietWin("high_stakes", "none", map[string]float64{"none": 0.9})),
	} {
		ck := score(t, c, 0, s).Checks[0]
		if ck.FN || ck.FP || ck.Verdict != 1 || ck.Expect != "unknown" {
			t.Errorf("either answer is acceptable and neither trigger is penalised: %+v", ck)
		}
	}
}

func TestScoreCase_ChipsScoredThroughChipOutcomes(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"chip_pulsar": {Answer: "yes"}}, "chip_daily")
	res := gateway.OracleResult{ChipOutcomes: []gateway.CheckOutcome{
		{Key: "pulsar", Winner: "yes", Probabilities: map[string]float64{"yes": 0.7}, Fired: false}, // under its bar
		{Key: "daily", Winner: "yes", Probabilities: map[string]float64{"yes": 0.9}, Fired: true},
	}}
	cs := score(t, c, 0, Sample{Result: res, Latency: time.Second})

	byKey := map[string]CheckScore{}
	for _, ck := range cs.Checks {
		byKey[ck.Key] = ck
	}
	if !byKey["chip_pulsar"].FN {
		t.Errorf("pulsar chip wanted but never offered: %+v", byKey["chip_pulsar"])
	}
	if !byKey["chip_daily"].FP {
		t.Errorf("daily chip offered where it should be quiet: %+v", byKey["chip_daily"])
	}
	near(t, "penalty (0.5 fn + 0.5 fp)", cs.Penalty, 1.0)
}

func TestScoreCase_RepeatsAverageProbabilitiesAndVoteOnTheNudge(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	mk := func(p float64, fired bool) Sample {
		o := gateway.CheckOutcome{Key: "high_stakes", Winner: "medical", Probabilities: map[string]float64{"medical": p}, Fired: fired}
		if fired {
			o.Nudge = "n"
		}
		return sample(time.Second, o)
	}
	// 0.6 / 0.7 / 0.8 average to 0.7; the nudge landed in 2 of 3 repeats.
	cs := score(t, c, 0, mk(0.6, false), mk(0.7, true), mk(0.8, true))
	ck := cs.Checks[0]
	near(t, "closeness", ck.Closeness, 0.7)
	near(t, "nudge rate", ck.NudgeRate, 2.0/3.0)
	if !ck.Nudged || ck.FN {
		t.Errorf("2 of 3 repeats nudged, so the check counts as delivered: %+v", ck)
	}
}

func TestScoreCase_LateCallIsScoredAsOracleOff(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	s := sample(6*time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.9, "none": 0.1}))

	unbounded := score(t, c, 0, s)
	budgeted := score(t, c, 5*time.Second, s)
	near(t, "unbounded", unbounded.Score, 1.92)
	// Oracle-off: no credit, and the wanted nudge never arrived (fn 2.0).
	near(t, "within budget", budgeted.Score, -2)
	if budgeted.Errored {
		t.Error("a late call is Oracle-off, not an errored case")
	}
}

func TestScoreCase_ErroredCaseEarnsNothingAndIsNotPenalised(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "medical"}}, "emotional")
	cs := score(t, c, 0, Sample{Err: ErrRefusal, ErrText: "refused to answer"})
	if !cs.Errored {
		t.Fatal("every repeat failed, the case is errored")
	}
	near(t, "score", cs.Score, 0)
	near(t, "max", cs.Max, 2) // still counted in the denominator
	if cs.Penalty != 0 {
		t.Errorf("no verdict was produced, nothing to penalise: %+v", cs)
	}
}

func TestFocusVerdict(t *testing.T) {
	sticky := []string{"safari"}
	res := func(mode string, cleared bool) gateway.OracleResult {
		return gateway.OracleResult{FocusMode: mode, FocusCleared: cleared}
	}
	tests := []struct {
		name       string
		acceptable []string
		prior      string
		res        gateway.OracleResult
		want       string
	}{
		{"pick made", []string{"brief"}, "", res("brief", false), "ok"},
		{"pick missed", []string{"brief"}, "", res("", false), "fn"},
		{"wrong pick", []string{"brief"}, "", res("shopper", false), "fp"},
		{"off held", []string{"off"}, "", res("", false), "ok"},
		{"off but picked", []string{"off"}, "", res("brief", false), "fp"},
		{"accept includes off, nothing picked", []string{"brief", "off"}, "", res("", false), "ok"},
		// The sticky rule: the raw answer is scored elsewhere; the outcome is "no change".
		{"sticky safari held", []string{"brief"}, "safari", res("", false), "ok"},
		{"sticky safari cleared", []string{"brief"}, "safari", res("", true), "fp"},
		{"sticky safari left", []string{"brief"}, "safari", res("brief", false), "fp"},
		{"clear a stale mode", []string{"off"}, "shopper", res("", true), "ok"},
		{"stale mode left in place", []string{"off"}, "shopper", res("", false), "fn"},
		{"switch made", []string{"brief"}, "shopper", res("brief", false), "ok"},
		{"switch missed", []string{"brief"}, "shopper", res("", false), "fn"},
		{"label is the current mode, kept", []string{"shopper"}, "shopper", res("", false), "ok"},
		{"label is the current mode, dropped", []string{"shopper"}, "shopper", res("", true), "fp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := focusVerdict(tc.acceptable, tc.prior, sticky, tc.res); got != tc.want {
				t.Errorf("focusVerdict = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestScoreCase_FocusActionIsScoredBesideTheRawAnswer(t *testing.T) {
	c := oneCase("a", map[string]Expectation{"focus": {Answer: "brief"}})
	c.State.Previous, c.State.PriorOracleFocus = "earlier", "safari"
	// Raw answer is brief (right), production held safari (also right).
	s := Sample{Latency: time.Second, Result: gateway.OracleResult{Checks: []gateway.CheckOutcome{
		{Key: "focus", Winner: "brief", Probabilities: map[string]float64{"brief": 0.9}, Fired: false},
	}}}
	cs := score(t, c, 0, s)
	if cs.Focus == nil || cs.Focus.Verdict != "ok" {
		t.Fatalf("sticky safari + no change is the right focus action: %+v", cs.Focus)
	}
	near(t, "verdict", cs.Checks[0].Verdict, 1)
	// focus check: 1.0 credit * (0.6 + 0.4*0.9) = 0.96, plus the 1.0 action credit.
	near(t, "score", cs.Score, 1.96)
	near(t, "max", cs.Max, 2.0)
}

func TestSummarize_RollsUpHeadlineLatencyAndFilters(t *testing.T) {
	w := mustWeights(t)
	p := prompts.Get()
	good := CaseRun{Case: oneCase("good", map[string]Expectation{"high_stakes": {Answer: "medical"}}),
		Samples: []Sample{sample(1*time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 1}))}}
	bad := CaseRun{Case: oneCase("bad", map[string]Expectation{"high_stakes": {Answer: "none"}}),
		Samples: []Sample{sample(3*time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 1}))}}
	failed := CaseRun{Case: oneCase("failed", map[string]Expectation{"high_stakes": {Answer: "medical"}}),
		Samples: []Sample{{Err: ErrRefusal}}}
	disputedCase := oneCase("disputed", map[string]Expectation{"high_stakes": {Answer: "none"}})
	disputedCase.DisputedLabel = true
	disp := CaseRun{Case: disputedCase,
		Samples: []Sample{sample(2*time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 1}))}}

	ms := Summarize(w, p, []CaseRun{good, bad, failed, disp})

	if ms.Cases != 3 || ms.Disputed != 1 {
		t.Fatalf("cases/disputed = %d/%d, want 3/1", ms.Cases, ms.Disputed)
	}
	// good: 2.0 earned. bad: earns 0 (p(none)=0), -3 fp. failed: 0. Max = 2+2+2.
	near(t, "headline", ms.Headline, 100*(2.0-3.0)/6.0)
	// The disputed case would have scored -3/2 on its own and must not touch the headline.
	near(t, "disputed headline", ms.DisputedHL, 100*-3.0/2.0)
	near(t, "verdict", ms.Verdict, 1.0/3.0)
	// Trigger decisions: good (right), bad (wrong); failed is excluded.
	near(t, "trigger", ms.Trigger, 0.5)
	if ms.DangerousOpps != 1 || ms.DangerousFP != 1 || ms.DangerousFPRate != 1 {
		t.Errorf("dangerous fp = %d/%d (rate %v), want 1/1", ms.DangerousFP, ms.DangerousOpps, ms.DangerousFPRate)
	}
	near(t, "error rate", ms.ErrorRate, 1.0/4.0)
	near(t, "refusal rate", ms.RefusalRate, 1.0/4.0)
	if ms.LatencyP50 != 2*time.Second || ms.LatencyMax != 3*time.Second {
		t.Errorf("latency p50/max = %v/%v, want 2s/3s", ms.LatencyP50, ms.LatencyMax)
	}
	if len(ms.Disqualified) != 2 {
		t.Errorf("a 25%% error rate and a 100%% dangerous-FP rate should both disqualify, got %v", ms.Disqualified)
	}
	if ms.ByTrack["high_stakes"].Cases != 3 || ms.ByTrack["high_stakes"].Errored != 1 {
		t.Errorf("track rollup = %+v", ms.ByTrack["high_stakes"])
	}
	if ms.HeadlineLo > ms.Headline || ms.HeadlineHi < ms.Headline {
		t.Errorf("headline %v outside its own bootstrap interval [%v, %v]", ms.Headline, ms.HeadlineLo, ms.HeadlineHi)
	}
}

func TestSummarize_WithinBudgetScoreDropsWhenCallsRunLate(t *testing.T) {
	w := mustWeights(t)
	slow := CaseRun{Case: oneCase("slow", map[string]Expectation{"high_stakes": {Answer: "medical"}}),
		Samples: []Sample{sample(9*time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 1}))}}
	ms := Summarize(w, prompts.Get(), []CaseRun{slow})
	near(t, "headline", ms.Headline, 100)
	near(t, "within-budget headline", ms.Budgeted, 100*-2.0/2.0)
	near(t, "within budget share", ms.WithinBudget, 0)
}

func TestBootstrapCI_IsDeterministic(t *testing.T) {
	var cases []CaseScore
	for i := 0; i < 50; i++ {
		cases = append(cases, CaseScore{Score: float64(i % 3), Max: 2})
	}
	lo1, hi1 := bootstrapCI(cases, 500, 7)
	lo2, hi2 := bootstrapCI(cases, 500, 7)
	if lo1 != lo2 || hi1 != hi2 || lo1 >= hi1 {
		t.Errorf("interval must be reproducible and non-degenerate: [%v,%v] vs [%v,%v]", lo1, hi1, lo2, hi2)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		err  error
		want ErrKind
	}{
		{nil, ErrNone},
		{context.DeadlineExceeded, ErrTimeout},
		{errors.New("calling jev: Post: context deadline exceeded"), ErrTimeout},
		{errors.New(`jev error (status 502): OpenAI refused to answer question "focus"`), ErrRefusal},
		{errors.New("jev error (status 429): rate limited"), ErrOther},
	}
	for _, tc := range tests {
		if got := ClassifyError(tc.err); got != tc.want {
			t.Errorf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestDefaultWeights(t *testing.T) {
	w := mustWeights(t)
	if w.Hash == "" || w.LatencyBudget != 5*time.Second {
		t.Errorf("default weights: hash %q budget %v, want a hash and the 5s budget", w.Hash, w.LatencyBudget)
	}
	if got := w.For("high_stakes"); got.FP != 3 || got.FN != 2 {
		t.Errorf("high_stakes = %+v, want fp 3 fn 2 (the plan's table)", got)
	}
	if w.For("chip_pulsar") != w.Checks["chips"] || w.For("chip_field") != w.Checks["chips"] {
		t.Error("every chip_* key shares the chips entry")
	}
	if w.CreditFor("high_stakes") != 2 {
		t.Errorf("credit defaults to fn, got %v", w.CreditFor("high_stakes"))
	}
	if !w.IsDangerous("emotional") || w.IsDangerous("format") {
		t.Error("dangerous checks wrong")
	}
	// Every check in the real prompts has an explicit entry, so none silently
	// falls through to the default.
	for key := range prompts.Get().Oracle.Checks {
		if _, ok := w.Checks[key]; !ok {
			t.Errorf("weights.yaml has no entry for check %q", key)
		}
	}
}

func TestLoadWeights_RejectsBadFiles(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unknown field", "alphaa: 0.5\n", "alphaa"},
		{"alpha out of range", "alpha: 1.5\nlatency_budget: 5s\n", "alpha"},
		{"no budget", "alpha: 0.5\n", "latency_budget"},
		{"unknown check", "alpha: 0.5\nlatency_budget: 5s\nchecks:\n  vibes: {fp: 1, fn: 1}\n", "no such check"},
		{"negative weight", "alpha: 0.5\nlatency_budget: 5s\nchecks:\n  intent: {fp: -1, fn: 1}\n", "negative"},
		{"unknown dangerous check", "alpha: 0.5\nlatency_budget: 5s\ndangerous_checks: [vibes]\n", "dangerous_checks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWeights(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	if _, err := LoadWeights(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing weights file must be an error, not a silent default")
	}
}

func TestLoadWeights_OverrideChangesHashAndValues(t *testing.T) {
	body := "alpha: 0.5\nlatency_budget: 3s\ndefault: {fp: 1, fn: 1}\nchecks:\n  high_stakes: {fp: 9, fn: 4, credit: 7}\n"
	path := filepath.Join(t.TempDir(), "w.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := LoadWeights(path)
	if err != nil {
		t.Fatal(err)
	}
	def := mustWeights(t)
	if w.Hash == def.Hash {
		t.Error("different weights must hash differently: the tracking DB relies on it")
	}
	if w.CreditFor("high_stakes") != 7 || w.LatencyBudget != 3*time.Second {
		t.Errorf("override not applied: credit %v budget %v", w.CreditFor("high_stakes"), w.LatencyBudget)
	}
	// The same case under heavier weights scores differently — retuning works.
	c := oneCase("a", map[string]Expectation{"high_stakes": {Answer: "none"}})
	s := sample(time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.9, "none": 0.1}))
	got := ScoreCase(w, prompts.Get(), CaseRun{Case: c, Samples: []Sample{s}}, 0)
	near(t, "penalty under overridden weights", got.Penalty, 9)
}
