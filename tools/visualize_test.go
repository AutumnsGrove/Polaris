package tools

import (
	"strings"
	"testing"
)

func TestHandleVisualize_Line_SetsChart(t *testing.T) {
	ctx := newTestContext()
	args := `{"kind":"line","title":"Temps","series":[{"label":"High","points":[{"x":"Mon","y":70},{"x":"Tue","y":72}]}]}`
	handleVisualize(args, ctx)

	if ctx.Chart == nil {
		t.Fatal("Chart is nil, want a set chart")
	}
	if ctx.Chart.Kind != "line" || ctx.Chart.Title != "Temps" {
		t.Errorf("Chart = %+v, want kind=line title=Temps", ctx.Chart)
	}
	if len(ctx.Chart.Series) != 1 || len(ctx.Chart.Series[0].Points) != 2 {
		t.Errorf("Chart.Series = %+v, want 1 series with 2 points", ctx.Chart.Series)
	}
}

func TestHandleVisualize_UnrecognizedKind_RejectsWithoutSettingChart(t *testing.T) {
	ctx := newTestContext()
	result := handleVisualize(`{"kind":"pie","title":"x"}`, ctx)

	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after a rejected kind", ctx.Chart)
	}
}

func TestHandleVisualize_Bar_RejectsMultipleSeries(t *testing.T) {
	ctx := newTestContext()
	args := `{"kind":"bar","title":"Compare","series":[
		{"label":"A","points":[{"x":"1","y":1}]},
		{"label":"B","points":[{"x":"1","y":2}]}
	]}`
	result := handleVisualize(args, ctx)

	if !strings.Contains(result, "exactly one series") {
		t.Errorf("result = %q, want a single-series-only error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Bar_RejectsOverBarCap(t *testing.T) {
	ctx := newTestContext()
	var points []string
	for i := 0; i < visualizeMaxBars+1; i++ {
		points = append(points, `{"x":"a","y":1}`)
	}
	args := `{"kind":"bar","title":"Too many","series":[{"label":"A","points":[` + strings.Join(points, ",") + `]}]}`
	result := handleVisualize(args, ctx)

	if !strings.Contains(result, "too many bars") {
		t.Errorf("result = %q, want a too-many-bars error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Line_RejectsEmptySeries(t *testing.T) {
	ctx := newTestContext()
	args := `{"kind":"line","title":"Empty","series":[{"label":"A","points":[]}]}`
	result := handleVisualize(args, ctx)

	if !strings.Contains(result, "has no points") {
		t.Errorf("result = %q, want a no-points error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Line_RejectsOverPointCap(t *testing.T) {
	ctx := newTestContext()
	var points []string
	for i := 0; i < visualizeMaxPointsPerSeries+1; i++ {
		points = append(points, `{"x":"a","y":1}`)
	}
	args := `{"kind":"line","title":"Too many","series":[{"label":"A","points":[` + strings.Join(points, ",") + `]}]}`
	result := handleVisualize(args, ctx)

	if !strings.Contains(result, "too many points") {
		t.Errorf("result = %q, want a too-many-points error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Timeline_RejectsOverEventCap(t *testing.T) {
	ctx := newTestContext()
	var events []string
	for i := 0; i < visualizeMaxEvents+1; i++ {
		events = append(events, `{"date":"2026-01-01","label":"x"}`)
	}
	args := `{"kind":"timeline","title":"Too many","events":[` + strings.Join(events, ",") + `]}`
	result := handleVisualize(args, ctx)

	if !strings.Contains(result, "too many events") {
		t.Errorf("result = %q, want a too-many-events error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Meter_RequiresValue(t *testing.T) {
	ctx := newTestContext()
	result := handleVisualize(`{"kind":"meter","title":"Usage"}`, ctx)

	if !strings.Contains(result, "requires a value") {
		t.Errorf("result = %q, want a value-required error", result)
	}
	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil after rejection", ctx.Chart)
	}
}

func TestHandleVisualize_Meter_SetsChart(t *testing.T) {
	ctx := newTestContext()
	args := `{"kind":"meter","title":"Context usage","value":{"current":80,"min":0,"max":100,"label":"tokens"}}`
	handleVisualize(args, ctx)

	if ctx.Chart == nil || ctx.Chart.Value == nil {
		t.Fatalf("Chart = %+v, want a set value", ctx.Chart)
	}
	if ctx.Chart.Value.Current != 80 || ctx.Chart.Value.Max != 100 {
		t.Errorf("Chart.Value = %+v, want current=80 max=100", ctx.Chart.Value)
	}
}

func TestHandleVisualize_SecondCallOverwritesFirst(t *testing.T) {
	ctx := newTestContext()
	handleVisualize(`{"kind":"line","title":"First","series":[{"label":"A","points":[{"x":"1","y":1}]}]}`, ctx)
	handleVisualize(`{"kind":"bar","title":"Second","series":[{"label":"B","points":[{"x":"1","y":2}]}]}`, ctx)

	if ctx.Chart == nil || ctx.Chart.Title != "Second" {
		t.Errorf("Chart = %+v, want last-write-wins with title=Second", ctx.Chart)
	}
}
