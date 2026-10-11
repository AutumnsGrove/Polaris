// Package tz holds the operator's chosen time zone (Settings > Time zone),
// shared by everything that reasons about wall-clock time: Pulsar and Pulsar
// Daily schedules, the date preamble, and the current_time tool.
//
// A package-level atomic rather than mutating time.Local: time.Local is a bare
// global read by every goroutine, so rewriting it on a settings save would be a
// data race, and it would silently shift unrelated log/backup timestamps too.
package tz

import (
	"sync/atomic"
	"time"
	// The Docker runtime image is bare Alpine with no /usr/share/zoneinfo, so
	// LoadLocation("America/New_York") would fail there without an embedded
	// copy of the IANA database.
	_ "time/tzdata"
)

var current atomic.Pointer[time.Location]

// Valid reports whether name is acceptable to Set. Empty is valid and means
// "use the server's own zone" — the behavior before this setting existed.
func Valid(name string) bool {
	if name == "" {
		return true
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// Set activates the named IANA zone (e.g. "America/New_York", which handles
// EST/EDT switching itself). Empty or unrecognized falls back to the server
// zone rather than erroring: a hand-edited DB value must never wedge the
// scheduler.
func Set(name string) {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			current.Store(loc)
			return
		}
	}
	current.Store(nil)
}

// Location is the active zone, or time.Local when none is configured.
func Location() *time.Location {
	if loc := current.Load(); loc != nil {
		return loc
	}
	return time.Local
}

// Now is time.Now() in the active zone.
func Now() time.Time { return time.Now().In(Location()) }
