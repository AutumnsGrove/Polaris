package oraclebench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTallyLatency(t *testing.T) {
	p := filepath.Join(t.TempDir(), "calls.jsonl")
	lines := `{"model":"a","latency_ms":100}
{"model":"a","latency_ms":2600}
{"model":"a","latency_ms":300,"err":"timeout"}
{"model":"b","latency_ms":2500}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TallyLatency([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	// Errored calls are not answers, so they count toward neither bucket; the
	// timeout boundary itself is inclusive (a call at exactly 2.5s still lands).
	if a := got["a"]; a.Answered != 2 || a.Under2_5s != 1 || a.Under5s != 2 {
		t.Errorf("a = %+v", a)
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
	if len(charts) != 9 {
		t.Fatalf("want 9 charts, got %d", len(charts))
	}
	for name, svg := range charts {
		if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(strings.TrimSpace(svg), "</svg>") {
			t.Errorf("%s is not a complete svg", name)
		}
		if !strings.Contains(svg, "good") {
			t.Errorf("%s is missing the eligible model", name)
		}
		// The scorecard deliberately lists a disqualified model (dimmed, with its
		// reason) so a failed run is visible; the comparison charts must not plot it.
		if name != "scorecard.svg" && strings.Contains(svg, "broken") {
			t.Errorf("%s plots a disqualified model", name)
		}
	}
}
