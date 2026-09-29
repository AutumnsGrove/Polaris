package tools

// ChartSpec is a structured chart a tool wants rendered instead of (or
// alongside) its prose answer — attached deterministically by a tool
// whose own response is already a time series (weather.go's "range"
// kind). The model-facing visualize tool that used to build one of
// these directly (Tier 2, per docs/plans/visualize-and-image-search.md)
// was removed once code_exec's general-purpose matplotlib plotting made
// it redundant — see issue #44. "range" survives because it's Tier 1
// (deterministic, no tool call, no code_exec/Docker dependency), not a
// model decision.
type ChartSpec struct {
	// Kind is always "range" today — weather.go's setWeatherChart is the
	// only caller left. "line"/"bar"/"timeline"/"meter" were the removed
	// visualize tool's own kinds (see this struct's doc comment); Series/
	// Events/Value below are now only ever populated the "range"/(unused)
	// way, kept as-is rather than collapsed since a future Tier-1 tool
	// could plausibly want "line" or "bar" the same deterministic way
	// weather wants "range".
	Kind   string        `json:"kind"`
	Title  string        `json:"title"`
	XLabel string        `json:"x_label,omitempty"`
	YLabel string        `json:"y_label,omitempty"`
	Series []ChartSeries `json:"series,omitempty"` // range
	Events []ChartEvent  `json:"events,omitempty"` // unused since visualize's removal
	Value  *ChartValue   `json:"value,omitempty"`  // unused since visualize's removal
	// Icons is "range"'s own field — one icon key per row, same order/
	// count as Series[0]'s points. Only ever set by weather.go's
	// setWeatherChart, from Open-Meteo's WMO weather code (see
	// weatherCodeIcon) — a fixed, small vocabulary the frontend maps to a
	// Lucide icon component (see ChartCard.svelte's iconFor). Not a
	// generic field the model can populate via visualize; "range" itself
	// is already Tier-1-only, never in visualize's kind enum.
	Icons []string `json:"icons,omitempty"` // range
}

type ChartSeries struct {
	Label  string       `json:"label"`
	Points []ChartPoint `json:"points"`
}

type ChartPoint struct {
	X interface{} `json:"x"` // string (date/category) or number
	Y float64     `json:"y"`
}

// ChartEvent is "timeline"'s own shape — deliberately not a ChartPoint. A
// timeline has no numeric value to put in Y; wedging it into ChartPoint
// with a mandatory-but-meaningless Y was the first draft's mistake.
type ChartEvent struct {
	Date  string `json:"date"`
	Label string `json:"label"`
}

type ChartValue struct {
	Current float64 `json:"current"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Label   string  `json:"label"`
}

// SetChart replaces this turn's chart. Last-write-wins, not append — see
// the Chart field's doc comment above for why. Safe to call concurrently.
func (c *Context) SetChart(spec ChartSpec) {
	c.chartMu.Lock()
	defer c.chartMu.Unlock()
	c.Chart = &spec
}

// ChartSnapshot returns this turn's chart, if any — same concurrent-read
// rationale as CitationsSnapshot/CardsSnapshot.
func (c *Context) ChartSnapshot() *ChartSpec {
	c.chartMu.Lock()
	defer c.chartMu.Unlock()
	return c.Chart
}

// showMu/showURL/showCaption back SetShow/ShowSnapshot — show.go's
// handleShow already emits a tool_result with the resolved workspace URL
// for a live chat's Emit-driven stream, but Pulsar Daily's tool contexts
// use a no-op Emit (see newDailyToolContext), so a Daily research/
// elaboration block has no other way to learn that its agent.Run called
// show and get back the URL/caption to attach as the block's own
// ImageURL. Same last-write-wins, snapshot-after-the-fact shape as
// Chart/SetChart/ChartSnapshot above, for the same reason: at most one
// artifact worth surfacing per block, read once after the run finishes.
