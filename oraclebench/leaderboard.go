package oraclebench

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Entry is one model's row.
type Entry struct {
	Model    string     `json:"model"`
	Resolved []string   `json:"resolved_models,omitempty"`
	NA       string     `json:"na,omitempty"`
	Score    ModelScore `json:"score"`
}

// Leaderboard is the whole comparison plus the settings needed to interpret it.
type Leaderboard struct {
	RunAt       string        `json:"run_at"`
	Cases       int           `json:"cases"`
	Repeats     int           `json:"repeats"`
	Concurrency int           `json:"concurrency"`
	Budget      time.Duration `json:"latency_budget"`
	WeightsHash string        `json:"weights_hash"`
	Entries     []Entry       `json:"entries"`
}

// tieGap is the headline gap under which two models are not meaningfully
// different: with ~250 cases the bootstrap interval is roughly +-5-7 points, so
// the plan calls anything under 2-3 points noise and prefers the cheaper or
// faster model on a tie.
const tieGap = 2.5

// BuildLeaderboard ranks the entries. Eligible models come first by headline
// (ties by within-budget score, then p95 latency); models over a hard filter
// follow, still ranked, so a disqualified model's numbers stay visible rather
// than vanishing; N/A models last.
func BuildLeaderboard(l Leaderboard) Leaderboard {
	rank := func(e Entry) int {
		switch {
		case e.NA != "":
			return 2
		case len(e.Score.Disqualified) > 0:
			return 1
		}
		return 0
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		a, b := l.Entries[i], l.Entries[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if a.Score.Headline != b.Score.Headline {
			return a.Score.Headline > b.Score.Headline
		}
		if a.Score.Budgeted != b.Score.Budgeted {
			return a.Score.Budgeted > b.Score.Budgeted
		}
		return a.Score.LatencyP95 < b.Score.LatencyP95
	})
	return l
}

func fmtDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func fmtPct(f float64) string { return fmt.Sprintf("%.0f%%", 100*f) }

// Render writes the leaderboard and the latency chart.
func (l Leaderboard) Render(w io.Writer) {
	fmt.Fprintf(w, "\nOracle bench: %d cases x %d repeats, concurrency %d, latency budget %s, weights %s\n\n",
		l.Cases, l.Repeats, l.Concurrency, fmtDur(l.Budget), l.WeightsHash)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tmodel\tscore\t95% CI\tin-budget\tverdict\tclose\ttrigger\tfocus\tdanger FP\terr\tp50\tp95\tcost\t")
	rankN, leader := 0, -1.0
	for _, e := range l.Entries {
		if e.NA != "" {
			fmt.Fprintf(tw, "-\t%s\tN/A (%s)\t\t\t\t\t\t\t\t\t\t\t\t\n", e.Model, e.NA)
			continue
		}
		s := e.Score
		mark := " "
		if len(s.Disqualified) == 0 {
			rankN++
			if leader < 0 {
				leader = s.Headline
			} else if leader-s.Headline <= tieGap {
				mark = "≈" // not meaningfully different from the leader
			}
		}
		num := fmt.Sprint(rankN)
		if len(s.Disqualified) > 0 {
			num = "x"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%.1f\t%.1f-%.1f\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t$%.3f\t\n",
			num, mark, e.Model, s.Headline, s.HeadlineLo, s.HeadlineHi, s.Budgeted,
			fmtPct(s.Verdict), fmtPct(s.Closeness), fmtPct(s.Trigger), fmtPct(s.Focus),
			fmtPct(s.DangerousFPRate), fmtPct(s.ErrorRate), fmtDur(s.LatencyP50), fmtDur(s.LatencyP95), s.CostUSD+s.JudgeCostUSD)
	}
	tw.Flush()

	fmt.Fprintln(w, "\n≈ within", tieGap, "points of the leader: treat as a tie and prefer the faster or pinned model.")
	header := false
	for _, e := range l.Entries {
		for _, reason := range e.Score.Disqualified {
			if !header {
				fmt.Fprintln(w, "x = disqualified:")
				header = true
			}
			fmt.Fprintf(w, "  %s: %s\n", e.Model, reason)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, LatencyChart(l.Entries, l.Budget, 40))
	renderTrackWeakSpots(w, l)
}

// LatencyChart ranks models by median latency, fastest first. The bar is p50
// (filled) out to p95 (shaded); `|` marks the budget, and a model whose p95
// passes it is flagged. Latency is shown apart from accuracy on purpose: "most
// accurate" and "accurate fast enough to ship" are different questions.
func LatencyChart(entries []Entry, budget time.Duration, width int) string {
	var rows []Entry
	var scale time.Duration = budget * 5 / 4
	for _, e := range entries {
		if e.NA != "" || e.Score.LatencyP50 <= 0 {
			continue
		}
		rows = append(rows, e)
		scale = max(scale, e.Score.LatencyP95)
	}
	if len(rows) == 0 {
		return ""
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.LatencyP50 < rows[j].Score.LatencyP50 })

	nameW := 0
	for _, e := range rows {
		nameW = max(nameW, len([]rune(e.Model)))
	}
	cell := func(d time.Duration) int { return int(float64(d) / float64(scale) * float64(width)) }

	var b strings.Builder
	fmt.Fprintf(&b, "Latency, fastest first (█ p50, ░ out to p95, | = %s budget):\n", fmtDur(budget))
	budgetCol := min(cell(budget), width-1)
	for _, e := range rows {
		p50, p95 := cell(e.Score.LatencyP50), cell(e.Score.LatencyP95)
		bar := make([]rune, width)
		for i := range bar {
			switch {
			case i < p50:
				bar[i] = '█'
			case i < p95:
				bar[i] = '░'
			default:
				bar[i] = ' '
			}
		}
		if bar[budgetCol] == ' ' {
			bar[budgetCol] = '|'
		}
		flag := ""
		if e.Score.LatencyP95 > budget {
			flag = "  over budget at p95"
		}
		fmt.Fprintf(&b, "  %-*s  %s  p50 %-6s p95 %-6s%s\n", nameW, e.Model, string(bar), fmtDur(e.Score.LatencyP50), fmtDur(e.Score.LatencyP95), flag)
	}
	return b.String()
}

// renderTrackWeakSpots lists, for each eligible model, any track scoring well
// below its own headline, so a model strong overall but blind on one track
// (the refusals-masquerading-as-accuracy risk) is visible in the terminal.
func renderTrackWeakSpots(w io.Writer, l Leaderboard) {
	var lines []string
	for _, e := range l.Entries {
		if e.NA != "" {
			continue
		}
		var weak []string
		for track, ts := range e.Score.ByTrack {
			if ts.Cases >= 3 && ts.Score < e.Score.Headline-25 {
				weak = append(weak, fmt.Sprintf("%s %.0f", track, ts.Score))
			}
		}
		if len(weak) > 0 {
			sort.Strings(weak)
			lines = append(lines, fmt.Sprintf("  %s: %s", e.Model, strings.Join(weak, ", ")))
		}
	}
	if len(lines) > 0 {
		fmt.Fprintln(w, "\nTracks scoring 25+ points below the model's own headline:")
		for _, ln := range lines {
			fmt.Fprintln(w, ln)
		}
	}
}

// JSON is the machine-readable leaderboard (oracle-bench-leaderboard.json).
func (l Leaderboard) JSON() ([]byte, error) { return json.MarshalIndent(l, "", "  ") }
