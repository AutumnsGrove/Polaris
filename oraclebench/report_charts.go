package oraclebench

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"time"
)

// The charts follow the launch-post convention the AI labs use: one featured
// model in the brand colour, everything else muted, the value printed on the
// bar, a plain table beside it. The palette is Polaris's own (warm near-black,
// amber star, cool slate) per PRODUCT.md's "night sky, not tech-neon".
//
// Each SVG paints its own dark card instead of following the viewer's colour
// scheme, so it reads the same on a light README, a dark one, or in a PR.
const (
	cBG     = "#1b1714"
	cPanel  = "#241f1b"
	cInk    = "#ece6dd"
	cDim    = "#9a9288"
	cGrid   = "#3a332c"
	cStar   = "#f2b84b" // amber: the featured (production) model
	cBar    = "#7d8da1" // slate: everyone else
	cBarLo  = "#4d596a"
	cDanger = "#d9705a"
)

// ReportCharts renders every chart for a leaderboard as name -> SVG. The
// scorecard lists every model, disqualified ones dimmed with their reason, so
// nothing vanishes; the comparison charts leave disqualified runs out, since
// their numbers describe a broken run rather than a model.
func ReportCharts(l Leaderboard, pinned string, shares map[string]CallStats) map[string]string {
	var ok []Entry
	for _, e := range l.Entries {
		if e.NA == "" && len(e.Score.Disqualified) == 0 {
			ok = append(ok, e)
		}
	}
	return map[string]string{
		"scorecard.svg":           scorecardSVG(l, pinned, shares),
		"overall-quality.svg":     overallSVG(ok, pinned),
		"metrics.svg":             metricsSVG(ok, pinned),
		"accuracy-vs-latency.svg": scatterSVG(ok, pinned),
		"tracks.svg":              trackTableSVG(ok, pinned),
		"breakdown.svg":           breakdownTableSVG(ok, pinned),
		"latency.svg":             latencySVG(ok, pinned),
		"cost.svg":                costSVG(ok, pinned),
		"cost-efficiency.svg":     efficiencySVG(ok, pinned),
		"refusals.svg":            refusalsSVG(l, shares),
	}
}

const svgFont = `-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif`

type svgDoc struct {
	b    strings.Builder
	w, h int
}

