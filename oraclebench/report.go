package oraclebench

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// ProductionTimeout mirrors gateway's oracleTimeout (2.5s): the point past which
// a real Oracle turn silently drops the decision. The bench's own scoring budget
// (weights.yaml, 5s) is deliberately looser, so the report draws both.
const ProductionTimeout = 2500 * time.Millisecond

// LatencyShare is one model's share of calls finishing inside the production
// timeout and the bench budget, counted over calls that returned an answer.
// It is computed from the raw per-call log (which is gitignored), then saved
// beside the committed leaderboard so the report can be regenerated without it.
type LatencyShare struct {
	Answered  int `json:"answered"`
	Under2_5s int `json:"under_2_5s"`
	Under5s   int `json:"under_5s"`
}

// callLine is the subset of a raw call record the latency tally needs.
type callLine struct {
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	Err       string `json:"err"`
}

// TallyLatency reads one or more raw calls JSONL files and counts, per model,
// how many successful calls fit inside the production timeout.
func TallyLatency(paths []string) (map[string]LatencyShare, error) {
	out := map[string]LatencyShare{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var c callLine
			if err := json.Unmarshal([]byte(line), &c); err != nil || c.Err != "" {
				continue
			}
			s := out[c.Model]
			s.Answered++
			if time.Duration(c.LatencyMS)*time.Millisecond <= ProductionTimeout {
				s.Under2_5s++
			}
			if time.Duration(c.LatencyMS)*time.Millisecond <= 5*time.Second {
				s.Under5s++
			}
			out[c.Model] = s
		}
	}
	return out, nil
}

// ReportCharts renders every chart for a leaderboard as name -> SVG. Models
// over a hard filter (error rate, dangerous false nudges) are left out of the
// charts that compare quality, since their numbers describe a broken run, and
// are listed in the table instead.
func ReportCharts(l Leaderboard, pinned string) map[string]string {
	var ok []Entry
	for _, e := range l.Entries {
		if e.NA == "" && len(e.Score.Disqualified) == 0 {
			ok = append(ok, e)
		}
	}
	return map[string]string{
		"accuracy-vs-latency.svg": scatterSVG(ok, pinned),
		"latency.svg":             latencySVG(ok, pinned),
		"tracks.svg":              trackHeatmapSVG(ok),
		"breakdown.svg":           breakdownSVG(ok),
		"cost.svg":                costSVG(ok, pinned),
	}
}

// svgStyle gives every chart light and dark palettes of its own, because GitHub
// renders an SVG referenced by <img> in the viewer's colour scheme.
const svgStyle = `<style>
svg{--bg:#fcfcfb;--ink:#0b0b0b;--ink2:#52514e;--grid:#e3e2de;--accent:#2a78d6;--hi:#eb6834;--on-accent:#ffffff;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
@media (prefers-color-scheme:dark){svg{--bg:#1a1a19;--ink:#f0efec;--ink2:#c3c2b7;--grid:#34332f;--accent:#3987e5;--hi:#d95926}}
.bg{fill:var(--bg)}.t{fill:var(--ink);font-size:13px}.t2{fill:var(--ink2);font-size:11px}.h{fill:var(--ink);font-size:15px;font-weight:600}
.grid{stroke:var(--grid);stroke-width:1}.ref{stroke:var(--ink2);stroke-width:1;stroke-dasharray:4 3}
.pt{fill:var(--accent)}.pin{fill:var(--hi)}.line{stroke:var(--accent);stroke-width:2}.lpin{stroke:var(--hi);stroke-width:2}
.bar{fill:var(--accent)}.barpin{fill:var(--hi)}.ca{fill:var(--on-accent);font-size:11px}
</style>`

type svgDoc struct {
	b    strings.Builder
	w, h int
}

