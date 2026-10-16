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
func ReportCharts(l Leaderboard, pinned string, shares map[string]LatencyShare) map[string]string {
	var ok []Entry
	for _, e := range l.Entries {
		if e.NA == "" && len(e.Score.Disqualified) == 0 {
			ok = append(ok, e)
		}
	}
	return map[string]string{
		"scorecard.svg":           scorecardSVG(l, pinned, shares),
		"accuracy-vs-latency.svg": scatterSVG(ok, pinned),
		"tracks.svg":              trackTableSVG(ok, pinned),
		"breakdown.svg":           breakdownTableSVG(ok, pinned),
		"latency.svg":             latencySVG(ok, pinned),
		"cost.svg":                costSVG(ok, pinned),
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
func scorecardSVG(l Leaderboard, pinned string, shares map[string]LatencyShare) string {
	const top, rowH = 112.0, 40.0
	n := len(l.Entries)
	d := newSVG(920, int(top+rowH*float64(n))+56, "Oracle decision models", fmt.Sprintf("%d hand-labeled cases, %d repeats each. Score is out of 100; whiskers are 95%% intervals.", l.Cases, l.Repeats))
	const barX, barW = 232.0, 300.0
	for _, c := range []struct {
		x     float64
		label string
	}{{barX, "SCORE"}, {640, "MEDIAN"}, {728, "UNDER 2.5s"}, {836, "$ / 1K TURNS"}} {
		anchor := "start"
		if c.x > barX {
			anchor = "end"
		}
		d.text(c.x, top-16, 11, 600, cDim, anchor, c.label)
	}
	d.line(24, top-6, 896, top-6, cGrid, 1, "")
	for i, e := range l.Entries {
		y := top + rowH*float64(i)
		mid := y + rowH/2
		name := shortName(e.Model)
		nameFill, nameW := cInk, 500
		if e.Model == pinned {
			nameFill, nameW = cStar, 700
		}
		s := e.Score
		if e.NA != "" || len(s.Disqualified) > 0 {
			reason := e.NA
			if reason == "" {
				reason = strings.Join(s.Disqualified, "; ")
			}
			d.text(24, mid+5, 15, nameW, cDim, "start", name)
			d.text(barX, mid+5, 13, 400, cDanger, "start", "disqualified: "+reason)
			d.line(24, y+rowH, 896, y+rowH, cGrid, 1, "")
			continue
		}
		d.text(24, mid+5, 15, nameW, nameFill, "start", name)
		d.rect(barX, mid-9, barW, 18, 4, cPanel, "")
		d.rect(barX, mid-9, math.Max(3, barW*clamp(s.Headline, 0, 100)/100), 18, 4, barColor(e.Model, pinned), fmt.Sprintf("%s: %.1f (%.1f-%.1f)", e.Model, s.Headline, s.HeadlineLo, s.HeadlineHi))
		lo, hi := barX+barW*clamp(s.HeadlineLo, 0, 100)/100, barX+barW*clamp(s.HeadlineHi, 0, 100)/100
		d.line(lo, mid, hi, mid, cInk, 1.5, "")
		d.line(lo, mid-5, lo, mid+5, cInk, 1.5, "")
		d.line(hi, mid-5, hi, mid+5, cInk, 1.5, "")
		d.text(barX+barW+10, mid+5, 15, 700, cInk, "start", fmt.Sprintf("%.1f", s.Headline))
		d.text(640, mid+5, 14, 400, cInk, "end", fmtDur(s.LatencyP50))
		under := "-"
		if sh, ok := shares[e.Model]; ok && s.Calls > 0 {
			under = fmt.Sprintf("%.1f%%", 100*float64(sh.Under2_5s)/float64(s.Calls))
		}
		d.text(728, mid+5, 14, 400, cInk, "end", under)
		per1k := 0.0
		if s.Calls > 0 {
			per1k = s.CostUSD / float64(s.Calls) * 1000
		}
		d.text(836, mid+5, 14, 400, cInk, "end", fmt.Sprintf("$%.2f", per1k))
		d.line(24, y+rowH, 896, y+rowH, cGrid, 1, "")
	}
	d.footnote("Gaps under 2.5 points are ties. Amber is the model pinned in production. Under 2.5s = share of calls inside Oracle's real timeout.")
	return d.String()
}

// scatterSVG plots accuracy against median latency on a log axis, with the
// production timeout marked. Up and left is better.
func scatterSVG(es []Entry, pinned string) string {
	d := newSVG(920, 524, "Accuracy vs. speed", "Score against median latency, log scale. Up and to the left is better.")
	const left, right, top, bottom = 64.0, 880.0, 100.0, 430.0
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

	// Labels try six spots around their point and take the first that doesn't
	// overlap one already placed; near-identical models (the two Jev versions)
	// would otherwise print on top of each other.
	type box struct{ x0, x1, y0, y1 float64 }
	var placed []box
	rows := append([]Entry(nil), es...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score.LatencyP50 < rows[j].Score.LatencyP50 })
	for _, e := range rows {
		s := e.Score
		cx, cy := x(float64(s.LatencyP50.Milliseconds())), y(s.Headline)
		col := barColor(e.Model, pinned)
		d.line(cx, y(s.HeadlineLo), cx, y(s.HeadlineHi), col, 2, "")
		fmt.Fprintf(&d.b, `<circle cx="%.1f" cy="%.1f" r="7" fill="%s" stroke="%s" stroke-width="2"><title>%s: %.1f (%.1f-%.1f), p50 %s</title></circle>`,
			cx, cy, col, cBG, html.EscapeString(e.Model), s.Headline, s.HeadlineLo, s.HeadlineHi, fmtDur(s.LatencyP50))
		name := shortName(e.Model)
		wd := 7.6 * float64(len(name))
		spots := []struct {
			dx, dy float64
			anchor string
		}{{12, 5, "start"}, {12, -10, "start"}, {-12, 5, "end"}, {-12, -10, "end"}, {12, 20, "start"}, {-12, 20, "end"}}
		// A point with a close neighbour on its right labels on the left, so the
		// text sits beside the point it names rather than the neighbour's.
		for _, o := range es {
			if ox := x(float64(o.Score.LatencyP50.Milliseconds())); o.Model != e.Model && ox > cx && ox-cx < 24 && math.Abs(y(o.Score.Headline)-cy) < 20 {
				spots[0], spots[2], spots[1], spots[3] = spots[2], spots[0], spots[3], spots[1]
				break
			}
		}
		for si, sp := range spots {
			b := box{cx + sp.dx, cx + sp.dx + wd, cy + sp.dy - 12, cy + sp.dy + 4}
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
				w := 500
				if e.Model == pinned {
					w = 700
				}
				d.text(cx+sp.dx, cy+sp.dy, 13, w, cInk, sp.anchor, name)
				break
			}
		}
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
