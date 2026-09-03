// pulsar_scheduler.go runs Pulsar's background scheduler — a goroutine
// checking every active routine once a minute for a due pulse, same shape
// as backup.RunScheduler (no external cron, works identically bare-metal
// and Docker, zero extra host setup). See docs/plans/pulsar-routines.md's
// "Pulse execution model" for the design this implements.
package gateway

import (
	"context"
	"strconv"
	"strings"
	"time"

	"polaris/store"
)

// pulsarSchedulerInterval is how often the scheduler re-checks every
// active routine. A minute, not backup's hourly cadence: a routine's
// time_of_day is minute-granular, so checking any less often would make
// "07:00" mean "some time between 07:00 and 07:59" in practice.
const pulsarSchedulerInterval = time.Minute

// RunPulsarScheduler fires a pulse for every active routine whose most
// recent scheduled time hasn't been run yet, once per tick, until done is
// closed. Meant to run as a background goroutine for the life of the
// server process — see cmd/run.go.
//
// Checking on every tick (including the very first one right after
// process start) against each routine's own last_run_at, rather than
// scheduling a one-shot timer per routine, is what gives "catch-up on
// restart" for free: a routine whose scheduled time passed while the
// process was down looks identical to one that's due right now, so
// nothing special has to detect "we missed one" separately.
func (s *Server) RunPulsarScheduler(done <-chan struct{}) {
	runOnce := func() {
		// Piggybacks the wizard's own session cleanup onto this same
		// once-a-minute tick — see sweepExpiredWizardSessions' doc comment
		// for why that needs a periodic sweep at all, not just its lazy
		// on-access check. Unconditional, not gated on any routine being
		// due: an abandoned wizard session has nothing to do with whether
		// any pulse fires this tick.
		s.sweepExpiredWizardSessions()

		routines, err := s.db.ListActivePulsarRoutines()
		if err != nil {
			log.Warn("listing active pulsar routines failed", "err", err)
			return
		}
		now := time.Now()
		for _, r := range routines {
			if isRoutineDue(r, now) {
				// Concurrent, not sequential — firePulse already calls
				// handleTurn, the exact same entry point many simultaneous
				// live WebSocket turns already run through concurrently, so
				// nothing about it assumes single-flight execution. Firing
				// sequentially instead would mean two routines due at the
				// same time_of_day (a plausible default, e.g. two routines
				// both left at "daily 7am") serialize behind each other —
				// a slow one (real web_search calls, 30-60s) delays every
				// other routine due that tick, and blocks this whole
				// runOnce pass from returning, which in turn delays the
				// NEXT tick's due-check for every routine, not just the
				// slow one's.
				go s.firePulseRecovered(r)
			}
		}
	}

	runOnce()
	ticker := time.NewTicker(pulsarSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-done:
			return
		}
	}
}

// isRoutineDue reports whether r has a scheduled fire time at or before
// now that it hasn't run for yet — comparing against last_run_at, or
// created_at for a routine that has never fired. created_at matters here,
// not just "never run == always due": without it, creating a "daily at
// 7am" routine at 2pm would treat today's already-passed 7am slot as
// missed and fire immediately on save, which reads as a bug (an
// unexpected pulse the moment the form is submitted), not as the
// restart-catch-up behavior it would be for an established routine whose
// process was actually down over a real scheduled time.
func isRoutineDue(r store.PulsarRoutine, now time.Time) bool {
	scheduled, ok := mostRecentScheduledTime(r, now)
	if !ok {
		return false
	}
	baseline := r.CreatedAt
	if r.LastRunAt != nil {
		baseline = *r.LastRunAt
	}
	return baseline.Before(scheduled)
}

// mostRecentScheduledTime returns the latest instant at or before now
// that r's schedule would have fired at — "today at time_of_day" for
// daily, walking back to the matching weekday/day-of-month for weekly/
// monthly. ok is false for a malformed schedule (should never happen for
// a routine created through the API, which validates this — see
// pulsar_routes.go's validateSchedule — but a defensive check here means
// a bad row can never wedge the whole scheduler loop).
func mostRecentScheduledTime(r store.PulsarRoutine, now time.Time) (time.Time, bool) {
	hour, minute, ok := parseTimeOfDay(r.TimeOfDay)
	if !ok {
		return time.Time{}, false
	}

	switch r.ScheduleType {
	case "daily":
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t, true

	case "weekly":
		wd, ok := parseWeekday(r.ScheduleParams)
		if !ok {
			return time.Time{}, false
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		// At most 7 steps back to the most recent matching weekday —
		// weekday cycles every 7 days, so this always terminates.
		for t.Weekday() != wd || t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t, true

	case "monthly":
		day, ok := parseDayOfMonth(r.ScheduleParams)
		if !ok {
			return time.Time{}, false
		}
		t := monthlyOccurrence(now.Year(), now.Month(), day, hour, minute, now.Location())
		if t.After(now) {
			year, month := now.Year(), now.Month()-1
			if month < time.January {
				month = time.December
				year--
			}
			t = monthlyOccurrence(year, month, day, hour, minute, now.Location())
		}
		return t, true

	default:
		return time.Time{}, false
	}
}

// monthlyOccurrence clamps day to the target month's actual last day —
// e.g. a "31st of every month" routine fires on Feb 28/29 instead of
// never firing in February at all, which a naive time.Date call would do
// silently (it normalizes Feb 31 into early March instead of erroring).
func monthlyOccurrence(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc)
}

