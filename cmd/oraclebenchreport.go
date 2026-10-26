package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"polaris/oraclebench"
)

var (
	obrRun    []string
	obrCalls  string
	obrOut    string
	obrPinned string

	obrAllowConcurrency bool
)

// The report is generated, never hand-drawn, so adding a model means re-running
// the bench and then this command, not editing charts.
var oracleBenchReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Regenerate the oracle-bench charts and results table from a saved leaderboard",
	Long: "Reads one saved leaderboard JSON (docs/benchmarks/oracle-decision-models/runs/*.json) and writes the\n" +
		"SVG charts plus results.md (a markdown table) into --out.\n\n" +
		"The share of calls inside production's 2.5s Oracle timeout, and where refusals landed, need the raw per-call log, which is\n" +
		"gitignored. Pass it once with --calls (all runs' logs) and the tally is saved to call-stats.json beside the charts;\n" +
		"later runs of this command reuse that file.",
	RunE: runOracleBenchReport,
}

func init() {
	f := oracleBenchReportCmd.Flags()
	f.StringSliceVar(&obrRun, "run", nil, "saved leaderboard JSON(s) to report on, comma-separated or repeated; several runs are merged (required)")
	f.BoolVar(&obrAllowConcurrency, "allow-concurrency-mismatch", false, "merge runs that differ only in concurrency; those models' rows are marked in results.md because their latency was measured under a different load")
	f.StringVar(&obrCalls, "calls", "", "raw calls JSONL (glob) used to tally the share of calls under the production timeout")
	f.StringVar(&obrOut, "out", "docs/benchmarks/oracle-decision-models", "directory the SVGs, results.md and call-stats.json are written to")
	f.StringVar(&obrPinned, "pinned", "typesafe/jev-1.14", "model currently pinned in production, highlighted in the charts")
	oracleBenchCmd.AddCommand(oracleBenchReportCmd)
}

func runOracleBenchReport(cmd *cobra.Command, args []string) error {
	if len(obrRun) == 0 {
		return fmt.Errorf("--run is required")
	}
	var boards []oraclebench.Leaderboard
	for _, path := range obrRun {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var b oraclebench.Leaderboard
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		boards = append(boards, b)
	}
	board, err := oraclebench.MergeLeaderboards(boards, obrAllowConcurrency)
	if err != nil {
		return err
	}

	sharePath := filepath.Join(obrOut, "call-stats.json")
	shares := map[string]oraclebench.CallStats{}
	if obrCalls != "" {
		paths, err := filepath.Glob(obrCalls)
		if err != nil || len(paths) == 0 {
			return fmt.Errorf("no calls files match %q", obrCalls)
		}
		if shares, err = oraclebench.TallyCalls(paths); err != nil {
			return err
		}
		js, _ := json.MarshalIndent(shares, "", "  ")
		if err := os.WriteFile(sharePath, append(js, '\n'), 0o644); err != nil {
			return err
		}
	} else if b, err := os.ReadFile(sharePath); err == nil {
		_ = json.Unmarshal(b, &shares)
	}

	for name, svg := range oraclebench.ReportCharts(board, obrPinned, shares) {
		if err := os.WriteFile(filepath.Join(obrOut, name), []byte(svg), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(obrOut, "results.md"), []byte(resultsTable(board, shares)), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote charts and results.md to %s\n", obrOut)
	return nil
}

// resultsTable is the headline leaderboard as markdown, generated from the same
// JSON as the charts so the numbers in the README can't drift from the saved run.
func resultsTable(b oraclebench.Leaderboard, shares map[string]oraclebench.CallStats) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Run %s: %d cases x %d repeats, concurrency %d, weights `%s`.\n\n", b.RunAt, b.Cases, b.Repeats, b.Concurrency, b.WeightsHash)
	sb.WriteString("| # | Model | Score (95% CI) | Verdict | Trigger | Focus | Danger FP | Errors | p50 | p95 | Under 2.5s | $/1k turns |\n")
	sb.WriteString("|--:|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	entries := b.Entries // saved already ranked by BuildLeaderboard
	rank := 0
	for _, e := range entries {
		if e.NA != "" {
			fmt.Fprintf(&sb, "| - | `%s` | N/A: %s | | | | | | | | | |\n", e.Model, e.NA)
			continue
		}
		s := e.Score
		n := "x"
		if len(s.Disqualified) == 0 {
			rank++
			n = fmt.Sprint(rank)
		}
		under := "-"
		if sh, ok := shares[e.Model]; ok && sh.Answered > 0 {
			under = fmt.Sprintf("%.1f%%", 100*float64(sh.Under2_5s)/float64(s.Calls))
		}
		per1k := 0.0
		if s.Calls > 0 {
			per1k = s.CostUSD / float64(s.Calls) * 1000
		}
		name := "`" + e.Model + "`"
		if _, ok := b.RanAtConcurrency[e.Model]; ok {
			name += " †"
		}
		fmt.Fprintf(&sb, "| %s | %s | **%.1f** (%.1f-%.1f) | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %s | %s | %s | $%.3f |\n",
			n, name, s.Headline, s.HeadlineLo, s.HeadlineHi, 100*s.Verdict, 100*s.Trigger, 100*s.Focus,
			100*s.DangerousFPRate, 100*s.ErrorRate, dur(s.LatencyP50), dur(s.LatencyP95), under, per1k)
	}
	var marked []string
	for _, e := range entries {
		if c, ok := b.RanAtConcurrency[e.Model]; ok {
			marked = append(marked, fmt.Sprintf("`%s` (%d)", e.Model, c))
		}
	}
	if len(marked) > 0 {
		fmt.Fprintf(&sb, "\n† ran at a different concurrency than the rest (%d): %s. Their latency and under-2.5s share were measured under a different load; accuracy is unaffected.\n",
			b.Concurrency, strings.Join(marked, ", "))
	}
	for _, e := range entries {
		for _, reason := range e.Score.Disqualified {
			fmt.Fprintf(&sb, "\nx = disqualified, `%s`: %s.\n", e.Model, reason)
		}
	}
	return sb.String()
}

func dur(d interface{ Milliseconds() int64 }) string {
	ms := d.Milliseconds()
	if ms <= 0 {
		return "-"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
