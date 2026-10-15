package oraclebench

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"polaris/config"
	"polaris/gateway"
	"polaris/prompts"
)

// This file is the scorer: pure functions from (case, what a model did) to
// numbers, with no network and no clock, so every figure in the leaderboard can
// be reproduced by hand in a test. See docs/plans/oracle-benchmark.md.
//
// Two layers are scored for every labeled check:
//
//	Layer 1  the raw answer: verdict (winner is acceptable), closeness
//	         (probability mass on acceptable answers), calibration (Brier).
//	Layer 2  the trigger: whether a nudge was actually delivered once
//	         production's thresholds and rules were applied.
//
// "Nudge delivered" is CheckOutcome.Fired && Nudge != "" (chips: Fired), NOT
// Fired alone: gateway sets Fired for any winner clearing the bar, quiet
// options included. See the package doc in case.go.

// ErrKind classifies a failed call. Refusals and timeouts are results, not
// retried away: a model that refuses a sensitive prompt can't run Oracle.
type ErrKind string

const (
	ErrNone    ErrKind = ""
	ErrRefusal ErrKind = "refusal"
	ErrTimeout ErrKind = "timeout"
	ErrOther   ErrKind = "error"
)

// ClassifyError maps a RunOracleRaw error to an ErrKind. OpenRouter reports a
// model declining a question as `502 "<vendor> refused to answer question ..."`.
func ClassifyError(err error) ErrKind {
	switch {
	case err == nil:
		return ErrNone
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(err.Error(), "deadline exceeded"):
		return ErrTimeout
	case strings.Contains(strings.ToLower(err.Error()), "refused to answer"):
		return ErrRefusal
	default:
		return ErrOther
	}
}

// Sample is one real call of one model on one case.
type Sample struct {
	Result  gateway.OracleResult
	Latency time.Duration
	Err     ErrKind
	ErrText string
}

// CaseRun is every repeat of one case on one model.
type CaseRun struct {
	Case    Case
	Samples []Sample
}

// nudgeExpectation is what a label implies about whether a nudge should arrive.
type nudgeExpectation int

const (
	nudgeUnknown nudgeExpectation = iota // accept set mixes nudging and quiet answers
	nudgeWanted
	nudgeNotWanted
)

func (n nudgeExpectation) String() string {
	return [...]string{"unknown", "wanted", "not_wanted"}[n]
}

// expectedNudge derives the expectation from the acceptable answers: if every
// one nudges a nudge is wanted, if none does it is not, and a mixed accept set
// is left unscored on the trigger layer (only the raw layer counts).
func expectedNudge(p *prompts.Set, key string, acceptable []string) nudgeExpectation {
	nudging := 0
	for _, a := range acceptable {
		if ExpectsNudge(p, key, a) {
			nudging++
		}
	}
	switch nudging {
	case len(acceptable):
		return nudgeWanted
	case 0:
		return nudgeNotWanted
	}
	return nudgeUnknown
}

// CheckScore is the scoring of one labeled (or quiet-listed) check on one case.
type CheckScore struct {
	Key   string `json:"key"`
	Quiet bool   `json:"quiet,omitempty"` // from the case's quiet list, not an expect label

	Acceptable []string `json:"acceptable,omitempty"`
	Credit     float64  `json:"credit"`
	Answered   bool     `json:"answered"`
	Winner     string   `json:"winner,omitempty"`
	Verdict    float64  `json:"verdict"`
	Closeness  float64  `json:"closeness"`
	Brier      float64  `json:"brier,omitempty"`
	Earned     float64  `json:"earned"`
	// JudgeQuality, when set by the judge stage, replaces this check's
	// verdict/closeness credit (the residual blend; see ApplyJudge).
	JudgeQuality *float64 `json:"judge_quality,omitempty"`

	Expect     string  `json:"expect,omitempty"` // wanted / not_wanted / unknown
	NudgeRate  float64 `json:"nudge_rate"`       // share of repeats that delivered a nudge
	Nudged     bool    `json:"nudged"`           // majority of repeats
	Suppressed bool    `json:"suppressed,omitempty"`
	FN         bool    `json:"fn,omitempty"`
	FP         bool    `json:"fp,omitempty"`
	Penalty    float64 `json:"penalty,omitempty"`
}