func newSVG(w, h int, title, subtitle string) *svgDoc {
	d := &svgDoc{w: w, h: h}
	fmt.Fprintf(&d.b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" role="img" aria-label="%s" font-family="%s">`, w, h, w, html.EscapeString(title), svgFont)
	fmt.Fprintf(&d.b, `<rect width="%d" height="%d" rx="14" fill="%s"/>`, w, h, cBG)
	d.text(24, 44, 22, 700, cInk, "start", title)
	if subtitle != "" {
		d.text(24, 68, 13, 400, cDim, "start", subtitle)
	}
	return d
}

func (d *svgDoc) text(x, y float64, size int, weight int, fill, anchor, s string) {
	fmt.Fprintf(&d.b, `<text x="%.1f" y="%.1f" font-size="%d" font-weight="%d" fill="%s" text-anchor="%s">%s</text>`, x, y, size, weight, fill, anchor, html.EscapeString(s))
}

func (d *svgDoc) rect(x, y, w, h, r float64, fill, title string) {
	fmt.Fprintf(&d.b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="%s">`, x, y, w, h, r, fill)
	if title != "" {
		fmt.Fprintf(&d.b, `<title>%s</title>`, html.EscapeString(title))
	}
	d.b.WriteString(`</rect>`)
}

func (d *svgDoc) line(x1, y1, x2, y2 float64, stroke string, w float64, dash string) {
	extra := ""
	if dash != "" {
		extra = fmt.Sprintf(` stroke-dasharray="%s"`, dash)
	}
	fmt.Fprintf(&d.b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f"%s/>`, x1, y1, x2, y2, stroke, w, extra)
}

func (d *svgDoc) footnote(s string) {
	d.text(24, float64(d.h-16), 11, 400, cDim, "start", s)
}

func (d *svgDoc) String() string { return d.b.String() + "</svg>\n" }

func shortName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func barColor(model, pinned string) string {
	if model == pinned {
		return cStar
	}
	return cBar
}

// scorecardSVG is the headline graphic: ranked bars on a fixed 0-100 axis (so
// near-ties look like near-ties, not a dramatic gap), with the facts that decide
// a winner beside each bar: speed, the share of calls inside production's
// timeout, and cost.
func scorecardSVG(l Leaderboard, pinned string, shares map[string]CallStats) string {
	const top, rowH = 112.0, 40.0
	n := len(l.Entries)
	d := newSVG(980, int(top+rowH*float64(n))+56, "Oracle decision models", fmt.Sprintf("%d hand-labeled cases, %d repeats each. Score is out of 100; whiskers are 95%% intervals.", l.Cases, l.Repeats))
	const barX, barW = 232.0, 300.0
	cols := []struct {
		x     float64
		label string
	}{{barX, "SCORE"}, {680, "MEDIAN"}, {778, "UNDER 2.5s"}, {868, "REFUSED"}, {956, "$ / 1K TURNS"}}
	for _, c := range cols {
		anchor := "start"
		if c.x > barX {
			anchor = "end"
		}
		d.text(c.x, top-16, 11, 600, cDim, anchor, c.label)
	}
	d.line(24, top-6, 956, top-6, cGrid, 1, "")
	for i, e := range l.Entries {
		y := top + rowH*float64(i)
		mid := y + rowH/2
		name := shortName(e.Model)
		nameFill, nameW := cInk, 500
		if e.Model == pinned {
			nameFill, nameW = cStar, 700
		}
		s := e.Score
		sh, haveStats := shares[e.Model]
		if e.NA != "" {
			d.text(24, mid+5, 15, nameW, cDim, "start", name)
			d.text(barX, mid+5, 13, 400, cDanger, "start", "not run: "+e.NA)
			d.line(24, y+rowH, 956, y+rowH, cGrid, 1, "")
			continue
		}
		disq := len(s.Disqualified) > 0
		if disq {
			// Keep the row's numbers: a disqualified model's refusal rate and
			// speed are exactly what the reader came to see.
			d.text(24, mid+5, 15, nameW, cDim, "start", name)
			d.text(barX, mid+5, 13, 400, cDanger, "start", "disqualified: "+strings.Join(s.Disqualified, "; "))
		} else {
			d.text(24, mid+5, 15, nameW, nameFill, "start", name)
			d.rect(barX, mid-9, barW, 18, 4, cPanel, "")
			d.rect(barX, mid-9, math.Max(3, barW*clamp(s.Headline, 0, 100)/100), 18, 4, barColor(e.Model, pinned), fmt.Sprintf("%s: %.1f (%.1f-%.1f)", e.Model, s.Headline, s.HeadlineLo, s.HeadlineHi))
			lo, hi := barX+barW*clamp(s.HeadlineLo, 0, 100)/100, barX+barW*clamp(s.HeadlineHi, 0, 100)/100
			d.line(lo, mid, hi, mid, cInk, 1.5, "")
			d.line(lo, mid-5, lo, mid+5, cInk, 1.5, "")
			d.line(hi, mid-5, hi, mid+5, cInk, 1.5, "")
			d.text(barX+barW+10, mid+5, 15, 700, cInk, "start", fmt.Sprintf("%.1f", s.Headline))
		}
		fill := cInk
		if disq {
			fill = cDim
		}
		d.text(680, mid+5, 14, 400, fill, "end", fmtDur(s.LatencyP50))
		under := "-"
		if haveStats && s.Calls > 0 {
			under = fmt.Sprintf("%.1f%%", 100*float64(sh.Under2_5s)/float64(s.Calls))
		}
		d.text(778, mid+5, 14, 400, fill, "end", under)
		refFill := fill
		if s.RefusalRate > 0.005 {
			refFill = cDanger
		}
		d.text(868, mid+5, 14, 400, refFill, "end", fmt.Sprintf("%.1f%%", 100*s.RefusalRate))
		per1k := 0.0
		if s.Calls > 0 {
			per1k = s.CostUSD / float64(s.Calls) * 1000
		}
		d.text(956, mid+5, 14, 400, fill, "end", fmt.Sprintf("$%.2f", per1k))
		d.line(24, y+rowH, 956, y+rowH, cGrid, 1, "")
	}
	d.footnote("Gaps under 2.5 points are ties. Amber is the model pinned in production. Under 2.5s = share of calls inside Oracle's real timeout. Refused = calls the model declined to answer.")
	return d.String()
}

// scatterSVG plots accuracy against median latency on a log axis, with the
// production timeout marked. Up and left is better.
func scatterSVG(es []Entry, pinned string) string {
	d := newSVG(920, 620, "Accuracy vs. speed", "Score against median latency, log scale. Up and to the left is better.")
	const left, right, top, bottom = 64.0, 880.0, 100.0, 526.0
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
		d.line(left, y(v), right, y(v), cGrid, 1, "")
		d.text(left-10, y(v)+4, 12, 400, cDim, "end", fmt.Sprintf("%.0f", v))
	}
	for _, ms := range []float64{100, 200, 500, 1000, 2000, 5000} {
		d.text(x(ms), bottom+22, 12, 400, cDim, "middle", fmtDur(time.Duration(ms)*time.Millisecond))
	}
	d.text((left+right)/2, bottom+46, 12, 400, cDim, "middle", "median latency")
	px := x(float64(ProductionTimeout.Milliseconds()))
	d.line(px, top, px, bottom, cStar, 1.5, "5 4")
	d.text(px-6, top+14, 12, 600, cStar, "end", "Oracle's real timeout: 2.5s")
	bx := x(5000)
	d.line(bx, top, bx, bottom, cDim, 1, "5 4")
	d.text(bx-6, top+14, 12, 400, cDim, "end", "bench budget 5s")

	lab := newLabeler()
	for _, e := range es {
		lab.avoid(x(float64(e.Score.LatencyP50.Milliseconds())), y(e.Score.Headline))
	}
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.LatencyP50 < rows[j].Score.LatencyP50 })
	for _, e := range rows {
		s := e.Score
		cx, cy := x(float64(s.LatencyP50.Milliseconds())), y(s.Headline)
		col := barColor(e.Model, pinned)
		d.line(cx, y(s.HeadlineLo), cx, y(s.HeadlineHi), col, 2, "")
		fmt.Fprintf(&d.b, `<circle cx="%.1f" cy="%.1f" r="7" fill="%s" stroke="%s" stroke-width="2"><title>%s: %.1f (%.1f-%.1f), p50 %s</title></circle>`,
			cx, cy, col, cBG, html.EscapeString(e.Model), s.Headline, s.HeadlineLo, s.HeadlineHi, fmtDur(s.LatencyP50))
		lab.place(d, cx, cy, shortName(e.Model), e.Model == pinned, func(ox, oy float64) bool {
			for _, o := range es {
				if o.Model != e.Model {
					if px := x(float64(o.Score.LatencyP50.Milliseconds())); px > cx && px-cx < 48 && math.Abs(y(o.Score.Headline)-oy) < 20 {
						return true
					}
				}
			}
			return false
		})
	}
	d.footnote("Vertical lines are 95% intervals. Amber is the model pinned in production.")
	return d.String()
}

