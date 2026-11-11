package oraclebench

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTallyCalls(t *testing.T) {
	p := filepath.Join(t.TempDir(), "calls.jsonl")
	lines := `{"model":"a","latency_ms":100}
{"model":"a","latency_ms":2600}
{"model":"a","latency_ms":300,"err":"timeout"}
{"model":"a","track":"medical","latency_ms":80,"err":"refusal"}
{"model":"a","track":"medical","latency_ms":90}
{"model":"b","latency_ms":2500}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TallyCalls([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	// Errored calls are not answers, so they count toward neither timing bucket;
	// the timeout boundary itself is inclusive (a call at exactly 2.5s still lands).
	// A refusal is also a failure, tallied by track so the report can say where.
	a := got["a"]
	if a.Calls != 5 || a.Answered != 3 || a.Under2_5s != 2 || a.Under5s != 3 || a.Failed != 2 {
		t.Errorf("a = %+v", a)
	}
	if a.Refused != 1 || a.RefusedByTrack["medical"] != 1 || a.CallsByTrack["medical"] != 2 {
		t.Errorf("a refusals = %+v", a)
	}
	if b := got["b"]; b.Answered != 1 || b.Under2_5s != 1 {
		t.Errorf("b = %+v", b)
	}
}

func TestReportChartsSkipDisqualified(t *testing.T) {
	good := Entry{Model: "v/good", Score: ModelScore{Headline: 90, HeadlineLo: 85, HeadlineHi: 95, Calls: 10, CostUSD: 0.01,
		LatencyP50: 200 * time.Millisecond, LatencyP95: 400 * time.Millisecond, ByTrack: map[string]TrackScore{"chips": {Cases: 5, Score: 88}}}}
	bad := Entry{Model: "v/broken", Score: ModelScore{Disqualified: []string{"error rate"}}}
	charts := ReportCharts(Leaderboard{Entries: []Entry{good, bad}}, "v/good", nil)
	if len(charts) != 10 {
		t.Fatalf("want 10 charts, got %d", len(charts))
	}
	for name, svg := range charts {
		if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(strings.TrimSpace(svg), "</svg>") {
			t.Errorf("%s is not a complete svg", name)
		}
		if !strings.Contains(svg, "good") {
			t.Errorf("%s is missing the eligible model", name)
		}
		// The scorecard deliberately lists a disqualified model (dimmed, with its
		// reason) and the refusals chart lists every model, since a disqualified model's
		// refusal rate is the point of that chart; the comparison charts must not plot it.
		if name != "scorecard.svg" && name != "refusals.svg" && strings.Contains(svg, "broken") {
			t.Errorf("%s plots a disqualified model", name)
		}
	}
}

func TestMergeLeaderboards(t *testing.T) {
	a := Leaderboard{RunAt: "t1", Cases: 295, Repeats: 3, Concurrency: 2, WeightsHash: "w", Entries: []Entry{
		{Model: "m/a", Score: ModelScore{Headline: 90}}, {Model: "m/b", Score: ModelScore{Headline: 80}}}}
	b := Leaderboard{RunAt: "t2", Cases: 295, Repeats: 3, Concurrency: 2, WeightsHash: "w", Entries: []Entry{
		{Model: "m/c", Score: ModelScore{Headline: 85}}, {Model: "m/b", Score: ModelScore{Headline: 82}}}}
	got, err := MergeLeaderboards([]Leaderboard{a, b}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RanAtConcurrency) != 0 {
		t.Errorf("same-concurrency runs should record no concurrency notes, got %v", got.RanAtConcurrency)
	}
	if len(got.Entries) != 3 || got.Entries[0].Model != "m/a" || got.Entries[1].Model != "m/c" {
		t.Errorf("merged and re-ranked wrong: %+v", got.Entries)
	}
	// A model in both runs takes the later one.
	for _, e := range got.Entries {
		if e.Model == "m/b" && e.Score.Headline != 82 {
			t.Errorf("m/b = %v, want the later run's 82", e.Score.Headline)
		}
	}
	// Runs scored under different weights are not comparable, so merging refuses.
	b.WeightsHash = "other"
	if _, err := MergeLeaderboards([]Leaderboard{a, b}, false); err == nil {
		t.Error("merging runs with different weights should fail")
	}
	// ...and the opt-in only covers concurrency, never weights, cases or repeats.
	if _, err := MergeLeaderboards([]Leaderboard{a, b}, true); err == nil {
		t.Error("allowConcurrencyMismatch must not let different weights through")
	}
}

func TestMergeLeaderboardsConcurrencyMismatchIsOptInAndMarked(t *testing.T) {
	a := Leaderboard{RunAt: "t1", Cases: 295, Repeats: 3, Concurrency: 2, WeightsHash: "w", Entries: []Entry{
		{Model: "m/a", Score: ModelScore{Headline: 90}}}}
	low := Leaderboard{RunAt: "t2", Cases: 295, Repeats: 3, Concurrency: 1, WeightsHash: "w", Entries: []Entry{
		{Model: "m/slow", Score: ModelScore{Headline: 88}}}}

	if _, err := MergeLeaderboards([]Leaderboard{a, low}, false); err == nil {
		t.Fatal("a concurrency mismatch must be refused unless explicitly allowed")
	}
	got, err := MergeLeaderboards([]Leaderboard{a, low}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.RanAtConcurrency["m/slow"]; !ok || c != 1 {
		t.Errorf("RanAtConcurrency = %v, want m/slow at 1", got.RanAtConcurrency)
	}
	if _, marked := got.RanAtConcurrency["m/a"]; marked {
		t.Error("the baseline run's models must not be marked")
	}

	// A model re-run later at the baseline concurrency sheds its old note.
	rerun := Leaderboard{RunAt: "t3", Cases: 295, Repeats: 3, Concurrency: 2, WeightsHash: "w", Entries: []Entry{
		{Model: "m/slow", Score: ModelScore{Headline: 87}}}}
	got, err = MergeLeaderboards([]Leaderboard{a, low, rerun}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, marked := got.RanAtConcurrency["m/slow"]; marked {
		t.Errorf("m/slow was re-run at concurrency 2 but is still marked: %v", got.RanAtConcurrency)
	}
}

// The raw log is the only place an older run's per-repeat costs survive, so the
// tally is how a run made before fresh-cost capture gets the production-basis
// figure without being re-run. Repeat 0 is the only call per case that sends a
// fresh state.
func TestTallyCalls_ProductionCostIsRepeatZeroOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "calls.jsonl")
	lines := `{"model":"a","repeat":0,"latency_ms":1500,"cost_usd":0.001,"input_tokens":3000,"cached_tokens":2000}
{"model":"a","repeat":1,"latency_ms":1500,"cost_usd":0.0009,"input_tokens":3000,"cached_tokens":2900}
{"model":"a","repeat":2,"latency_ms":1500,"cost_usd":0.0009,"input_tokens":3000,"cached_tokens":2900}
{"model":"a","repeat":0,"latency_ms":1500,"cost_usd":0.001,"input_tokens":3000,"cached_tokens":2000}
{"model":"b","repeat":0,"latency_ms":200,"cost_usd":0.0002}
{"model":"b","repeat":1,"latency_ms":200,"cost_usd":0.0002}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TallyCalls([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	a := got["a"]
	if a.FreshCalls != 2 || math.Abs(a.CostUSDFresh-0.002) > 1e-12 {
		t.Errorf("a fresh = %.6f over %d, want 0.002 over 2 (one per case)", a.CostUSDFresh, a.FreshCalls)
	}
	// Four calls for a (two cases x repeats 0-2 and 0-1): tokens total across all
	// of them, unlike the cost, which is the first call of each case only.
	if a.InputTokens != 12000 || a.CachedTokens != 9800 {
		t.Errorf("a tokens = %d/%d, want 12000/9800", a.InputTokens, a.CachedTokens)
	}
	// A log written before tokens were captured still yields a cost basis.
	if b := got["b"]; b.FreshCalls != 1 || math.Abs(b.CostUSDFresh-0.0002) > 1e-12 || b.CachedTokens != 0 {
		t.Errorf("b = %+v", b)
	}
}

// CostPer1k must prefer the run's own fresh-cost figure, fall back to a
// log-derived one, and only then to the repeat average — and say which it used,
// because the report marks the fallback so two bases never share a column
// silently.
func TestCostPer1k_PrefersProductionBasisAndReportsWhenItCannot(t *testing.T) {
	e := Entry{Model: "m", Score: ModelScore{Calls: 300, CostUSD: 0.30, FreshCalls: 100, FreshCostUSD: 0.12}}
	if got, known := CostPer1k(e, nil); !known || math.Abs(got-1.2) > 1e-9 {
		t.Errorf("with fresh data: %.4f known=%v, want 1.2 true", got, known)
	}

	// A pre-capture leaderboard, with the fact recovered from the raw log.
	e = Entry{Model: "m", Score: ModelScore{Calls: 300, CostUSD: 0.30}}
	shares := map[string]CallStats{"m": {FreshCalls: 100, CostUSDFresh: 0.15}}
	if got, known := CostPer1k(e, shares); !known || math.Abs(got-1.5) > 1e-9 {
		t.Errorf("from the tally: %.4f known=%v, want 1.5 true", got, known)
	}

	// Nothing but the repeat average: usable, but flagged as not known.
	e = Entry{Model: "m", Score: ModelScore{Calls: 300, CostUSD: 0.30}}
	if got, known := CostPer1k(e, nil); known || math.Abs(got-1.0) > 1e-9 {
		t.Errorf("fallback: %.4f known=%v, want 1.0 false", got, known)
	}
}