// FocusScore is the scoring of the focus *action*: the mode production ends up
// in, which can differ from the raw answer (sticky Safari, switch thresholds).
type FocusScore struct {
	Verdict string  `json:"verdict"` // ok / fn / fp
	Credit  float64 `json:"credit"`
	Earned  float64 `json:"earned"`
	Penalty float64 `json:"penalty,omitempty"`
}

// CaseScore is one case on one model.
type CaseScore struct {
	ID       string       `json:"id"`
	Track    string       `json:"track"`
	Disputed bool         `json:"disputed,omitempty"`
	Errored  bool         `json:"errored,omitempty"`
	Checks   []CheckScore `json:"checks"`
	Focus    *FocusScore  `json:"focus,omitempty"`
	Earned   float64      `json:"earned"`
	Penalty  float64      `json:"penalty"`
	Max      float64      `json:"max"`
	Score    float64      `json:"score"` // Earned - Penalty
}

// usableSamples returns the samples that count, in order. A failed call is
// dropped. With a budget, a call slower than it becomes an empty result, which
// is what production does when Oracle times out (RunOracle returns the zero
// OracleResult), so "within budget" is the same scorer on remapped samples.
func usableSamples(samples []Sample, budget time.Duration) []Sample {
	var out []Sample
	for _, s := range samples {
		switch {
		case s.Err != ErrNone:
		case budget > 0 && s.Latency > budget:
			out = append(out, Sample{Latency: s.Latency})
		default:
			out = append(out, s)
		}
	}
	return out
}

func findOutcome(res gateway.OracleResult, key string) *gateway.CheckOutcome {
	list, name := res.Checks, key
	if chip, isChip := strings.CutPrefix(key, ChipPrefix); isChip {
		list, name = res.ChipOutcomes, chip
	}
	for i := range list {
		if list[i].Key == name {
			return &list[i]
		}
	}
	return nil
}

// nudgeDelivered is the bench's "fired": see the file comment.
func nudgeDelivered(key string, o *gateway.CheckOutcome) bool {
	if strings.HasPrefix(key, ChipPrefix) {
		return o.Fired
	}
	return o.Fired && o.Nudge != ""
}

// aggregated is one check's answer pooled across repeats: probabilities are
// averaged (the argmax is stable across repeats but the probability moves
// +-0.03-0.07, so single samples are noisy near a threshold).
type aggregated struct {
	answered   bool
	winner     string
	probs      map[string]float64
	nudgeRate  float64
	suppressed bool
}

func aggregate(key string, samples []Sample) aggregated {
	var a aggregated
	sums := map[string]float64{}
	answers, nudged, suppressed := 0, 0, 0
	for _, s := range samples {
		o := findOutcome(s.Result, key)
		if o == nil {
			continue
		}
		answers++
		for opt, p := range o.Probabilities {
			sums[opt] += p
		}
		if nudgeDelivered(key, o) {
			nudged++
		}
		if o.Suppressed {
			suppressed++
		}
	}
	if len(samples) > 0 {
		a.nudgeRate = float64(nudged) / float64(len(samples))
		a.suppressed = suppressed > 0 && float64(suppressed)/float64(len(samples)) >= 0.5
	}
	if answers == 0 {
		return a
	}
	a.answered = true
	a.probs = make(map[string]float64, len(sums))
	for opt, sum := range sums {
		a.probs[opt] = sum / float64(answers)
	}
	// Argmax with a stable tie-break so a run is reproducible.
	opts := make([]string, 0, len(a.probs))
	for opt := range a.probs {
		opts = append(opts, opt)
	}
	sort.Strings(opts)
	for _, opt := range opts {
		if a.winner == "" || a.probs[opt] > a.probs[a.winner] {
			a.winner = opt
		}
	}
	return a
}