// tableSVG draws a launch-post style table: one row per label, one column per
// model, plain numbers, the best in each row picked out. Colour never carries
// meaning alone: the winner is also bold.
func tableSVG(title, subtitle, footnote string, es []Entry, pinned string, rows []string, val func(Entry, string) (float64, bool)) string {
	const left, top, rowH = 170.0, 190.0, 32.0
	colW := math.Min(96, (920-left-24)/float64(max(len(es), 1)))
	w := 920
	h := int(top + rowH*float64(len(rows)) + 48)
	d := newSVG(w, h, title, subtitle)
	for ci, e := range es {
		cx := left + colW*float64(ci) + colW/2
		fill, wt := cDim, 500
		if e.Model == pinned {
			fill, wt = cStar, 700
		}
		fmt.Fprintf(&d.b, `<text transform="translate(%.1f %.1f) rotate(-40)" font-size="12" font-weight="%d" fill="%s">%s</text>`, cx-2, top-14, wt, fill, html.EscapeString(shortName(e.Model)))
	}
	d.line(24, top-4, left+colW*float64(len(es)), top-4, cGrid, 1, "")
	for ri, r := range rows {
		y := top + rowH*float64(ri)
		mid := y + rowH/2 + 5
		best := math.Inf(-1)
		for _, e := range es {
			if v, ok := val(e, r); ok {
				best = math.Max(best, v)
			}
		}
		d.text(24, mid, 14, 500, cInk, "start", r)
		for ci, e := range es {
			cx := left + colW*float64(ci) + colW/2
			v, ok := val(e, r)
			if !ok {
				d.text(cx, mid, 14, 400, cDim, "middle", "-")
				continue
			}
			if math.Round(v) >= math.Round(best) {
				d.rect(cx-24, y+4, 48, rowH-8, 6, cPanel, "")
				d.text(cx, mid, 14, 700, cStar, "middle", fmt.Sprintf("%.0f", v))
			} else {
				d.text(cx, mid, 14, 400, cInk, "middle", fmt.Sprintf("%.0f", v))
			}
		}
		d.line(24, y+rowH, left+colW*float64(len(es)), y+rowH, cGrid, 1, "")
	}
	d.footnote(footnote)
	return d.String()
}