func parseTimeOfDay(s string) (hour, minute int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

var weekdayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

func parseWeekday(s string) (time.Weekday, bool) {
	wd, ok := weekdayNames[strings.ToLower(strings.TrimSpace(s))]
	return wd, ok
}

func parseDayOfMonth(s string) (int, bool) {
	day, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || day < 1 || day > 31 {
		return 0, false
	}
	return day, true
}

// firePulseRecovered wraps firePulse with a panic recovery, same
// reasoning as turn.go's detached follow-up-suggestions goroutine: this
// now runs in its own goroutine (see runOnce above), outside any call
// stack net/http recovers, so an unrecovered panic in one routine's pulse
// would otherwise take down the whole process instead of just failing
// that one pulse.
func (s *Server) firePulseRecovered(r store.PulsarRoutine) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic firing pulsar pulse", "routine", r.ID, "name", r.Name, "panic", rec)
		}
	}()
	s.firePulse(r)
}

// firePulse runs one routine's scheduled turn — seeding a brand-new
// thread with the routine's saved prompt and stored turn config, through
// the exact same handleTurn path a live chat message takes (see the plan
// doc: "No new rendering path"). No live WebSocket/HTTP client is waiting
// on this, so send only needs to notice a failure to log, unlike
// handleAsk's non-streaming call (gateway/ask.go) which also collects the
// answer text for its HTTP response.
func (s *Server) firePulse(r store.PulsarRoutine) {
	// Same shutdown-draining registration handleAsk/handleWS use before
	// calling handleTurn — without it, a pulse firing during a restart's
	// drain window is invisible to WaitForActiveTurns and can be killed
	// mid-write exactly like an ungated live turn could. Checked before
	// SetPulsarRoutineLastRun below, not after: stamping last_run_at for a
	// pulse that never actually ran would silently defeat the plan doc's
	// "catch-up on restart" guarantee for this exact routine — the next
	// tick (right after the drain finishes) would see a fresh last_run_at
	// and conclude nothing was missed.
	if !s.TryStartTurn() {
		log.Warn("skipping pulsar pulse — server is restarting", "routine", r.ID)
		return
	}
	defer s.FinishTurn()

	// Recorded before firing, not after — see
	// store.SetPulsarRoutineLastRun's doc comment: a crash mid-turn must
	// not leave this stale and cause the very next tick to immediately
	// re-fire the same routine.
	if err := s.db.SetPulsarRoutineLastRun(r.ID, time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		log.Warn("recording pulsar routine last run failed, skipping this pulse", "routine", r.ID, "err", err)
		return
	}

	msg := ClientMessage{
		Type:              "message",
		Content:           r.Prompt,
		Model:             r.Model,
		Source:            "pulsar",
		FocusMode:         r.FocusMode,
		DeepResearch:      r.DeepResearch,
		PulsarRoutineID:   r.ID,
		PulsarRoutineName: r.Name,
	}
	// Best-effort: a lookup failure shouldn't block the pulse itself from
	// firing, just fall back to no prior-report context this one time —
	// same reasoning as handleTurn's other best-effort DB reads (e.g.
	// SetThreadConfig).
	if report, at, ok, err := s.db.LatestPulseReport(r.ID); err != nil {
		log.Warn("loading previous pulse report failed, continuing without it", "routine", r.ID, "err", err)
	} else if ok {
		msg.PulsarPreviousReport = report
		msg.PulsarPreviousReportAt = at
	}

	var turnErr string
	s.handleTurn(context.Background(), msg, func(evt ServerEvent) {
		// Every event still lands in the events table via handleTurn's own
		// LogEvent calls regardless of this callback — this only needs to
		// notice a hard failure worth a scheduler-level log line. A failed
		// pulse otherwise reuses the existing incomplete-turn/retry UI
		// (see the plan doc), so there's no separate error state to
		// construct here.
		if evt.Type == "error" {
			turnErr = evt.Message
		}
	}, nil)

	if turnErr != "" {
		log.Warn("pulsar pulse failed", "routine", r.ID, "name", r.Name, "err", turnErr)
	} else {
		log.Info("pulsar pulse fired", "routine", r.ID, "name", r.Name)
	}
}
