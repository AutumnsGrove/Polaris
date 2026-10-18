// comet_rhythm.go turns the period's user-message timestamps into the
// rhythm block: a per-day heatmap, an hour-of-day histogram, and the peak.
// Bucketing is done here, in the operator's own zone, rather than in SQL —
// SQLite's date() would bucket by UTC, so a late-evening message would land
// on the wrong day for anyone not on UTC.
package gateway

import (
	"time"

	"polaris/store"
	"polaris/tz"
)

// computeCometRhythm builds CometRhythm from raw UTC timestamps. times may
// be empty (a month with no messages), which yields an all-zero rhythm
// covering the period's real day count.
func computeCometRhythm(period string, times []time.Time) store.CometRhythm {
	loc := tz.Location()
	start, end, ok := cometPeriodBounds(period)
	if !ok {
		return store.CometRhythm{}
	}

	perDay := map[string]int{}
	var hours [24]int
	for _, t := range times {
		lt := t.In(loc)
		perDay[lt.Format("2006-01-02")]++
		hours[lt.Hour()]++
	}

	rhythm := store.CometRhythm{Days: []store.CometDayCount{}, Hours: hours}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		n := perDay[key]
		rhythm.Days = append(rhythm.Days, store.CometDayCount{Date: key, Count: n})
		rhythm.DaysInPeriod++
		if n > 0 {
			rhythm.ActiveDays++
		}
		if n > rhythm.BusiestCount {
			rhythm.BusiestCount = n
			rhythm.BusiestDate = key
		}
	}

	peak := 0
	for h := 1; h < 24; h++ {
		if hours[h] > hours[peak] {
			peak = h
		}
	}
	rhythm.PeakHour = peak
	return rhythm
}