func trackTableSVG(es []Entry, pinned string) string {
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
	rows := append([]string{"Overall"}, tracks...)
	return tableSVG("Score by question family", "Each row is a family of Oracle questions, scored 0-100. The best in a row is boxed.",
		"A model strong overall can still be weak on one family. A 25-point dip below a model's own score is flagged in the run's text report.", es, pinned, rows,
		func(e Entry, r string) (float64, bool) {
			if r == "Overall" {
				return e.Score.Headline, true
			}
			ts, ok := e.Score.ByTrack[r]
			return ts.Score, ok
		})
}

func breakdownTableSVG(es []Entry, pinned string) string {
	rows := []string{"Verdict", "Closeness", "Trigger", "Focus"}
	return tableSVG("What each model gets right", "Share correct on each kind of decision, in percent. The best in a row is boxed.",
		"Verdict is the raw answer. Trigger and focus are what production's own rules did with it, which is what a user actually feels.", es, pinned, rows,
		func(e Entry, r string) (float64, bool) {
			s := e.Score
			switch r {
			case "Verdict":
				return 100 * s.Verdict, true
			case "Closeness":
				return 100 * s.Closeness, true
			case "Trigger":
				return 100 * s.Trigger, true
			}
			return 100 * s.Focus, true
		})
}

// barChart draws one row per model with a bar, an end label, and optional
// whisker: shared by the latency and cost charts.
func barChart(title, subtitle, footnote string, rows []Entry, pinned string, axisMax float64, ticks []float64, tickLabel func(float64) string, value func(Entry) float64, whisker func(Entry) float64, label func(Entry) string, mark float64, markLabel string) string {
	const left, right, top, rowH = 200.0, 760.0, 112.0, 38.0
	h := int(top+rowH*float64(len(rows))) + 70
	d := newSVG(920, h, title, subtitle)
	x := func(v float64) float64 { return left + clamp(v, 0, axisMax)/axisMax*(right-left) }
	bottom := top + rowH*float64(len(rows))
	for _, t := range ticks {
		if t > axisMax {
			continue
		}
		d.line(x(t), top-8, x(t), bottom, cGrid, 1, "")
		d.text(x(t), bottom+20, 12, 400, cDim, "middle", tickLabel(t))
	}
	if mark > 0 {
		d.line(x(mark), top-8, x(mark), bottom, cStar, 1.5, "5 4")
		d.text(x(mark)+6, top-14, 12, 600, cStar, "start", markLabel)
	}
	for i, e := range rows {
		mid := top + rowH*float64(i) + rowH/2
		fill, wt := cInk, 500
		if e.Model == pinned {
			fill, wt = cStar, 700
		}
		d.text(left-14, mid+5, 14, wt, fill, "end", shortName(e.Model))
		d.rect(left, mid-9, math.Max(3, x(value(e))-left), 18, 4, barColor(e.Model, pinned), e.Model)
		end := x(value(e))
		if whisker != nil {
			wx := x(whisker(e))
			d.line(end, mid, wx, mid, cInk, 1.5, "")
			d.line(wx, mid-5, wx, mid+5, cInk, 1.5, "")
			end = wx
		}
		d.text(end+10, mid+5, 13, 600, cInk, "start", label(e))
	}
	d.footnote(footnote)
	return d.String()
}

func latencySVG(es []Entry, pinned string) string {
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.LatencyP50 < rows[j].Score.LatencyP50 })
	maxMS := 3000.0
	for _, e := range rows {
		maxMS = math.Max(maxMS, float64(e.Score.LatencyP95.Milliseconds())*1.35)
	}
	return barChart("Latency", "Median per call (bar) and 95th percentile (whisker), fastest first.",
		"Measured at concurrency 2 on one day; latency moves with provider load. Oracle drops the decision after 2.5s.", rows, pinned, maxMS,
		[]float64{0, 500, 1000, 1500, 2000, 2500, 3000, 4000, 5000},
		func(v float64) string {
			if v == 0 {
				return "0"
			}
			return fmtDur(time.Duration(v) * time.Millisecond)
		},
		func(e Entry) float64 { return float64(e.Score.LatencyP50.Milliseconds()) },
		func(e Entry) float64 { return float64(e.Score.LatencyP95.Milliseconds()) },
		func(e Entry) string {
			return fmt.Sprintf("%s  (p95 %s)", fmtDur(e.Score.LatencyP50), fmtDur(e.Score.LatencyP95))
		},
		float64(ProductionTimeout.Milliseconds()), "Oracle's real timeout")
}

