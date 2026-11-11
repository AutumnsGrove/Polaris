package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"polaris/prompts"
	"polaris/store"
	"polaris/tz"
)

// TestVisualsModes pins the dial's value set. The PUT validation is built from
// visualsModes and prompts.UIFragment switches on the same three strings, so
// a mode added in one place but not the other would be accepted by the API and
// then silently offer no blocks (or vice versa).
func TestVisualsModes(t *testing.T) {
	want := []string{"off", "low", "normal"}
	if !slices.Equal(visualsModes, want) {
		t.Fatalf("visualsModes = %v, want %v", visualsModes, want)
	}
	set := prompts.Get()
	for _, m := range visualsModes {
		if got := set.UIFragment(m) != ""; got != (m != "off") {
			t.Errorf("UIFragment(%q) non-empty = %v, want %v", m, got, m != "off")
		}
	}
	if !slices.Contains(visualsModes, visualsDefault) {
		t.Errorf("visualsDefault %q is not one of visualsModes", visualsDefault)
	}
}

func TestVisualsFromStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if got := VisualsFromStore(nil); got != "low" {
		t.Errorf("nil store: got %q, want low", got)
	}
	if got := VisualsFromStore(db); got != "low" {
		t.Errorf("unset: got %q, want the Low default", got)
	}
	for _, m := range visualsModes {
		if err := db.SetSetting(settingVisuals, m); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if got := VisualsFromStore(db); got != m {
			t.Errorf("stored %q: got %q", m, got)
		}
	}
	// A hand-edited DB value outside the set falls back rather than reaching the prompt.
	if err := db.SetSetting(settingVisuals, "loud"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := VisualsFromStore(db); got != "low" {
		t.Errorf("unrecognized value: got %q, want the Low default", got)
	}
}

// TestOracleGhostEnabledFromStore pins the explicit-opt-in polarity of the
// oracle_ghost_enabled setting (see its doc comment in settings.go): off
// for a nil store, unset, a read error, or any value other than "true".
func TestOracleGhostEnabledFromStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if OracleGhostEnabledFromStore(nil) {
		t.Error("want Oracle-in-ghost off for a nil store")
	}
	if OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost off by default (unset)")
	}
	if err := db.SetSetting(settingOracleGhostEnabled, "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost on after storing \"true\"")
	}
	if err := db.SetSetting(settingOracleGhostEnabled, "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost off after storing \"false\"")
	}
}

func TestPutSettingsTimezone(t *testing.T) {
	t.Cleanup(func() { tz.Set("") })
	h := newTestHarness(t, "")

	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := put(`{"timezone":"Mars/Olympus"}`); code != http.StatusBadRequest {
		t.Errorf("invalid zone: status %d, want 400", code)
	}
	if code := put(`{"timezone":"America/New_York"}`); code != http.StatusNoContent {
		t.Fatalf("valid zone: status %d, want 204", code)
	}
	if got := tz.Location().String(); got != "America/New_York" {
		t.Errorf("active zone = %q, want it applied immediately on save", got)
	}

	// A restart re-reads the persisted value.
	tz.Set("")
	ApplyTimezoneFromStore(h.db)
	if got := tz.Location().String(); got != "America/New_York" {
		t.Errorf("after ApplyTimezoneFromStore zone = %q", got)
	}

	if code := put(`{"timezone":""}`); code != http.StatusNoContent {
		t.Fatalf("clearing: status %d", code)
	}
	if tz.Location() != time.Local {
		t.Errorf("empty should revert to the server zone")
	}
}
