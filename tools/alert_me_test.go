package tools

import (
	"strings"
	"testing"
)

func TestAlertMe_RecordsTrimmedReason(t *testing.T) {
	ctx := newTestContext()
	var got string
	ctx.PulseAlert = func(reason string) error { got = reason; return nil }

	result := Dispatch("alert_me", `{"reason":"  Fed cut rates 50bp  "}`, ctx, "c1")
	if got != "Fed cut rates 50bp" {
		t.Errorf("recorded reason = %q, want trimmed", got)
	}
	if strings.HasPrefix(result, "error") {
		t.Errorf("result = %q, want success", result)
	}
}

func TestAlertMe_RejectsEmptyReasonAndCapsLength(t *testing.T) {
	ctx := newTestContext()
	var got string
	ctx.PulseAlert = func(reason string) error { got = reason; return nil }

	if r := Dispatch("alert_me", `{"reason":"   "}`, ctx, "c1"); !strings.HasPrefix(r, "error") || got != "" {
		t.Errorf("empty reason: result %q recorded %q, want error and nothing recorded", r, got)
	}

	Dispatch("alert_me", `{"reason":"`+strings.Repeat("é", 500)+`"}`, ctx, "c2")
	if n := len([]rune(got)); n != maxPulseAlertReasonLen {
		t.Errorf("capped reason has %d runes, want %d", n, maxPulseAlertReasonLen)
	}
}

func TestAlertMe_OnlyOfferedWithPulseAlert(t *testing.T) {
	ctx := newTestContext()
	for _, d := range Defs(ctx) {
		if d.Function.Name == "alert_me" {
			t.Fatal("alert_me offered without PulseAlert wired")
		}
	}
	ctx.PulseAlert = func(string) error { return nil }
	found := false
	for _, d := range Defs(ctx) {
		found = found || d.Function.Name == "alert_me"
	}
	if !found {
		t.Fatal("alert_me not offered when PulseAlert is wired")
	}
}