func newSVG(w, h int, title, subtitle string) *svgDoc {
	d := &svgDoc{w: w, h: h}
	fmt.Fprintf(&d.b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" role="img" aria-label="%s">`, w, h, w, html.EscapeString(title))
	d.b.WriteString(svgStyle)
	fmt.Fprintf(&d.b, `<rect class="bg" width="%d" height="%d" rx="8"/>`, w, h)
	fmt.Fprintf(&d.b, `<text class="h" x="20" y="28">%s</text>`, html.EscapeString(title))
	fmt.Fprintf(&d.b, `<text class="t2" x="20" y="46">%s</text>`, html.EscapeString(subtitle))
	return d
}

func (d *svgDoc) text(class string, x, y float64, anchor, s string) {
	fmt.Fprintf(&d.b, `<text class="%s" x="%.1f" y="%.1f" text-anchor="%s">%s</text>`, class, x, y, anchor, html.EscapeString(s))
}

func (d *svgDoc) String() string { return d.b.String() + "</svg>\n" }

// shortName drops the vendor prefix: "typesafe/jev-1.14" -> "jev-1.14".
func shortName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// scatterSVG is the money chart: accuracy against median latency on a log
// axis, with the production timeout and the bench budget marked. Up and to the
// left is better.
func scatterSVG(es []Entry, pinned string) string {
	d := newSVG(760, 456, "Accuracy vs. speed", "Headline score against median latency (log scale). Up and left is better.")
	const left, right, top, bottom = 56.0, 740.0, 70.0, 380.0
	minMS, maxMS := 100.0, 6000.0
	x := func(ms float64) float64 {
		return left + (math.Log(clamp(ms, minMS, maxMS))-math.Log(minMS))/(math.Log(maxMS)-math.Log(minMS))*(right-left)
	}
	lo, hi := 100.0, 0.0
	for _, e := range es {
		lo, hi = math.Min(lo, e.Score.HeadlineLo), math.Max(hi, e.Score.HeadlineHi)
	}
	lo, hi = math.Floor(lo/5)*5-5, math.Min(100, math.Ceil(hi/5)*5)
	y := func(v float64) float64 { return bottom - (clamp(v, lo, hi)-lo)/(hi-lo)*(bottom-top) }

	for v := math.Ceil(lo/5) * 5; v <= hi; v += 5 {
		fmt.Fprintf(&d.b, `<line class="grid" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, left, right, y(v), y(v))
		d.text("t2", left-8, y(v)+4, "end", fmt.Sprintf("%.0f", v))
	}
	for _, ms := range []float64{100, 200, 500, 1000, 2000, 5000} {
		d.text("t2", x(ms), bottom+18, "middle", fmtDur(time.Duration(ms)*time.Millisecond))
	}
	d.text("t2", (left+right)/2, bottom+36, "middle", "median latency (p50)")
	for _, r := range []struct {
		ms    time.Duration
		label string
	}{{ProductionTimeout, "production timeout 2.5s"}, {5 * time.Second, "bench budget 5s"}} {
		fmt.Fprintf(&d.b, `<line class="ref" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, x(float64(r.ms.Milliseconds())), x(float64(r.ms.Milliseconds())), top, bottom)
		d.text("t2", x(float64(r.ms.Milliseconds()))-4, top+12, "end", r.label)
	}
	// Labels try four spots around their point and take the first that doesn't
	// overlap one already placed; near-identical models (the two Jev versions)
	// would otherwise print on top of each other.
	type box struct{ x0, x1, y0, y1 float64 }
	var placed []box
	for _, e := range es {
		s := e.Score
		cx, cy := x(float64(s.LatencyP50.Milliseconds())), y(s.Headline)
		cls, lcls := "pt", "line"
		if e.Model == pinned {
			cls, lcls = "pin", "lpin"
		}
		fmt.Fprintf(&d.b, `<line class="%s" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, lcls, cx, cx, y(s.HeadlineLo), y(s.HeadlineHi))
		fmt.Fprintf(&d.b, `<circle class="%s" cx="%.1f" cy="%.1f" r="5"><title>%s: %.1f (%.1f-%.1f), p50 %s</title></circle>`,
			cls, cx, cy, html.EscapeString(e.Model), s.Headline, s.HeadlineLo, s.HeadlineHi, fmtDur(s.LatencyP50))
		name := shortName(e.Model)
		wd := 7.2 * float64(len(name))
		spots := []struct {
			dx, dy float64
			anchor string
		}{{9, 4, "start"}, {9, -10, "start"}, {-9, 4, "end"}, {-9, -10, "end"}, {9, 18, "start"}, {-9, 18, "end"}}
		// A point with a close neighbour on its right labels on the left, so the
		// text sits beside the point it names rather than the neighbour's.
		for _, o := range es {
			if ox := x(float64(o.Score.LatencyP50.Milliseconds())); o.Model != e.Model && ox > cx && ox-cx < 20 && math.Abs(y(o.Score.Headline)-cy) < 20 {
				spots[0], spots[2], spots[1], spots[3] = spots[2], spots[0], spots[3], spots[1]
				break
			}
		}
		for si, sp := range spots {
			b := box{cx + sp.dx, cx + sp.dx + wd, cy + sp.dy - 11, cy + sp.dy + 3}
			if sp.anchor == "end" {
				b.x0, b.x1 = cx+sp.dx-wd, cx+sp.dx
			}
			clash := false
			for _, o := range placed {
				if b.x0 < o.x1 && b.x1 > o.x0 && b.y0 < o.y1 && b.y1 > o.y0 {
					clash = true
				}
			}
			if !clash || si == len(spots)-1 {
				placed = append(placed, b)
				d.text("t", cx+sp.dx, cy+sp.dy, sp.anchor, name)
				break
			}
		}
	}
	d.text("t2", left, 446, "start", "Vertical bars are 95% intervals. Orange is the model pinned in production. Gaps under 2.5 points are ties.")
	return d.String()
}

// latencySVG draws p50 bars with a p95 whisker, fastest first.
func latencySVG(es []Entry, pinned string) string {
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.LatencyP50 < rows[j].Score.LatencyP50 })
	h := 90 + 30*len(rows)
	d := newSVG(760, h, "Latency", "Median (bar) and 95th percentile (whisker) per call, fastest first.")
	const left, right = 150.0, 720.0
	maxMS := 5000.0
	for _, e := range rows {
		maxMS = math.Max(maxMS, float64(e.Score.LatencyP95.Milliseconds()))
	}
	x := func(ms float64) float64 { return left + ms/maxMS*(right-left) }
	for _, ms := range []float64{0, 1000, 2000, 3000, 4000, 5000} {
		fmt.Fprintf(&d.b, `<line class="grid" x1="%.1f" x2="%.1f" y1="64" y2="%d"/>`, x(ms), x(ms), h-26)
		d.text("t2", x(ms), float64(h-10), "middle", fmtDur(time.Duration(ms)*time.Millisecond))
	}
	fmt.Fprintf(&d.b, `<line class="ref" x1="%.1f" x2="%.1f" y1="64" y2="%d"/>`, x(2500), x(2500), h-26)
	d.text("t2", x(2500)+4, 76, "start", "production timeout")
	for i, e := range rows {
		cy := 92.0 + 30*float64(i)
		cls, lcls := "bar", "line"
		if e.Model == pinned {
			cls, lcls = "barpin", "lpin"
		}
		d.text("t", left-10, cy+4, "end", shortName(e.Model))
		fmt.Fprintf(&d.b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="12" rx="2"><title>%s p50 %s, p95 %s</title></rect>`,
			cls, left, cy-6, math.Max(2, x(float64(e.Score.LatencyP50.Milliseconds()))-left), html.EscapeString(e.Model), fmtDur(e.Score.LatencyP50), fmtDur(e.Score.LatencyP95))
		fmt.Fprintf(&d.b, `<line class="%s" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, lcls, x(float64(e.Score.LatencyP50.Milliseconds())), x(float64(e.Score.LatencyP95.Milliseconds())), cy, cy)
		d.text("t2", x(float64(e.Score.LatencyP95.Milliseconds()))+6, cy+4, "start", fmt.Sprintf("p50 %s / p95 %s", fmtDur(e.Score.LatencyP50), fmtDur(e.Score.LatencyP95)))
	}
	return d.String()
}

// heatmap draws a models-by-columns grid of 0-100 values in one hue, darker
// meaning higher. Values print in every cell, so colour is never the only cue.
func heatmap(title, subtitle string, es []Entry, cols []string, val func(Entry, string) (float64, bool)) string {
	const cellW, cellH, left, top = 62.0, 26.0, 150.0, 96.0
	w := int(left + cellW*float64(len(cols)) + 20)
	h := int(top + cellH*float64(len(es)) + 24)
	d := newSVG(max(w, 520), h, title, subtitle)
	for ci, c := range cols {
		cx := left + cellW*float64(ci) + cellW/2
		fmt.Fprintf(&d.b, `<text class="t2" transform="translate(%.1f %.1f) rotate(-35)" text-anchor="start">%s</text>`, cx-4, top-8, html.EscapeString(c))
	}
	for ri, e := range es {
		cy := top + cellH*float64(ri)
		d.text("t", left-10, cy+cellH/2+4, "end", shortName(e.Model))
		for ci, c := range cols {
			v, ok := val(e, c)
			cx := left + cellW*float64(ci)
			if !ok {
				d.text("t2", cx+cellW/2, cy+cellH/2+4, "middle", "-")
				continue
			}
			op := 0.08 + 0.92*clamp((v-40)/60, 0, 1)
			fmt.Fprintf(&d.b, `<rect fill="var(--accent)" fill-opacity="%.2f" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="3"><title>%s / %s: %.0f</title></rect>`,
				op, cx+1, cy+1, cellW-2, cellH-2, html.EscapeString(e.Model), html.EscapeString(c), v)
			cls := "t"
			if op > 0.6 {
				cls = "ca"
			}
			d.text(cls, cx+cellW/2, cy+cellH/2+4, "middle", fmt.Sprintf("%.0f", v))
		}
	}
	return d.String()
}

func trackHeatmapSVG(es []Entry) string {
	seen := map[string]bool{}
	var tracks []string
	for _, e := range es {
		for t := range e.Score.ByTrack {
			if !seen[t] {
				seen[t] = true
				tracks = append(tracks, t)
			}
		}
	}
	sort.Strings(tracks)
	return heatmap("Score by track", "0-100 per question family; darker is better. A model strong overall can still be blind on one track.", es, tracks,
		func(e Entry, t string) (float64, bool) {
			ts, ok := e.Score.ByTrack[t]
			return ts.Score, ok
		})
}

func breakdownSVG(es []Entry) string {
	cols := []string{"verdict", "closeness", "trigger", "focus"}
	return heatmap("What each model gets right", "Share correct on each kind of decision (%). Verdict is the raw answer; trigger and focus are what production's rules did with it.", es, cols,
		func(e Entry, c string) (float64, bool) {
			s := e.Score
			switch c {
			case "verdict":
				return 100 * s.Verdict, true
			case "closeness":
				return 100 * s.Closeness, true
			case "trigger":
				return 100 * s.Trigger, true
			}
			return 100 * s.Focus, true
		})
}

// costSVG shows decision cost per 1,000 Oracle turns (one decision call each).
func costSVG(es []Entry, pinned string) string {
	type row struct {
		e    Entry
		per1 float64
	}
	var rows []row
	maxC := 0.0
	for _, e := range es {
		if e.Score.Calls == 0 {
			continue
		}
		c := e.Score.CostUSD / float64(e.Score.Calls) * 1000
		rows = append(rows, row{e, c})
		maxC = math.Max(maxC, c)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].per1 < rows[j].per1 })
	h := 80 + 30*len(rows)
	d := newSVG(760, h, "Cost per 1,000 Oracle turns", "Decision-call cost only (the Oracle runs one decision call before each turn).")
	const left, right = 150.0, 650.0
	for i, r := range rows {
		cy := 74.0 + 30*float64(i)
		cls := "bar"
		if r.e.Model == pinned {
			cls = "barpin"
		}
		d.text("t", left-10, cy+4, "end", shortName(r.e.Model))
		wd := 2.0
		if maxC > 0 {
			wd = math.Max(2, r.per1/maxC*(right-left))
		}
		fmt.Fprintf(&d.b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="12" rx="2"><title>%s</title></rect>`, cls, left, cy-6, wd, html.EscapeString(r.e.Model))
		d.text("t2", left+wd+6, cy+4, "start", fmt.Sprintf("$%.3f", r.per1))
	}
	return d.String()
}