// scoreCheck scores one check against its acceptable answers.
func scoreCheck(w Weights, p *prompts.Set, key string, acceptable []string, quiet bool, samples []Sample, errored bool) CheckScore {
	cs := CheckScore{Key: key, Quiet: quiet, Acceptable: acceptable}
	agg := aggregate(key, samples)
	cs.NudgeRate = agg.nudgeRate
	cs.Nudged = agg.nudgeRate >= 0.5 && agg.nudgeRate > 0
	cs.Suppressed = agg.suppressed

	if !quiet {
		cs.Credit = w.CreditFor(key)
		if agg.answered {
			cs.Answered = true
			cs.Winner = agg.winner
			if contains(acceptable, agg.winner) {
				cs.Verdict = 1
			}
			for _, a := range acceptable {
				cs.Closeness += agg.probs[a]
			}
			cs.Closeness = min(cs.Closeness, 1)
			for _, opt := range Options(p, key) {
				want := 0.0
				if contains(acceptable, opt) {
					want = 1
				}
				d := agg.probs[opt] - want
				cs.Brier += d * d
			}
		}
		cs.Earned = cs.Credit * (w.Alpha*cs.Verdict + (1-w.Alpha)*cs.Closeness)
	} else {
		cs.Answered = agg.answered
		cs.Winner = agg.winner
	}

	// Layer 2 is skipped for an errored case (the call never produced a
	// verdict; the error rate carries that) and for a check production held
	// back because a sibling fired (its nudge was never the model's to deliver).
	if errored || cs.Suppressed {
		return cs
	}
	exp := nudgeNotWanted
	if !quiet {
		exp = expectedNudge(p, key, acceptable)
	}
	cs.Expect = exp.String()
	cw := w.For(key)
	switch {
	case exp == nudgeWanted && !cs.Nudged:
		cs.FN, cs.Penalty = true, cw.FN
	case exp == nudgeNotWanted && cs.Nudged:
		cs.FP, cs.Penalty = true, cw.FP
	}
	return cs
}

// focusVerdict classifies one sample's focus outcome against the label,
// applying the same sticky rule production does: when Oracle already set a
// sticky mode the right outcome is "no change" whatever the raw answer, so the
// raw layer scores the answer and this layer scores what production did.
func focusVerdict(acceptable []string, prior string, sticky []string, res gateway.OracleResult) string {
	mode, cleared := res.FocusMode, res.FocusCleared
	picks := make([]string, 0, len(acceptable))
	for _, a := range acceptable {
		if a != "off" {
			picks = append(picks, a)
		}
	}
	switch {
	case prior != "" && (contains(sticky, prior) || contains(acceptable, prior)):
		// Stay put: a sticky mode is never left, and a label that names the
		// current mode means keep it.
		if !cleared && (mode == "" || mode == prior) {
			return "ok"
		}
		return "fp"
	case len(picks) == 0: // label is "off"
		if prior != "" {
			if cleared {
				return "ok"
			}
			return "fn" // a stale mode Oracle set earlier was left in place
		}
		if mode == "" {
			return "ok"
		}
		return "fp"
	}
	switch {
	case mode != "" && contains(picks, mode):
		return "ok"
	case mode == "" && contains(acceptable, "off") && !cleared:
		return "ok"
	case mode == "":
		return "fn"
	}
	return "fp"
}

func scoreFocus(w Weights, acceptable []string, prior string, samples []Sample) *FocusScore {
	sticky := config.DefaultOracle().Checks["focus"].Sticky
	counts := map[string]int{}
	for _, s := range samples {
		counts[focusVerdict(acceptable, prior, sticky, s.Result)]++
	}
	if len(samples) == 0 {
		counts["fn"] = 1 // Oracle never ran: no mode was set
	}
	// Plurality, ties going to the more forgiving verdict.
	verdict := "ok"
	for _, v := range []string{"fn", "fp"} {
		if counts[v] > counts[verdict] {
			verdict = v
		}
	}
	fs := &FocusScore{Verdict: verdict, Credit: w.FocusActionCredit}
	cw := w.For("focus")
	switch verdict {
	case "ok":
		fs.Earned = fs.Credit
	case "fn":
		fs.Penalty = cw.FN
	case "fp":
		fs.Penalty = cw.FP
	}
	return fs
}

