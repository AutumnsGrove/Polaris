package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"polaris/agent"
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

// TestNovaLoopFromStore pins the harness pick's precedence: a stored
// "nova"/"polaris" beats config.yaml's default in both directions, and unset,
// junk, or a nil store all defer to the default.
func TestNovaLoopFromStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if !NovaLoopFromStore(nil, true) || NovaLoopFromStore(nil, false) {
		t.Error("nil store must defer to the config default")
	}
	if !NovaLoopFromStore(db, true) || NovaLoopFromStore(db, false) {
		t.Error("unset must defer to the config default")
	}
	for val, want := range map[string]bool{"polaris": false, "nova": true, "junk": true} {
		if err := db.SetSetting(settingAgentHarness, val); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		// "junk" defers to the default (true here); the two real picks ignore it.
		if got := NovaLoopFromStore(db, true); got != want {
			t.Errorf("stored %q, config default true: got %v, want %v", val, got, want)
		}
	}
	db.SetSetting(settingAgentHarness, "nova")
	if !NovaLoopFromStore(db, false) {
		t.Error("stored nova must beat a config default of false")
	}
}

func TestHandlePutSettings_AgentHarnessRoundTrips(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	t.Cleanup(func() { agent.SetNovaLoop(false) })

	put := func(v string) int {
		body, _ := json.Marshal(map[string]interface{}{"agent_harness": v})
		req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /api/settings: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	get := func() string {
		resp, err := http.Get(h.url("/api/settings"))
		if err != nil {
			t.Fatalf("GET /api/settings: %v", err)
		}
		defer resp.Body.Close()
		var settings map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&settings)
		s, _ := settings["agent_harness"].(string)
		return s
	}

	if got := get(); got != "nova" {
		t.Errorf("default agent_harness = %q, want nova (the config default)", got)
	}
	if code := put("polaris"); code != http.StatusNoContent {
		t.Fatalf("PUT polaris status = %d, want 204", code)
	}
	if got := get(); got != "polaris" {
		t.Errorf("agent_harness after PUT polaris = %q", got)
	}
	if code := put("bogus"); code != http.StatusBadRequest {
		t.Errorf("PUT bogus status = %d, want 400", code)
	}
}
