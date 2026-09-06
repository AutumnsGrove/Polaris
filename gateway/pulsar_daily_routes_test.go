package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestHandleUpdateDailyConfig_CustomBlocksRoundTrip(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks": []string{"headlines"},
		"custom_blocks": []map[string]string{
			{"key": "custom_stocks", "title": "Stock Watchlist", "instructions": "Check NVDA and AAPL closing prices"},
		},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var cfg store.PulsarDailyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(cfg.CustomBlocks) != 1 || cfg.CustomBlocks[0].Key != "custom_stocks" || cfg.CustomBlocks[0].Title != "Stock Watchlist" {
		t.Errorf("CustomBlocks = %+v, want the one block just written", cfg.CustomBlocks)
	}
}

func TestHandleUpdateDailyConfig_RejectsCustomBlockCollidingWithBuiltIn(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks": []string{"headlines"},
		"custom_blocks": []map[string]string{
			{"key": "weather", "title": "My Weather", "instructions": "whatever"},
		},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a custom block key colliding with a built-in block", resp.StatusCode)
	}
}

func TestHandleUpdateDailyConfig_RejectsIncompleteCustomBlock(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks": []string{"headlines"},
		"custom_blocks": []map[string]string{
			{"key": "custom_stocks", "title": "Stock Watchlist", "instructions": ""},
		},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a custom block missing instructions", resp.StatusCode)
	}
}

func TestHandleUpdateDailyConfig_RejectsDuplicateCustomBlockKeys(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks": []string{"headlines"},
		"custom_blocks": []map[string]string{
			{"key": "custom_stocks", "title": "Stock Watchlist", "instructions": "Check NVDA"},
			{"key": "custom_stocks", "title": "Duplicate", "instructions": "Check AAPL"},
		},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for two custom blocks sharing a key", resp.StatusCode)
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

	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}, 0); err != nil {
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

	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}, 0); err != nil {
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

func TestHandleGetNextDailyEdition(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}, 0); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}
	if err := h.db.UpsertDailyEdition("2026-09-07", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Rainy"}}, 0); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	resp, err := http.Get(h.url("/api/pulsar/daily/editions/2026-09-05/next"))
	if err != nil {
		t.Fatalf("GET next edition: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var next store.PulsarDailyEdition
	json.NewDecoder(resp.Body).Decode(&next)
	if next.Date != "2026-09-07" {
		t.Errorf("next edition = %+v, want 2026-09-07 (the missed 2026-09-06 shouldn't matter)", next)
	}

	resp2, err := http.Get(h.url("/api/pulsar/daily/editions/2026-09-07/next"))
	if err != nil {
		t.Fatalf("GET next edition past the newest: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 past the newest edition", resp2.StatusCode)
	}
}

func TestHandleGetDailyTrace(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.UpsertDailyBlockTrace(store.PulsarDailyBlockTrace{
		EditionDate:   "2026-09-06",
		BlockKey:      "weather",
		Title:         "Weather",
		StageAContent: "Now: 68F, clear",
		CostUSD:       0,
	}); err != nil {
		t.Fatalf("seeding trace: %v", err)
	}

	resp, err := http.Get(h.url("/api/pulsar/daily/editions/2026-09-06/trace"))
	if err != nil {
		t.Fatalf("GET trace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var trace []store.PulsarDailyBlockTrace
	if err := json.NewDecoder(resp.Body).Decode(&trace); err != nil {
		t.Fatalf("decoding trace response: %v", err)
	}
	if len(trace) != 1 || trace[0].BlockKey != "weather" || trace[0].StageAContent != "Now: 68F, clear" {
		t.Errorf("trace = %+v, want the one seeded row", trace)
	}

	// A date with no trace yet is a normal empty result, not a 404 — see
	// handleGetDailyTrace's doc comment.
	resp2, err := http.Get(h.url("/api/pulsar/daily/editions/2020-01-01/trace"))
	if err != nil {
		t.Fatalf("GET trace for untouched date: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty result, not a 404) for a date nothing has run for", resp2.StatusCode)
	}
	var empty []store.PulsarDailyBlockTrace
	json.NewDecoder(resp2.Body).Decode(&empty)
	if len(empty) != 0 {
		t.Errorf("trace for untouched date = %+v, want empty", empty)
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

// TestHandleExpandDailyBlock_ReturnsSeededContent covers the happy path
// for a plain text block — no turn is run anymore (see
// handleExpandDailyBlock's doc comment on why), just the seeded message
// text handed back for the frontend to send itself.
func TestHandleExpandDailyBlock_ReturnsSeededContent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{
		{Key: "weather", Title: "Weather", Content: "Sunny, 72F"},
	}, 0); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"date": "2026-09-03", "block_key": "weather"})
	resp, err := http.Post(h.url("/api/pulsar/daily/expand"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST expand: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var decoded map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	content, _ := decoded["content"].(string)
	if !strings.Contains(content, "Weather") || !strings.Contains(content, "Sunny, 72F") {
		t.Errorf("content = %q, want the block's title and content folded in", content)
	}
	if decoded["attachment_id"] != nil {
		t.Errorf("attachment_id = %v, want unset for a non-image block", decoded["attachment_id"])
	}
}

// TestHandleExpandDailyBlock_PictureOfDayIncludesRealAttachment covers
// the bug this rework fixed: Picture of the Day's expand-to-chat must
// hand back a real attachment (so the model actually sees the image),
// not just the caption text.
func TestHandleExpandDailyBlock_PictureOfDayIncludesRealAttachment(t *testing.T) {
	fakeImageBytes := []byte("\x89PNG\r\n\x1a\nfake-png-bytes")
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(fakeImageBytes)
	}))
	defer imgSrv.Close()

	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{
		{Key: "picture_of_day", Title: "Picture of the Day", Content: "A nebula", ImageURL: imgSrv.URL + "/image.png"},
	}, 0); err != nil {
		t.Fatalf("seeding edition: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"date": "2026-09-03", "block_key": "picture_of_day"})
	resp, err := http.Post(h.url("/api/pulsar/daily/expand"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST expand: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var decoded map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	attachmentID, _ := decoded["attachment_id"].(string)
	if attachmentID == "" {
		t.Fatal("attachment_id is empty, want a real downloaded attachment for an image block")
	}
	if decoded["attachment_content_type"] != "image/png" {
		t.Errorf("attachment_content_type = %v, want image/png", decoded["attachment_content_type"])
	}

	saved, err := os.ReadFile(filepath.Join(h.srvObj.liveConfig().Attachments.Dir, attachmentID))
	if err != nil {
		t.Fatalf("reading saved attachment file: %v", err)
	}
	if string(saved) != string(fakeImageBytes) {
		t.Error("saved attachment file doesn't match the fetched image bytes")
	}
}

func TestHandleExpandDailyBlock_UnknownBlockKeyReturns404(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.UpsertDailyEdition("2026-09-03", []store.PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Sunny"}}, 0); err != nil {
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