func costSVG(es []Entry, pinned string) string {
	per1k := func(e Entry) float64 {
		if e.Score.Calls == 0 {
			return 0
		}
		return e.Score.CostUSD / float64(e.Score.Calls) * 1000
	}
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return per1k(rows[i]) < per1k(rows[j]) })
	maxC := 0.0
	for _, e := range rows {
		maxC = math.Max(maxC, per1k(e))
	}
	maxC = math.Max(maxC*1.25, 0.01)
	step := math.Pow(10, math.Floor(math.Log10(maxC)))
	var ticks []float64
	for t := 0.0; t <= maxC; t += step {
		ticks = append(ticks, t)
	}
	return barChart("Cost per 1,000 Oracle turns", "Decision-call cost only: Oracle makes one decision call before each turn.",
		"Excludes the benchmark's judge model. Prices are OpenRouter's at the time of the run.", rows, pinned, maxC, ticks,
		func(v float64) string { return fmt.Sprintf("$%.2f", v) },
		per1k, nil,
		func(e Entry) string { return fmt.Sprintf("$%.3f", per1k(e)) }, 0, "")
}

// overallSVG is the single-number view: the headline quality score, ranked, with
// its 95% interval. It is deliberately latency-blind (the scorecard and scatter
// bring speed back in), so "best answers" and "best answers fast enough" stay
// separate questions.
func overallSVG(es []Entry, pinned string) string {
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.Headline > rows[j].Score.Headline })
	return barChart("Overall quality", "Headline score out of 100. Speed is not counted here.",
		"Score combines raw answer accuracy with whether production's rules delivered the right nudge. Whiskers are 95% intervals; gaps under 2.5 points are ties.", rows, pinned, 100,
		[]float64{0, 20, 40, 60, 80, 100},
		func(v float64) string { return fmt.Sprintf("%.0f", v) },
		func(e Entry) float64 { return e.Score.Headline },
		func(e Entry) float64 { return e.Score.HeadlineHi },
		func(e Entry) string {
			return fmt.Sprintf("%.1f  (%.1f-%.1f)", e.Score.Headline, e.Score.HeadlineLo, e.Score.HeadlineHi)
		}, 0, "")
}

// metricsSVG is a grid of small ranked-bar panels, one per axis the leaderboard
// records, each sorted best first. Panels whose lower numbers are better say so
// in their title, since a bar chart otherwise reads longer as better.
func metricsSVG(es []Entry, pinned string) string {
	type metric struct {
		title, note string
		lowerBetter bool
		val         func(Score ModelScore) float64
		format      func(float64) string
		max         float64 // fixed axis max; 0 = scale to the data
	}
	pct := func(v float64) string { return fmt.Sprintf("%.0f%%", v) }
	pct1 := func(v float64) string { return fmt.Sprintf("%.1f%%", v) }
	metrics := []metric{
		{"Raw answer accuracy", "share of verdicts correct", false, func(s ModelScore) float64 { return 100 * s.Verdict }, pct, 100},
		{"Closeness", "how near the probabilities sit to the label", false, func(s ModelScore) float64 { return 100 * s.Closeness }, pct, 100},
		{"Trigger decisions", "right call on whether to nudge", false, func(s ModelScore) float64 { return 100 * s.Trigger }, pct, 100},
		{"Focus actions", "right call on which action to take", false, func(s ModelScore) float64 { return 100 * s.Focus }, pct, 100},
		{"Contested cases", "score on the disputed-label cases", false, func(s ModelScore) float64 { return s.DisputedHL }, func(v float64) string { return fmt.Sprintf("%.0f", v) }, 100},
		{"Calibration (Brier)", "lower is better", true, func(s ModelScore) float64 { return s.Brier }, func(v float64) string { return fmt.Sprintf("%.3f", v) }, 0},
		{"Dangerous false nudges", "lower is better", true, func(s ModelScore) float64 { return 100 * s.DangerousFPRate }, pct1, 0},
		{"Errors and refusals", "lower is better", true, func(s ModelScore) float64 { return 100 * s.ErrorRate }, pct1, 0},
	}
	const cols, panelW, panelGap, top = 2, 420.0, 20.0, 100.0
	rowH := 24.0
	panelH := 64 + rowH*float64(len(es)) + 16
	nRows := (len(metrics) + cols - 1) / cols
	h := int(top+panelH*float64(nRows)) + 40
	d := newSVG(920, h, "More ways to compare", "Each panel ranks the models on one axis, best first.")
	for mi, m := range metrics {
		px := 24 + float64(mi%cols)*(panelW+panelGap)
		py := top + float64(mi/cols)*panelH
		d.rect(px, py, panelW, panelH-16, 10, cPanel, "")
		d.text(px+16, py+26, 15, 700, cInk, "start", m.title)
		d.text(px+16, py+44, 11, 400, cDim, "start", m.note)
		rows := append([]Entry(nil), es...)
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := m.val(rows[i].Score), m.val(rows[j].Score)
			if m.lowerBetter {
				return a < b
			}
			return a > b
		})
		axis := m.max
		if axis == 0 {
			for _, e := range rows {
				axis = math.Max(axis, m.val(e.Score))
			}
			axis = math.Max(axis*1.15, 1e-9)
		}
		const nameW, barMax = 128.0, 190.0
		for i, e := range rows {
			mid := py + 64 + rowH*float64(i) + rowH/2 - 4
			fill, wt := cInk, 400
			if e.Model == pinned {
				fill, wt = cStar, 700
			}
			d.text(px+16+nameW, mid+4, 12, wt, fill, "end", shortName(e.Model))
			v := m.val(e.Score)
			d.rect(px+24+nameW, mid-6, math.Max(2, barMax*clamp(v, 0, axis)/axis), 12, 3, barColor(e.Model, pinned), e.Model)
			d.text(px+24+nameW+math.Max(2, barMax*clamp(v, 0, axis)/axis)+8, mid+4, 12, 600, cInk, "start", m.format(v))
		}
	}
	d.footnote("Trigger and focus score what production's rules did with the answer. Dangerous false nudges are nudges fired where one would do harm.")
	return d.String()
}

