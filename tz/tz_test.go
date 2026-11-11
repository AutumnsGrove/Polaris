package tz

import (
	"testing"
	"time"
)

func TestSetAndFallback(t *testing.T) {
	t.Cleanup(func() { Set("") })

	Set("")
	if Location() != time.Local {
		t.Fatalf("unset should be time.Local, got %v", Location())
	}

	Set("America/New_York")
	if got := Location().String(); got != "America/New_York" {
		t.Fatalf("got %q", got)
	}
	// DST handled by the zone itself, not a fixed offset.
	_, winter := time.Date(2026, 1, 15, 12, 0, 0, 0, Location()).Zone()
	_, summer := time.Date(2026, 7, 15, 12, 0, 0, 0, Location()).Zone()
	if winter != -5*3600 || summer != -4*3600 {
		t.Fatalf("offsets winter=%d summer=%d", winter, summer)
	}

	Set("Not/AZone")
	if Location() != time.Local {
		t.Fatalf("invalid name should fall back to time.Local")
	}
	if Valid("Not/AZone") || !Valid("") || !Valid("Europe/London") {
		t.Fatal("Valid misbehaves")
	}
}
