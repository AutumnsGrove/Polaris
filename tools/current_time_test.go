package tools

import (
	"testing"
	"time"
)

func TestHandleCurrentTime_ReportsClockWithZoneAndOffset(t *testing.T) {
	loc := time.FixedZone("EDT", -4*60*60)
	orig := currentTimeNow
	currentTimeNow = func() time.Time { return time.Date(2026, 9, 23, 15, 4, 5, 0, loc) }
	t.Cleanup(func() { currentTimeNow = orig })

	var events []string
	ctx := &Context{Emit: func(evt string, _ map[string]interface{}) { events = append(events, evt) }}
	got := handleCurrentTime("{}", ctx, "call-1")

	want := "Wednesday, September 23, 2026, 15:04:05 (timezone: EDT, UTC-04:00)"
	if got != want {
		t.Fatalf("handleCurrentTime = %q, want %q", got, want)
	}
	// Both events, not just the return value — the timeline chip and the
	// durable event log both key off them (see gateway.logTurnEvent).
	if len(events) != 2 || events[0] != "tool_call" || events[1] != "tool_result" {
		t.Fatalf("emitted %v, want [tool_call tool_result]", events)
	}
}