// labeler places point labels without overlap: each label tries six spots around
// its point and takes the first that is clear of the labels already placed.
// Near-identical models (the two Jev versions, 1.13 and 1.14) would otherwise print on top of
// each other.
type labeler struct {
	placed []struct{ x0, x1, y0, y1 float64 }
}

func newLabeler() *labeler { return &labeler{} }

// avoid reserves the space around a plotted point so no label is drawn over it.
func (l *labeler) avoid(cx, cy float64) {
	l.placed = append(l.placed, struct{ x0, x1, y0, y1 float64 }{cx - 9, cx + 9, cy - 9, cy + 9})
}

// place draws name beside (cx, cy). crowdedRight reports a close neighbour on the
// point's right, in which case the label goes left so the text sits beside the
// point it names rather than the neighbour's.
func (l *labeler) place(d *svgDoc, cx, cy float64, name string, bold bool, crowdedRight func(cx, cy float64) bool) {
	wd := 7.6 * float64(len(name))
	// Near spots first; the far ones (with a leader line back to the point) only
	// matter in a dense cluster like the fast Jev/D1/Microsoft group.
	spots := []struct {
		dx, dy float64
		anchor string
	}{{12, 5, "start"}, {12, -10, "start"}, {-12, 5, "end"}, {-12, -10, "end"}, {12, 20, "start"}, {-12, 20, "end"},
		{26, -30, "start"}, {-26, -30, "end"}, {26, 36, "start"}, {-26, 36, "end"}, {26, -52, "start"}, {-26, -52, "end"}, {26, 56, "start"}, {-26, 56, "end"}}
	if crowdedRight(cx, cy) {
		for i := 0; i+1 < 6; i += 2 {
			spots[i], spots[i+1] = spots[i+1], spots[i]
		}
		spots[0], spots[2], spots[1], spots[3] = spots[2], spots[0], spots[3], spots[1]
	}
	for si, sp := range spots {
		b := struct{ x0, x1, y0, y1 float64 }{cx + sp.dx, cx + sp.dx + wd, cy + sp.dy - 12, cy + sp.dy + 4}
		if sp.anchor == "end" {
			b.x0, b.x1 = cx+sp.dx-wd, cx+sp.dx
		}
		clash := false
		for _, o := range l.placed {
			if b.x0 < o.x1 && b.x1 > o.x0 && b.y0 < o.y1 && b.y1 > o.y0 {
				clash = true
			}
		}
		if clash && si != len(spots)-1 {
			continue
		}
		l.placed = append(l.placed, b)
		if math.Abs(sp.dx) > 14 || math.Abs(sp.dy) > 22 {
			ex := b.x0
			if sp.anchor == "end" {
				ex = b.x1
			}
			d.line(cx, cy, ex, cy+sp.dy-4, cDim, 1, "")
		}
		d.rect(b.x0-3, b.y0, b.x1-b.x0+6, b.y1-b.y0+2, 3, cBG, "")
		w := 500
		if bold {
			w = 700
		}
		d.text(cx+sp.dx, cy+sp.dy, 13, w, cInk, sp.anchor, name)
		return
	}
}

