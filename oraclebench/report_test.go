package oraclebench

import (
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
