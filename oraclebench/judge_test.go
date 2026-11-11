package oraclebench

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"polaris/llm"
	"polaris/prompts"
)

type stubChat struct {
	content string
	cost    float64
	err     error
	got     []llm.ChatMessage
}

func (s *stubChat) ChatCompletionStreaming(_ context.Context, msgs []llm.ChatMessage, _ func(string), _ func(string)) (*llm.ChatResponse, error) {
	s.got = msgs
	if s.err != nil {
		return nil, s.err
	}
	return &llm.ChatResponse{Content: s.content, CostUSD: s.cost}, nil
}

type stubJudge struct {
	byKey map[string]Judgment
	err   error
	calls []string
}

func (s *stubJudge) Judge(_ context.Context, req JudgeRequest) (Judgment, error) {
	s.calls = append(s.calls, req.Key)
	if s.err != nil {
		return Judgment{}, s.err
	}
	return s.byKey[req.Key], nil
}

func TestParseJudgment(t *testing.T) {
	tests := []struct {
		name, in string
		want     Judgment
		wantErr  bool
	}{
		{"plain", `{"acceptable": true, "quality": 0.8, "note": "fine"}`, Judgment{Acceptable: true, Quality: 0.8, Note: "fine"}, false},
		{"code fence", "```json\n{\"acceptable\": true, \"quality\": 1}\n```", Judgment{Acceptable: true, Quality: 1}, false},
		{"prose around it", "Sure! Here you go: {\"acceptable\": false, \"quality\": 0.1} Hope that helps.", Judgment{Quality: 0.1}, false},
		{"quality clamped", `{"acceptable": true, "quality": 7}`, Judgment{Acceptable: true, Quality: 1}, false},
		{"negative clamped", `{"acceptable": true, "quality": -3}`, Judgment{Acceptable: true, Quality: 0}, false},
		// An answer the judge calls unacceptable can't also be high quality.
		{"unacceptable caps quality", `{"acceptable": false, "quality": 0.9}`, Judgment{Quality: 0.25}, false},
		{"model better flag", `{"acceptable": true, "quality": 1, "model_better": true}`, Judgment{Acceptable: true, Quality: 1, ModelBetter: true}, false},
		{"no json", "I think it is fine.", Judgment{}, true},
		{"broken json", `{"acceptable": tru`, Judgment{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJudgment(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestJudgePrompt_CarriesEverythingTheJudgeNeeds(t *testing.T) {
	p := prompts.Get()
	c := Case{
		ID: "x", Track: "high_stakes", DisputedLabel: true,
		Rationale: "Medical detail inside a fiction frame.",
		State:     State{Previous: "earlier question", Latest: "is 400mg ibuprofen with wine realistic for my novel"},
	}
	got := judgePrompt(p, JudgeRequest{
		Case: c, Key: "high_stakes", Acceptable: []string{"none", "medical"},
		Winner: "medical", Probs: map[string]float64{"medical": 0.8, "none": 0.2},
	})
	for _, want := range []string{
		"Previous message: earlier question", "Latest message: is 400mg",
		"Health, symptoms, medications", // an option description from prompts.yaml
		"Labeled answer: none or medical", "Medical detail inside a fiction frame.",
		"disputed", "Classifier answered: medical", "medical=0.80, none=0.20",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("judge prompt is missing %q:\n%s", want, got)
		}
	}
}

func TestJudgePrompt_FieldChipUsesTheFixtureFields(t *testing.T) {
	got := judgePrompt(prompts.Get(), JudgeRequest{
		Case: Case{State: State{Latest: "my starter smells off"}, Rationale: "r"},
		Key:  "chip_field", Acceptable: []string{"Cooking"}, Winner: "Cooking", Probs: map[string]float64{"Cooking": 1},
	})
	if !strings.Contains(got, "Recipes, techniques, meal planning") || !strings.Contains(got, "- none:") {
		t.Errorf("field chip prompt should list the fixture fields and none:\n%s", got)
	}
}

func TestLLMJudge_ParsesReplyAndReportsCost(t *testing.T) {
	chat := &stubChat{content: "```json\n{\"acceptable\": true, \"quality\": 0.6}\n```", cost: 0.0001}
	j := LLMJudge{Client: chat, P: prompts.Get()}
	got, err := j.Judge(context.Background(), JudgeRequest{
		Case: Case{State: State{Latest: "m"}, Rationale: "r"}, Key: "premise", Acceptable: []string{"yes"}, Winner: "no",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Acceptable || got.Quality != 0.6 || got.CostUSD != 0.0001 {
		t.Errorf("got %+v", got)
	}
	if len(chat.got) != 2 || chat.got[0].Role != "system" || chat.got[1].Role != "user" {
		t.Errorf("want a system + user message, got %+v", chat.got)
	}

	chat.err = errors.New("boom")
	if _, err := j.Judge(context.Background(), JudgeRequest{Case: Case{}, Key: "premise"}); err == nil {
		t.Error("a chat failure must surface")
	}
}

func scoredNearMiss(t *testing.T) (CaseRun, CaseScore) {
	t.Helper()
	// Labeled medical; model said none at 0.55 with medical at 0.40: a miss
	// whose acceptable answer still drew real mass.
	c := oneCase("near", map[string]Expectation{"high_stakes": {Answer: "medical"}})
	cr := CaseRun{Case: c, Samples: []Sample{sample(time.Second,
		quietWin("high_stakes", "none", map[string]float64{"none": 0.55, "medical": 0.40}))}}
	return cr, ScoreCase(mustWeights(t), prompts.Get(), cr, 0)
}

func TestChoose(t *testing.T) {
	sel := JudgeSelection{NearMissFloor: 0.25, AuditRate: 0, Seed: 1}

	_, near := scoredNearMiss(t)
	if j, r := sel.Choose("m", near, near.Checks[0]); !j || !r {
		t.Errorf("a defensible near-miss is residual: judge=%v residual=%v", j, r)
	}

	farMiss := CheckScore{Key: "high_stakes", Answered: true, Verdict: 0, Closeness: 0.05}
	if j, _ := sel.Choose("m", CaseScore{ID: "x"}, farMiss); j {
		t.Error("a clear miss (acceptable answers drew almost no mass) needs no judge")
	}

	right := CheckScore{Key: "high_stakes", Answered: true, Verdict: 1, Closeness: 0.9}
	if j, _ := sel.Choose("m", CaseScore{ID: "x"}, right); j {
		t.Error("a clear hit is only judged when it lands in the audit slice")
	}

	if j, r := sel.Choose("m", CaseScore{ID: "x", Disputed: true}, right); !j || !r {
		t.Error("every labeled check on a disputed case goes to the judge")
	}
	if j, _ := sel.Choose("m", CaseScore{ID: "x", Disputed: true}, CheckScore{Key: "emotional", Quiet: true, Answered: true}); j {
		t.Error("a quiet-list entry has no answer to grade")
	}
	if j, _ := sel.Choose("m", CaseScore{ID: "x", Disputed: true}, CheckScore{Key: "format"}); j {
		t.Error("an unanswered check has nothing to grade")
	}
}

func TestAuditSliceIsStableAndRoughlyTheRequestedShare(t *testing.T) {
	sel := JudgeSelection{AuditRate: 0.10, Seed: 1}
	hits := 0
	for i := 0; i < 5000; i++ {
		id := fmt.Sprintf("case-%d", i)
		a := sel.audited("modelA", id, "premise")
		if a != sel.audited("modelA", id, "premise") {
			t.Fatal("the audit choice must be deterministic")
		}
		if a {
			hits++
		}
	}
	if share := float64(hits) / 5000; share < 0.08 || share > 0.12 {
		t.Errorf("audit share = %.3f, want about 0.10", share)
	}
	if (JudgeSelection{AuditRate: 0}).audited("m", "c", "k") {
		t.Error("rate 0 audits nothing")
	}
}

func TestJudgeCase_OnlyJudgesSelectedChecksAndKeepsGoingOnFailure(t *testing.T) {
	w, p := mustWeights(t), prompts.Get()
	c := oneCase("two", map[string]Expectation{"high_stakes": {Answer: "medical"}, "premise": {Answer: "yes"}})
	cr := CaseRun{Case: c, Samples: []Sample{sample(time.Second,
		quietWin("high_stakes", "none", map[string]float64{"none": 0.55, "medical": 0.40}), // near-miss
		nudged("premise", "yes", map[string]float64{"yes": 0.95}),                          // clear hit
	)}}
	sel := JudgeSelection{NearMissFloor: 0.25, AuditRate: 0, Seed: 1}

	stub := &stubJudge{byKey: map[string]Judgment{"high_stakes": {Acceptable: true, Quality: 0.5}}}
	got, errs := JudgeCase(context.Background(), stub, sel, w, p, "m", cr)
	if len(errs) != 0 || len(stub.calls) != 1 || stub.calls[0] != "high_stakes" {
		t.Fatalf("only the near-miss should be judged; calls=%v errs=%v", stub.calls, errs)
	}
	if j := got["high_stakes"]; !j.Residual || j.Quality != 0.5 {
		t.Errorf("judgment = %+v, want a residual one", j)
	}

	failing := &stubJudge{err: errors.New("429")}
	got, errs = JudgeCase(context.Background(), failing, sel, w, p, "m", cr)
	if len(got) != 0 || len(errs) != 1 {
		t.Errorf("a judge failure drops that judgment and reports it, got %v / %v", got, errs)
	}
}

func TestScoreCase_ResidualJudgmentReplacesTheVerdictCredit(t *testing.T) {
	cr, _ := scoredNearMiss(t)
	// Without a judge: 2.0 * (0.6*0 + 0.4*0.40) = 0.32.
	near(t, "deterministic earned", ScoreCase(mustWeights(t), prompts.Get(), cr, 0).Checks[0].Earned, 0.32)

	cr.Judgments = map[string]Judgment{"high_stakes": {Acceptable: true, Quality: 0.5, Residual: true}}
	ck := ScoreCase(mustWeights(t), prompts.Get(), cr, 0).Checks[0]
	// The judge calls the answer defensible: credit * quality = 2.0 * 0.5.
	near(t, "judged earned", ck.Earned, 1.0)
	if ck.JudgeQuality == nil || *ck.JudgeQuality != 0.5 {
		t.Errorf("JudgeQuality = %v", ck.JudgeQuality)
	}
	// Layer 2 is untouched by the judge: the user still got no medical nudge.
	if !ck.FN {
		t.Error("the judge rescues the raw credit, not the missed nudge")
	}
}

func TestScoreCase_AuditJudgmentDoesNotChangeTheScore(t *testing.T) {
	cr, _ := scoredNearMiss(t)
	before := ScoreCase(mustWeights(t), prompts.Get(), cr, 0).Score
	cr.Judgments = map[string]Judgment{"high_stakes": {Acceptable: true, Quality: 1, Residual: false}}
	after := ScoreCase(mustWeights(t), prompts.Get(), cr, 0)
	near(t, "score", after.Score, before)
	if after.Checks[0].JudgeQuality == nil {
		t.Error("the audit read should still be recorded on the check")
	}
}

func TestSummarize_CountsAuditDisagreementsAndFlagsCasesForReview(t *testing.T) {
	w, p := mustWeights(t), prompts.Get()
	hit := func(id string, j Judgment) CaseRun {
		return CaseRun{
			Case:      oneCase(id, map[string]Expectation{"high_stakes": {Answer: "medical"}}),
			Samples:   []Sample{sample(time.Second, nudged("high_stakes", "medical", map[string]float64{"medical": 0.95}))},
			Judgments: map[string]Judgment{"high_stakes": j},
		}
	}
	runs := []CaseRun{
		hit("agree", Judgment{Acceptable: true, Quality: 1, CostUSD: 0.0001}),
		hit("disagree", Judgment{Acceptable: false, Quality: 0.1, CostUSD: 0.0002}), // judge thinks the label is wrong
		hit("better", Judgment{Acceptable: true, Quality: 1, ModelBetter: true}),
	}
	ms := Summarize(w, p, runs)
	if ms.JudgeAudited != 3 || ms.JudgeDisagreed != 1 {
		t.Errorf("audited/disagreed = %d/%d, want 3/1", ms.JudgeAudited, ms.JudgeDisagreed)
	}
	near(t, "judge cost", ms.JudgeCostUSD, 0.0003)
	if len(ms.ReviewCases) != 2 {
		t.Errorf("review cases = %v, want the disagreement and the model-better case", ms.ReviewCases)
	}
}
