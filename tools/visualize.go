// visualize is the Tier-2 chart tool — reached for when the model has
// synthesized chartable data itself (comparing figures across search
// results, building a timeline out of a research thread) rather than
// pulled it from one structured source. Tier 1 (deterministic, no tool
// call — see weather.go's handleWeather) exists precisely to keep this
// one rare: it only has to fire for genuinely model-synthesized data. See
// docs/plans/visualize-and-image-search.md.
package tools

import (
	"encoding/json"
	"fmt"

	"polaris/llm"
)

// visualizeMaxPointsPerSeries/visualizeMaxBars/visualizeMaxEvents are
// enforced in the handler before ctx.SetChart is ever called, not just at
// render time — a chart that exceeds one of these is a tool error, not a
// silent truncation, so the model can actually correct itself (aggregate,
// pick the most important N) instead of a chart quietly rendering with
// data missing and no explanation.
const (
	visualizeMaxPointsPerSeries = 50
	visualizeMaxBars            = 12
	visualizeMaxEvents          = 20
)

var visualizeKinds = map[string]bool{"line": true, "bar": true, "timeline": true, "meter": true}

var visualizeDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "visualize",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/visualize.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"kind": map[string]interface{}{
					"type": "string",
					"enum": []string{"line", "bar", "timeline", "meter"},
					"description": "Chart kind. line/bar plot series/points. timeline plots dated events with " +
						"no axis. meter shows one current value against a min/max range.",
				},
				"title": map[string]interface{}{"type": "string", "description": "Chart title."},
				"x_label": map[string]interface{}{"type": "string",
					"description": "X-axis label — line/bar only."},
				"y_label": map[string]interface{}{"type": "string",
					"description": "Y-axis label — line/bar only."},
				"series": map[string]interface{}{
					"type": "array",
					"description": "line/bar only. bar accepts exactly one series in v1 — combine multiple " +
						"comparisons into one series rather than passing several.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"label": map[string]interface{}{"type": "string", "description": "This series' name."},
							"points": map[string]interface{}{
								"type": "array",
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"x": map[string]interface{}{
											"description": "Category/date (string) or numeric x-value.",
										},
										"y": map[string]interface{}{"type": "number"},
									},
									"required": []string{"x", "y"},
								},
							},
						},
						"required": []string{"label", "points"},
					},
				},
				"events": map[string]interface{}{
					"type":        "array",
					"description": "timeline only — a list of dated entries, not a plotted series.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"date":  map[string]interface{}{"type": "string"},
							"label": map[string]interface{}{"type": "string"},
						},
						"required": []string{"date", "label"},
					},
				},
				"value": map[string]interface{}{
					"type":        "object",
					"description": "meter only.",
					"properties": map[string]interface{}{
						"current": map[string]interface{}{"type": "number"},
						"min":     map[string]interface{}{"type": "number"},
						"max":     map[string]interface{}{"type": "number"},
						"label":   map[string]interface{}{"type": "string"},
					},
					"required": []string{"current", "min", "max"},
				},
			},
			"required": []string{"kind", "title"},
		},
	},
}

func init() { Register("visualize", handleVisualize) }

func handleVisualize(argsJSON string, ctx *Context) string {
	var args struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		XLabel string `json:"x_label"`
		YLabel string `json:"y_label"`
		Series []struct {
			Label  string `json:"label"`
			Points []struct {
				X interface{} `json:"x"`
				Y float64     `json:"y"`
			} `json:"points"`
		} `json:"series"`
		Events []struct {
			Date  string `json:"date"`
			Label string `json:"label"`
		} `json:"events"`
		Value *struct {
			Current float64 `json:"current"`
			Min     float64 `json:"min"`
			Max     float64 `json:"max"`
			Label   string  `json:"label"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "visualize", nil, "error: "+err.Error())
	}

	callArgs := map[string]interface{}{"kind": args.Kind, "title": args.Title}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "visualize", "args": callArgs})

	fail := func(msg string) string {
		result := "error: " + msg
		ctx.Emit("tool_result", map[string]interface{}{"tool": "visualize", "result": result})
		return result
	}

	if !visualizeKinds[args.Kind] {
		return fail(fmt.Sprintf("unrecognized kind %q — visualize supports line, bar, timeline, or meter.", args.Kind))
	}
	if args.Title == "" {
		return fail("title is required.")
	}

	spec := ChartSpec{Kind: args.Kind, Title: args.Title, XLabel: args.XLabel, YLabel: args.YLabel}

	switch args.Kind {
	case "line", "bar":
		if len(args.Series) == 0 {
			return fail(fmt.Sprintf("%s requires at least one series.", args.Kind))
		}
		if args.Kind == "bar" && len(args.Series) > 1 {
			return fail(fmt.Sprintf("bar chart supports exactly one series in v1 — got %d series. "+
				"Combine into one series, or use line instead.", len(args.Series)))
		}
		totalBars := 0
		spec.Series = make([]ChartSeries, len(args.Series))
		for i, s := range args.Series {
			if len(s.Points) > visualizeMaxPointsPerSeries {
				return fail(fmt.Sprintf("too many points (%d) — visualize supports at most %d per series. "+
					"Reduce the count and call again with fewer numbers.", len(s.Points), visualizeMaxPointsPerSeries))
			}
			totalBars += len(s.Points)
			points := make([]ChartPoint, len(s.Points))
			for j, p := range s.Points {
				points[j] = ChartPoint{X: p.X, Y: p.Y}
			}
			spec.Series[i] = ChartSeries{Label: s.Label, Points: points}
		}
		if args.Kind == "bar" && totalBars > visualizeMaxBars {
			return fail(fmt.Sprintf("too many bars (%d) — visualize supports at most %d bars for a bar chart. "+
				"Reduce the count and call again with fewer numbers.", totalBars, visualizeMaxBars))
		}
	case "timeline":
		if len(args.Events) == 0 {
			return fail("timeline requires at least one event.")
		}
		if len(args.Events) > visualizeMaxEvents {
			return fail(fmt.Sprintf("too many events (%d) — visualize supports at most %d events for a timeline. "+
				"Reduce the count and call again with fewer numbers.", len(args.Events), visualizeMaxEvents))
		}
		spec.Events = make([]ChartEvent, len(args.Events))
		for i, e := range args.Events {
			spec.Events[i] = ChartEvent{Date: e.Date, Label: e.Label}
		}
	case "meter":
		if args.Value == nil {
			return fail("meter requires a value object with current/min/max.")
		}
		spec.Value = &ChartValue{
			Current: args.Value.Current, Min: args.Value.Min, Max: args.Value.Max, Label: args.Value.Label,
		}
	}

	ctx.SetChart(spec)

	result := fmt.Sprintf("Chart %q (%s) is now attached to this turn's answer — don't repeat its data as a "+
		"list or table in your reply, just refer to it in prose.", args.Title, args.Kind)
	log.Info("visualize", "kind", args.Kind, "title", args.Title)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":   "visualize",
		"result": result,
		"chart":  ctx.ChartSnapshot(),
	})
	return result
}