// ScoreCase scores one case on one model. budget > 0 scores late calls as
// Oracle-off; 0 means no latency cut.
func ScoreCase(w Weights, p *prompts.Set, cr CaseRun, budget time.Duration) CaseScore {
	c := cr.Case
	samples := usableSamples(cr.Samples, budget)
	errored := len(samples) == 0 && len(cr.Samples) > 0
	cs := CaseScore{ID: c.ID, Track: c.Track, Disputed: c.DisputedLabel, Errored: errored}

	keys := make([]string, 0, len(c.Expect))
	for k := range c.Expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		check := scoreCheck(w, p, key, c.Expect[key].Acceptable(), false, samples, errored)
		cs.Checks = append(cs.Checks, check)
		cs.Max += check.Credit
		cs.Earned += check.Earned
		cs.Penalty += check.Penalty
	}
	for _, key := range c.Quiet {
		check := scoreCheck(w, p, key, nil, true, samples, errored)
		cs.Checks = append(cs.Checks, check)
		cs.Penalty += check.Penalty
	}
	if e, ok := c.Expect["focus"]; ok {
		cs.Max += w.FocusActionCredit
		if !errored {
			cs.Focus = scoreFocus(w, e.Acceptable(), c.State.PriorOracleFocus, samples)
			cs.Earned += cs.Focus.Earned
			cs.Penalty += cs.Focus.Penalty
		}
	}
	cs.Score = cs.Earned - cs.Penalty
	return cs
}

// TrackScore is a per-track rollup, so a model strong overall but blind on one
// track shows it.
type TrackScore struct {
	Cases   int     `json:"cases"`
	Errored int     `json:"errored,omitempty"`
	Score   float64 `json:"score"`
}

// ModelScore is everything the leaderboard shows for one model. Headline
// columns exclude disputed-label cases, which are reported on their own so
// they can neither sink nor lift the ranking.
type ModelScore struct {
	Cases    int `json:"cases"`
	Disputed int `json:"disputed_cases"`

	Headline   float64 `json:"headline"`    // 0-100, no latency cut
	HeadlineLo float64 `json:"headline_lo"` // bootstrap 95% interval over cases
	HeadlineHi float64 `json:"headline_hi"`
	Budgeted   float64 `json:"headline_within_budget"` // late calls scored as Oracle-off
	DisputedHL float64 `json:"headline_disputed"`

	Verdict   float64 `json:"verdict"`
	Closeness float64 `json:"closeness"`
	Trigger   float64 `json:"trigger"` // share of trigger decisions that were right
	Focus     float64 `json:"focus"`   // share of focus actions that were right
	Brier     float64 `json:"brier"`   // mean over answered labeled checks; lower is better

	DangerousFP     int     `json:"dangerous_fp"`
	DangerousOpps   int     `json:"dangerous_opportunities"`
	DangerousFPRate float64 `json:"dangerous_fp_rate"`

	Calls        int     `json:"calls"`
	ErrorRate    float64 `json:"error_rate"` // refusals + timeouts + other errors
	RefusalRate  float64 `json:"refusal_rate"`
	WithinBudget float64 `json:"within_budget"` // share of calls that finished in time

	LatencyP50 time.Duration `json:"latency_p50"`
	LatencyP95 time.Duration `json:"latency_p95"`
	LatencyMax time.Duration `json:"latency_max"`

	CostUSD float64 `json:"cost_usd"`

	ByTrack      map[string]TrackScore `json:"by_track"`
	Disqualified []string              `json:"disqualified,omitempty"`
}

func pct(f float64) string { return strconv.FormatFloat(100*f, 'f', -1, 64) + "%" }

func ratio(num, den float64) float64 {
	if den == 0 {
		return 0
	}
	return num / den
}

func headline(cases []CaseScore) float64 {
	var s, m float64
	for _, c := range cases {
		s += c.Score
		m += c.Max
	}
	return 100 * ratio(s, m)
}

// bootstrapCI resamples cases with replacement and returns the 2.5th and 97.5th
// percentile headline. With ~250 cases the interval is roughly +-5-7 points, so
// a gap under 2-3 points between models is noise (the plan's rule). The seed is
// fixed so a leaderboard re-render is reproducible.
func bootstrapCI(cases []CaseScore, iters int, seed uint64) (lo, hi float64) {
	if len(cases) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	scores := make([]float64, iters)
	pick := make([]CaseScore, len(cases))
	for i := range scores {
		for j := range pick {
			pick[j] = cases[rng.IntN(len(cases))]
		}
		scores[i] = headline(pick)
	}
	sort.Float64s(scores)
	return scores[int(0.025*float64(iters))], scores[int(0.975*float64(iters))-1]
}

// percentile is nearest-rank over a sorted slice.
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(q*float64(len(sorted))+0.999999) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}

