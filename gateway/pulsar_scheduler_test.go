package gateway

import (
	"testing"
	"time"

	"polaris/store"
)

func mustParseTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatalf("parsing time %q: %v", value, err)
	}
	return tm
}

func timePtr(t time.Time) *time.Time { return &t }

func TestIsRoutineDue_Daily(t *testing.T) {
	now := mustParseTime(t, "2006-01-02 15:04", "2026-09-03 07:05")
	longAgo := mustParseTime(t, "2006-01-02 15:04", "2026-08-01 00:00")

	// An established routine (created well before today), never run —
	// its time already passed today, so it's due.
	r := store.PulsarRoutine{ScheduleType: "daily", TimeOfDay: "07:00", CreatedAt: longAgo}
	if !isRoutineDue(r, now) {
		t.Error("a never-run daily routine whose time already passed today should be due")
	}

	r.LastRunAt = timePtr(mustParseTime(t, "2006-01-02 15:04", "2026-09-03 07:00"))
	if isRoutineDue(r, now) {
		t.Error("a daily routine already run today at its scheduled time should not be due again")
	}

	r.LastRunAt = timePtr(mustParseTime(t, "2006-01-02 15:04", "2026-09-02 07:00"))
	if !isRoutineDue(r, now) {
		t.Error("a daily routine last run yesterday should be due again today")
	}

	// Before today's scheduled time, last run yesterday — not due yet.
	before := mustParseTime(t, "2006-01-02 15:04", "2026-09-03 06:59")
	if isRoutineDue(r, before) {
		t.Error("a daily routine already run yesterday should not be due again before today's scheduled time")
	}
}

// TestIsRoutineDue_NeverRun_JustCreated locks in a real bug caught while
// writing these tests: without weighing created_at, a routine created
// *after* today's scheduled slot already passed (e.g. saving a "daily at
// 7am" routine at 2pm) read identically to an established routine that
// missed its slot while the process was down, and fired immediately on
// save — surprising, not the restart-catch-up behavior it was mistaken
// for. created_at as the never-run baseline fixes this: a routine can
// only be "due" for a slot that occurred at or after it was created.
func TestIsRoutineDue_NeverRun_JustCreated(t *testing.T) {
	now := mustParseTime(t, "2006-01-02 15:04", "2026-09-03 14:00")
	r := store.PulsarRoutine{
		ScheduleType: "daily",
		TimeOfDay:    "07:00",
		CreatedAt:    now, // created right now, at 2pm — today's 7am slot already passed
	}
	if isRoutineDue(r, now) {
		t.Error("a routine created after today's slot already passed should wait for tomorrow's slot, not fire immediately")
	}

	tomorrow := mustParseTime(t, "2006-01-02 15:04", "2026-09-04 07:05")
	if !isRoutineDue(r, tomorrow) {
		t.Error("the same routine should be due once tomorrow's slot arrives")
	}
}

func TestIsRoutineDue_RestartCatchUp(t *testing.T) {
	// A daily 07:00 routine last run two days ago — simulates the box
	// being down through yesterday's scheduled fire. Per the plan doc's
	// "Catch-up on restart", this must be due the moment the scheduler
	// checks again, regardless of what time "now" actually is.
	now := mustParseTime(t, "2006-01-02 15:04", "2026-09-03 23:00")
	r := store.PulsarRoutine{
		ScheduleType: "daily",
		TimeOfDay:    "07:00",
		LastRunAt:    timePtr(mustParseTime(t, "2006-01-02 15:04", "2026-09-01 07:00")),
	}
	if !isRoutineDue(r, now) {
		t.Error("a routine that missed a scheduled fire while the process was down should catch up immediately")
	}
}

func TestIsRoutineDue_Weekly(t *testing.T) {
	// 2026-09-03 is a Thursday.
	now := mustParseTime(t, "2006-01-02 15:04", "2026-09-03 10:00")

	r := store.PulsarRoutine{ScheduleType: "weekly", ScheduleParams: "monday", TimeOfDay: "09:00"}
	if !isRoutineDue(r, now) {
		t.Error("a weekly-Monday routine never run should be due once its weekday has occurred")
	}

	// Ran this past Monday (2026-08-31) — not due again until next Monday.
	r.LastRunAt = timePtr(mustParseTime(t, "2006-01-02 15:04", "2026-08-31 09:00"))
	if isRoutineDue(r, now) {
		t.Error("a weekly routine already run this week's occurrence should not be due again")
	}
}

func TestIsRoutineDue_Monthly(t *testing.T) {
	// 31st-of-the-month routine checked in February — should clamp to
	// Feb's actual last day rather than never firing.
	now := mustParseTime(t, "2006-01-02 15:04", "2026-02-28 10:00")
	r := store.PulsarRoutine{ScheduleType: "monthly", ScheduleParams: "31", TimeOfDay: "09:00"}
	if !isRoutineDue(r, now) {
		t.Error("a 31st-of-month routine should clamp to Feb 28 and be due, not silently never fire")
	}
}

func TestIsRoutineDue_MalformedSchedule(t *testing.T) {
	now := time.Now()
	for _, r := range []store.PulsarRoutine{
		{ScheduleType: "daily", TimeOfDay: "not-a-time"},
		{ScheduleType: "weekly", ScheduleParams: "not-a-weekday", TimeOfDay: "09:00"},
		{ScheduleType: "monthly", ScheduleParams: "not-a-number", TimeOfDay: "09:00"},
		{ScheduleType: "yearly", TimeOfDay: "09:00"},
	} {
		if isRoutineDue(r, now) {
			t.Errorf("malformed routine %+v should never be due", r)
		}
	}
}