// efficiencySVG plots quality against cost per 1,000 turns on a log axis and
// draws the efficient frontier: the models no other model beats on both axes.
// A model off the frontier has a cheaper-and-better alternative.
func efficiencySVG(es []Entry, pinned string) string {
	per1k := func(e Entry) float64 {
		if e.Score.Calls == 0 {
			return 0
		}
		return e.Score.CostUSD / float64(e.Score.Calls) * 1000
	}
	d := newSVG(920, 620, "Cost efficiency", "Score against decision cost per 1,000 Oracle turns, log scale. Up and to the left is better.")
	const left, right, top, bottom = 64.0, 880.0, 100.0, 526.0
	minC, maxC := math.Inf(1), 0.0
	lo, hi := 100.0, 0.0
	for _, e := range es {
		if c := per1k(e); c > 0 {
			minC, maxC = math.Min(minC, c), math.Max(maxC, c)
		}
		lo, hi = math.Min(lo, e.Score.HeadlineLo), math.Max(hi, e.Score.HeadlineHi)
	}
	if maxC == 0 {
		return d.String()
	}
	minC, maxC = minC/1.6, maxC*1.6
	lo, hi = math.Floor(lo/5)*5-5, math.Min(100, math.Ceil(hi/5)*5)
	x := func(c float64) float64 {
		return left + (math.Log(clamp(c, minC, maxC))-math.Log(minC))/(math.Log(maxC)-math.Log(minC))*(right-left)
	}
	y := func(v float64) float64 { return bottom - (clamp(v, lo, hi)-lo)/(hi-lo)*(bottom-top) }
	for v := math.Ceil(lo/5) * 5; v <= hi; v += 5 {
		d.line(left, y(v), right, y(v), cGrid, 1, "")
		d.text(left-10, y(v)+4, 12, 400, cDim, "end", fmt.Sprintf("%.0f", v))
	}
	lastTick := math.Inf(-1)
	for _, t := range []float64{0.01, 0.02, 0.03, 0.05, 0.07, 0.1, 0.15, 0.2, 0.3, 0.5, 0.7, 1, 2, 5} {
		if t >= minC && t <= maxC && x(t)-lastTick >= 52 {
			d.text(x(t), bottom+22, 12, 400, cDim, "middle", fmt.Sprintf("$%g", t))
			lastTick = x(t)
		}
	}
	d.text((left+right)/2, bottom+46, 12, 400, cDim, "middle", "cost per 1,000 turns")
	// A log axis has no zero: free models sit on the left edge, labelled as such.
	for _, e := range es {
		if per1k(e) == 0 {
			d.text(left, bottom+22, 12, 600, cStar, "middle", "free")
			break
		}
	}

	// Frontier: walk models from cheapest up, keeping each that beats every
	// cheaper one on score.
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return per1k(rows[i]) < per1k(rows[j]) })
	var front []Entry
	best := math.Inf(-1)
	for _, e := range rows {
		if e.Score.Headline > best {
			front = append(front, e)
			best = e.Score.Headline
		}
	}
	if len(front) > 1 {
		var pts []string
		for _, e := range front {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(per1k(e)), y(e.Score.Headline)))
		}
		fmt.Fprintf(&d.b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-dasharray="5 4"/>`, strings.Join(pts, " "), cStar)
		d.text(right, top+14, 12, 600, cStar, "end", "dashed line: the efficient frontier")
	}
	lab := newLabeler()
	for _, e := range es {
		lab.avoid(x(per1k(e)), y(e.Score.Headline))
	}
	for _, e := range rows {
		s := e.Score
		cx, cy := x(per1k(e)), y(s.Headline)
		col := barColor(e.Model, pinned)
		d.line(cx, y(s.HeadlineLo), cx, y(s.HeadlineHi), col, 2, "")
		fmt.Fprintf(&d.b, `<circle cx="%.1f" cy="%.1f" r="7" fill="%s" stroke="%s" stroke-width="2"><title>%s: %.1f at $%.3f per 1,000 turns</title></circle>`,
			cx, cy, col, cBG, html.EscapeString(e.Model), s.Headline, per1k(e))
		lab.place(d, cx, cy, shortName(e.Model), e.Model == pinned, func(ox, oy float64) bool {
			for _, o := range es {
				if o.Model != e.Model {
					if px := x(per1k(o)); px > cx && px-cx < 48 && math.Abs(y(o.Score.Headline)-oy) < 20 {
						return true
					}
				}
			}
			return false
		})
	}
	d.footnote("Vertical lines are 95% intervals. A model off the dashed frontier has another that is both cheaper and better.")
	return d.String()
}

// refusalsSVG tracks the calls a model declined to answer. A refusal can't run
// Oracle (the turn proceeds with Oracle off), so it is recorded as a result and
// never retried. Top: refusal rate per model. Below: for each model that refused
// anything, the question families it refused on, since sensitive-topic checks
// (medical, private people) are exactly where a general chat model's safety
// training and an Oracle classifier's job collide.
func refusalsSVG(l Leaderboard, shares map[string]CallStats) string {
	var rows []Entry
	for _, e := range l.Entries {
		if e.NA == "" {
			rows = append(rows, e)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.RefusalRate > rows[j].Score.RefusalRate })

	const left, right, top, rowH = 200.0, 700.0, 112.0, 34.0
	// Models with refusals get a per-track breakdown underneath.
	var refusers []Entry
	tracks := map[string]bool{}
	for _, e := range rows {
		if sh := shares[e.Model]; sh.Refused > 0 {
			refusers = append(refusers, e)
			for t := range sh.RefusedByTrack {
				tracks[t] = true
			}
		}
	}
	var trackList []string
	for t := range tracks {
		trackList = append(trackList, t)
	}
	// Worst track first, by refusals summed over models; ties alphabetical.
	sum := func(t string) int {
		n := 0
		for _, e := range refusers {
			n += shares[e.Model].RefusedByTrack[t]
		}
		return n
	}
	sort.SliceStable(trackList, func(i, j int) bool {
		if a, b := sum(trackList[i]), sum(trackList[j]); a != b {
			return a > b
		}
		return trackList[i] < trackList[j]
	})
	if len(trackList) > 12 {
		trackList = trackList[:12]
	}

	barsH := top + rowH*float64(len(rows))
	tableTop := barsH + 70
	h := int(barsH) + 60
	if len(trackList) > 0 {
		h = int(tableTop) + 24*len(trackList) + 80
	}
	d := newSVG(920, h, "Refusals", "Calls a model declined to answer. A refusal turns Oracle off for that turn, so it is counted, never retried.")
	maxR := 0.05
	for _, e := range rows {
		maxR = math.Max(maxR, e.Score.RefusalRate)
	}
	x := func(v float64) float64 { return left + clamp(v, 0, maxR)/maxR*(right-left) }
	d.line(x(0.05), top-8, x(0.05), barsH, cStar, 1.5, "5 4")
	d.text(x(0.05)+6, top-14, 12, 600, cStar, "start", "5%: disqualifying")
	for i, e := range rows {
		mid := top + rowH*float64(i) + rowH/2
		d.text(left-14, mid+5, 14, 500, cInk, "end", shortName(e.Model))
		r := e.Score.RefusalRate
		fill := cBar
		if r > 0.05 {
			fill = cDanger
		}
		d.rect(left, mid-9, math.Max(3, x(r)-left), 18, 4, fill, e.Model)
		label := fmt.Sprintf("%.1f%%", 100*r)
		if sh, ok := shares[e.Model]; ok && sh.Refused > 0 {
			label = fmt.Sprintf("%.1f%%  (%d of %d calls)", 100*r, sh.Refused, sh.Calls)
		}
		d.text(x(r)+10, mid+5, 13, 600, cInk, "start", label)
	}
	if len(trackList) > 0 {
		d.text(24, tableTop-22, 15, 700, cInk, "start", "Where the refusals landed")
		d.text(24, tableTop-4, 11, 400, cDim, "start", "Share of each question family's calls that were refused.")
		const lw, cw = 190.0, 130.0
		for ci, e := range refusers {
			d.text(lw+cw*float64(ci)+cw/2, tableTop+18, 12, 600, cDim, "middle", shortName(e.Model))
		}
		for ri, t := range trackList {
			y := tableTop + 30 + 24*float64(ri)
			d.text(24, y+16, 13, 500, cInk, "start", t)
			for ci, e := range refusers {
				sh := shares[e.Model]
				cx := lw + cw*float64(ci) + cw/2
				tot := sh.CallsByTrack[t]
				if tot == 0 {
					d.text(cx, y+16, 13, 400, cDim, "middle", "-")
					continue
				}
				v := float64(sh.RefusedByTrack[t]) / float64(tot)
				if v == 0 {
					d.text(cx, y+16, 13, 400, cDim, "middle", "0%")
					continue
				}
				d.rect(cx-30, y+1, 60, 20, 4, cPanel, "")
				d.text(cx, y+16, 13, 700, cDanger, "middle", fmt.Sprintf("%.0f%%", 100*v))
			}
		}
	}
	d.footnote("Refusal is classified from the provider's own \"refused to answer\" response. Timeouts and other errors are not counted here.")
	return d.String()
}