// Summarize scores every case for one model and rolls them up. It scores the
// bank twice, with and without the latency cut.
func Summarize(w Weights, p *prompts.Set, runs []CaseRun) ModelScore {
	ms := ModelScore{ByTrack: map[string]TrackScore{}}

	var graded, gradedBudget, disputed []CaseScore
	var verdictSum, closeSum, brierSum float64
	var labeled, brierN, trigRight, trigN, focusRight, focusN int

	for _, cr := range runs {
		cs := ScoreCase(w, p, cr, 0)
		bs := ScoreCase(w, p, cr, w.LatencyBudget)
		if cs.Disputed {
			disputed = append(disputed, cs)
			ms.Disputed++
			continue
		}
		graded = append(graded, cs)
		gradedBudget = append(gradedBudget, bs)

		tr := ms.ByTrack[cs.Track]
		tr.Cases++
		if cs.Errored {
			tr.Errored++
		}
		ms.ByTrack[cs.Track] = tr

		for _, ck := range cs.Checks {
			if ck.Quiet {
				if !cs.Errored && !ck.Suppressed {
					trigN++
					if !ck.FP {
						trigRight++
					}
					if ck.FP && w.IsDangerous(ck.Key) {
						ms.DangerousFP++
					}
					if w.IsDangerous(ck.Key) {
						ms.DangerousOpps++
					}
				}
				continue
			}
			labeled++
			verdictSum += ck.Verdict
			closeSum += ck.Closeness
			if ck.Answered {
				brierSum += ck.Brier
				brierN++
			}
			if cs.Errored || ck.Suppressed || ck.Expect != nudgeWanted.String() && ck.Expect != nudgeNotWanted.String() {
				continue
			}
			trigN++
			if !ck.FN && !ck.FP {
				trigRight++
			}
			if ck.Expect == nudgeNotWanted.String() && w.IsDangerous(ck.Key) {
				ms.DangerousOpps++
				if ck.FP {
					ms.DangerousFP++
				}
			}
		}
		if cs.Focus != nil {
			focusN++
			if cs.Focus.Verdict == "ok" {
				focusRight++
			}
		}
	}
	ms.Cases = len(graded)

	ms.Headline = headline(graded)
	ms.HeadlineLo, ms.HeadlineHi = bootstrapCI(graded, 1000, 1)
	ms.Budgeted = headline(gradedBudget)
	ms.DisputedHL = headline(disputed)

	ms.Verdict = ratio(verdictSum, float64(labeled))
	ms.Closeness = ratio(closeSum, float64(labeled))
	ms.Trigger = ratio(float64(trigRight), float64(trigN))
	ms.Focus = ratio(float64(focusRight), float64(focusN))
	ms.Brier = ratio(brierSum, float64(brierN))
	ms.DangerousFPRate = ratio(float64(ms.DangerousFP), float64(ms.DangerousOpps))

	byTrack := map[string][]CaseScore{}
	for _, c := range graded {
		byTrack[c.Track] = append(byTrack[c.Track], c)
	}
	for track, cases := range byTrack {
		tr := ms.ByTrack[track]
		tr.Score = headline(cases)
		ms.ByTrack[track] = tr
	}

	var durations []time.Duration
	failed, refused, inBudget := 0, 0, 0
	for _, cr := range runs {
		for _, s := range cr.Samples {
			ms.Calls++
			ms.CostUSD += s.Result.CostUSD
			if s.Err != ErrNone {
				failed++
				if s.Err == ErrRefusal {
					refused++
				}
				continue
			}
			durations = append(durations, s.Latency)
			if s.Latency <= w.LatencyBudget {
				inBudget++
			}
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	ms.LatencyP50, ms.LatencyP95 = percentile(durations, 0.5), percentile(durations, 0.95)
	if n := len(durations); n > 0 {
		ms.LatencyMax = durations[n-1]
	}
	ms.ErrorRate = ratio(float64(failed), float64(ms.Calls))
	ms.RefusalRate = ratio(float64(refused), float64(ms.Calls))
	ms.WithinBudget = ratio(float64(inBudget), float64(ms.Calls))

	if ms.ErrorRate > w.RefusalMaxRate {
		ms.Disqualified = append(ms.Disqualified, "error/refusal rate above "+pct(w.RefusalMaxRate))
	}
	if ms.DangerousFPRate > w.DangerousFPMaxRate {
		ms.Disqualified = append(ms.Disqualified, "dangerous false-nudge rate above "+pct(w.DangerousFPMaxRate))
	}
	return ms
}
