package oraclebench

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"polaris/llm"
	"polaris/prompts"
)

// The judge is the only LLM call made at scoring time, and it is deliberately
// narrow (docs/plans/oracle-benchmark.md, Layer 3). The deterministic layers
// settle almost everything; the judge reads only what they cannot:
//
//   - the residual: a disputed-label case, or a miss whose acceptable answers
//     still drew real probability mass (a defensible near-miss). Its quality
//     REPLACES that check's verdict/closeness credit.
//   - an audit slice of the rest: scored by nothing, used to measure how often
//     the label and the judge disagree, which is the bank's own error rate.
//
// It must be a non-decision chat model so it can't grade its own family
// (the default is the registry's `deepseek`).

// Judgment is the judge's read of one model answer to one check.
type Judgment struct {
	Acceptable bool    `json:"acceptable"`
	Quality    float64 `json:"quality"`
	// ModelBetter means the judge thinks the model's answer beats the label,
	// which flags the case for a human rather than silently flipping it.
	ModelBetter bool    `json:"model_better,omitempty"`
	Note        string  `json:"note,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	// Residual judgments feed the score; audit-only ones just measure label
	// quality. Set by the runner from NeedsJudge, not by the model.
	Residual bool `json:"residual"`
}

// JudgeRequest is everything the judge sees about one answer.
type JudgeRequest struct {
	Case       Case
	Key        string
	Acceptable []string
	Winner     string
	Probs      map[string]float64
}

// Judge grades one answer. The LLM implementation is below; tests use stubs.
type Judge interface {
	Judge(ctx context.Context, req JudgeRequest) (Judgment, error)
}

// chatClient is the one llm.Client method the judge needs.
type chatClient interface {
	ChatCompletionStreaming(ctx context.Context, messages []llm.ChatMessage, onChunk func(string), onReasoning func(string)) (*llm.ChatResponse, error)
}

// LLMJudge grades through a chat model.
type LLMJudge struct {
	Client chatClient
	P      *prompts.Set
}

const judgeSystem = `You are auditing an automatic classifier that reads a message sent to a web-research assistant and answers one multiple-choice question about it. You are given the message, the question and its options, the answer the benchmark labeled correct (and any other answers it accepts), why, and the answer the classifier actually gave with its probabilities.

Decide whether the classifier's answer is acceptable: would a thoughtful person, reading the question's option descriptions, call it a reasonable answer for this message? Judge the answer against the message and the option descriptions, not against how confident the classifier was.

Reply with ONLY a JSON object, no other text:
{"acceptable": true or false, "quality": a number from 0 to 1 (1 = as good as the label, 0.5 = defensible but not the best fit, 0 = wrong), "model_better": true only if the classifier's answer is clearly BETTER than the labeled one, "note": "one short sentence"}`

// judgePrompt builds the user message. Exposed to tests via the package.
func judgePrompt(p *prompts.Set, req JudgeRequest) string {
	var b strings.Builder
	c := req.Case
	if c.State.Previous != "" {
		fmt.Fprintf(&b, "Previous message: %s\n\n", c.State.Previous)
	}
	fmt.Fprintf(&b, "Latest message: %s\n\n", c.State.Latest)

	instructions, options := questionText(p, req.Key)
	fmt.Fprintf(&b, "Question (%s): %s\n\nOptions:\n", req.Key, instructions)
	names := make([]string, 0, len(options))
	for name := range options {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %s\n", name, options[name])
	}

	fmt.Fprintf(&b, "\nLabeled answer: %s\n", strings.Join(req.Acceptable, " or "))
	fmt.Fprintf(&b, "Why: %s\n", strings.TrimSpace(c.Rationale))
	if c.DisputedLabel {
		b.WriteString("(The label is disputed: reasonable people split on it.)\n")
	}
	fmt.Fprintf(&b, "\nClassifier answered: %s\nProbabilities: %s\n", req.Winner, formatProbs(req.Probs))
	return b.String()
}

// questionText returns the instructions and option descriptions the classifier
// was asked, for a check or a chip.
func questionText(p *prompts.Set, key string) (string, map[string]string) {
	if name, isChip := strings.CutPrefix(key, ChipPrefix); isChip {
		if name == "field" {
			opts := map[string]string{"none": "Doesn't clearly belong to any field."}
			for n, d := range FixtureFields {
				opts[n] = d
			}
			return p.Oracle.Chips["field"].Instructions, opts
		}
		chip := p.Oracle.Chips[name]
		return chip.Instructions, chip.Options
	}
	check := p.Oracle.Checks[key]
	return check.Instructions, check.Options
}

func formatProbs(probs map[string]float64) string {
	type kv struct {
		k string
		v float64
	}
	var all []kv
	for k, v := range probs {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	parts := make([]string, 0, len(all))
	for _, e := range all {
		parts = append(parts, fmt.Sprintf("%s=%.2f", e.k, e.v))
	}
	return strings.Join(parts, ", ")
}

// Judge implements Judge.
func (j LLMJudge) Judge(ctx context.Context, req JudgeRequest) (Judgment, error) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: judgeSystem},
		{Role: "user", Content: judgePrompt(j.P, req)},
	}
	resp, err := j.Client.ChatCompletionStreaming(ctx, msgs, func(string) {}, nil)
	if err != nil {
		return Judgment{}, fmt.Errorf("judge call: %w", err)
	}
	out, err := parseJudgment(resp.Content)
	if err != nil {
		return Judgment{}, err
	}
	out.CostUSD = resp.CostUSD
	return out, nil
}

