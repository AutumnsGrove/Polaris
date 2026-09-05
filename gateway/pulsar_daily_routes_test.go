package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"polaris/store"
)

func TestHandleGetDailyConfig_CreatesDefaultsOnFirstRead(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/pulsar/daily/config"))
	if err != nil {
		t.Fatalf("GET /api/pulsar/daily/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var cfg store.PulsarDailyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if cfg.TimeOfDay != "07:00" || cfg.ArchitectModel != "deepseek-pro" {
		t.Errorf("cfg = %+v, want the column defaults", cfg)
	}
}

func putDailyConfig(t *testing.T, h *testHarness, body map[string]interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, h.url("/api/pulsar/daily/config"), bytes.NewReader(b))
	if err != nil {
		t.Fatalf("building PUT request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/pulsar/daily/config: %v", err)
	}
	return resp
}

func TestHandleUpdateDailyConfig_HappyPath(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"weather", "quote"},
		"sports_teams":    "",
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "06:30",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := json.Marshal(resp)
		t.Fatalf("status = %d, want 200 (%v)", resp.StatusCode, body)
	}

	var cfg store.PulsarDailyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(cfg.EnabledBlocks) != 2 || cfg.TimeOfDay != "06:30" {
		t.Errorf("cfg = %+v, want the values just written", cfg)
	}
}

func TestHandleUpdateDailyConfig_RejectsSportsWithoutTeams(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"sports"},
		"sports_teams":    "",
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (sports enabled with no team/league given)", resp.StatusCode)
	}
}

func TestHandleUpdateDailyConfig_RejectsUnknownBlock(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"top_story"}, // not independently toggleable — see dailyBlockRegistry's doc comment
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (top_story isn't a real toggleable block)", resp.StatusCode)
	}
}

func TestHandleUpdateDailyConfig_CustomInstructionsRoundTrip(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":      []string{"headlines", "local"},
		"custom_instructions": map[string]string{"headlines": "focus on AI", "local": "Beaverton, OR and also Portland, OR"},
		"architect_model":     "deepseek-pro",
		"writer_model":        "deepseek",
		"time_of_day":         "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var cfg store.PulsarDailyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if cfg.CustomInstructions["headlines"] != "focus on AI" {
		t.Errorf("CustomInstructions[headlines] = %q, want %q", cfg.CustomInstructions["headlines"], "focus on AI")
	}
	if cfg.CustomInstructions["local"] != "Beaverton, OR and also Portland, OR" {
		t.Errorf("CustomInstructions[local] = %q, want the suburb+nearby-city value just written", cfg.CustomInstructions["local"])
	}
}

func TestHandleUpdateDailyConfig_RejectsUnknownCustomInstructionBlock(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":      []string{"headlines"},
		"custom_instructions": map[string]string{"nonexistent_block": "whatever"},
		"architect_model":     "deepseek-pro",
		"writer_model":        "deepseek",
		"time_of_day":         "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a custom_instructions key not in the block registry", resp.StatusCode)
	}
}

func TestHandleUpdateDailyConfig_RejectsBadTimeOfDay(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"weather"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "not-a-time",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleGetDailyEdition_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/pulsar/daily/editions/2026-01-01"))
	if err != nil {
		t.Fatalf("GET edition: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a date with no edition", resp.StatusCode)
	}
}

func TestHandleGetDailyEdition_LatestAndByDate(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	resp, err := http.Get(h.url("/api/pulsar/daily/editions/2026-09-03"))
	if err != nil {
		t.Fatalf("GET edition by date: %v", err)
	}
	defer resp.Body.Close()
	var byDate store.PulsarDailyEdition
	json.NewDecoder(resp.Body).Decode(&byDate)
	if byDate.Date != "2026-09-03" || len(byDate.Blocks) != 1 {
		t.Errorf("edition by date = %+v, want the seeded row", byDate)
	}

	resp2, err := http.Get(h.url("/api/pulsar/daily/editions/latest"))
	if err != nil {
		t.Fatalf("GET latest edition: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for /editions/latest with one real edition seeded", resp2.StatusCode)
	}
	var latest store.PulsarDailyEdition
	json.NewDecoder(resp2.Body).Decode(&latest)
	if latest.Date != "2026-09-03" {
		t.Errorf("latest edition = %+v, want the only seeded date", latest)
	}
}

func TestHandleGetPreviousDailyEdition(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	resp, err := http.Get(h.url("/api/pulsar/daily/editions/2026-09-05/previous"))
	if err != nil {
		t.Fatalf("GET previous edition: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var prev store.PulsarDailyEdition
	json.NewDecoder(resp.Body).Decode(&prev)
	if prev.Date != "2026-09-03" {
		t.Errorf("previous edition = %+v, want 2026-09-03 (the missed 2026-09-04 shouldn't matter)", prev)
	}
}

func TestHandleExpandDailyBlock_UnknownDateReturns404(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]string{"date": "2026-01-01", "block_key": "weather"})
	resp, err := http.Post(h.url("/api/pulsar/daily/expand"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST expand: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a date with no edition", resp.StatusCode)
	}
}

func TestHandleExpandDailyBlock_UnknownBlockKeyReturns404(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"date": "2026-09-03", "block_key": "nonexistent"})
	resp, err := http.Post(h.url("/api/pulsar/daily/expand"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST expand: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a block key not present in that edition", resp.StatusCode)
	}
}

func TestDailyFollowupFamily(t *testing.T) {
	tests := map[string]string{
		"picture_of_day": "media",
		"word_of_day":    "curiosity",
		"on_this_day":    "curiosity",
		"quote":          "curiosity",
		"headlines":      "research",
		"weather":        "research",
		"sports":         "research",
	}
	for key, want := range tests {
		if got := dailyFollowupFamily(key); got != want {
			t.Errorf("dailyFollowupFamily(%q) = %q, want %q", key, got, want)
		}
	}
}