// parseJudgment reads the judge's reply. Plain prompting plus this tolerance
// (strip a code fence, take the outermost braces) parsed 4/4 in the spike;
// response_format json_object was not an improvement and once broke a reply.
func parseJudgment(content string) (Judgment, error) {
	s := strings.TrimSpace(content)
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return Judgment{}, fmt.Errorf("judge reply has no JSON object: %q", truncate(content, 120))
	}
	var j Judgment
	if err := json.Unmarshal([]byte(s[start:end+1]), &j); err != nil {
		return Judgment{}, fmt.Errorf("judge reply is not valid JSON (%v): %q", err, truncate(content, 120))
	}
	j.Quality = min(1, max(0, j.Quality))
	// A "not acceptable" answer can't also be high quality; trust the boolean.
	if !j.Acceptable {
		j.Quality = min(j.Quality, 0.25)
	}
	return j, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// JudgeSelection says which labeled checks go to the judge and why.
type JudgeSelection struct {
	// NearMissFloor: a miss counts as defensible when the acceptable answers
	// still drew at least this much probability. The plan's suggested 0.25.
	NearMissFloor float64
	// AuditRate is the share of the remaining checks audited for label quality.
	AuditRate float64
	Seed      uint64
}

// DefaultJudgeSelection is the plan's: floor 0.25, 10% audit.
func DefaultJudgeSelection() JudgeSelection {
	return JudgeSelection{NearMissFloor: 0.25, AuditRate: 0.10, Seed: 1}
}

// Choose reports whether to judge one scored check and whether the result
// counts toward the score (residual) or only measures label quality (audit).
// Quiet-list entries are never judged: there is no answer to grade.
func (s JudgeSelection) Choose(modelName string, cs CaseScore, ck CheckScore) (judge, residual bool) {
	if ck.Quiet || !ck.Answered {
		return false, false
	}
	switch {
	case cs.Disputed:
		return true, true
	case ck.Verdict == 0 && ck.Closeness >= s.NearMissFloor:
		return true, true
	}
	return s.audited(modelName, cs.ID, ck.Key), false
}

// audited is a stable per-(model, case, check) coin flip, so a re-run audits
// the same slice and results stay comparable without storing the choice.
func (s JudgeSelection) audited(model, caseID, key string) bool {
	if s.AuditRate <= 0 {
		return false
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%s|%s|%s", s.Seed, model, caseID, key)
	return float64(h.Sum64()%10000)/10000 < s.AuditRate
}

// JudgeCase scores a case, sends the checks Choose selects to the judge, and
// returns the judgments to hand back to ScoreCase through CaseRun.Judgments. A
// judge failure drops that judgment (the check keeps its deterministic score)
// rather than failing the run; the error is returned for the caller to log.
func JudgeCase(ctx context.Context, j Judge, sel JudgeSelection, w Weights, p *prompts.Set, modelName string, cr CaseRun) (map[string]Judgment, []error) {
	base := ScoreCase(w, p, cr, 0)
	out := map[string]Judgment{}
	var errs []error
	for _, ck := range base.Checks {
		judge, residual := sel.Choose(modelName, base, ck)
		if !judge {
			continue
		}
		agg := aggregate(ck.Key, usableSamples(cr.Samples, 0))
		got, err := j.Judge(ctx, JudgeRequest{Case: cr.Case, Key: ck.Key, Acceptable: ck.Acceptable, Winner: agg.winner, Probs: agg.probs})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", cr.Case.ID, ck.Key, err))
			continue
		}
		got.Residual = residual
		out[ck.Key] = got
	}
	return out, errs
}
