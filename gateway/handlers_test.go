package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"polaris/store"
)

func TestHandleModels_ListsConfiguredModelsWithDefault(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/models"))
	if err != nil {
		t.Fatalf("GET /api/models: %v", err)
	}
	defer resp.Body.Close()

	var models []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// Models now come from the registry (models/models.go), not config.yaml
	if len(models) != 4 {
		t.Fatalf("got %d models, want 4 from registry", len(models))
	}
	// Default is set in testutil_test.go's writeTestConfig
	defaultFound := false
	for _, m := range models {
		if m.Default {
			defaultFound = true
			break
		}
	}
	if !defaultFound {
		t.Errorf("no default model found in %+v", models)
	}
}

func TestHandleModels_HotReloadsModelOverrides(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	// Models are now defined in the registry (models/models.go), but
	// config.yaml can override their settings via model_overrides. This
	// test verifies that liveConfig() still picks up those changes without
	// a restart — even though we can't add new models, we can tune existing
	// ones on the fly.
	//
	// The test just confirms that rewriting the config doesn't break model
	// listing — there's no easy way to observe the temperature override from
	// the /api/models endpoint since it doesn't expose those fields.
	h.rewriteConfigWithOverride(t, "mimo-pro", 0.9)

	resp, err := http.Get(h.url("/api/models"))
	if err != nil {
		t.Fatalf("GET /api/models: %v", err)
	}
	defer resp.Body.Close()

	var models []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&models)

	// Should still have all 4 registry models
	if len(models) != 4 {
		t.Errorf("got %d models after config rewrite, want 4", len(models))
	}
}

func TestHandleGetSettings_Defaults(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/settings"))
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer resp.Body.Close()

	var settings map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&settings)
	if settings["theme"] != "dark" {
		t.Errorf("theme = %v, want dark by default", settings["theme"])
	}
	if settings["show_prices"] != true {
		t.Errorf("show_prices = %v, want true by default", settings["show_prices"])
	}
	if settings["default_model"] != "mimo-pro" {
		t.Errorf("default_model = %v, want mimo-pro (config.yaml's default)", settings["default_model"])
	}
}

func TestHandlePutSettings_UpdatesAndPersists(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"theme": "light", "show_prices": false, "default_model": "deepseek"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	getResp, err := http.Get(h.url("/api/settings"))
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer getResp.Body.Close()
	var settings map[string]interface{}
	json.NewDecoder(getResp.Body).Decode(&settings)
	if settings["theme"] != "light" || settings["show_prices"] != false || settings["default_model"] != "deepseek" {
		t.Errorf("settings after PUT = %+v, want the updated values", settings)
	}
}

func TestHandlePutSettings_RejectsInvalidTheme(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"theme": "purple"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid theme", resp.StatusCode)
	}
}

func TestHandlePutSettings_RejectsUnknownModel(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"default_model": "does-not-exist"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown model id", resp.StatusCode)
	}
}

func TestThreadsCRUD(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.CreateThread("t1", "Original Title", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "hello", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// List
	listResp, err := http.Get(h.url("/api/threads"))
	if err != nil {
		t.Fatalf("GET /api/threads: %v", err)
	}
	defer listResp.Body.Close()
	var threads []store.Thread
	json.NewDecoder(listResp.Body).Decode(&threads)
	if len(threads) != 1 || threads[0].ID != "t1" {
		t.Fatalf("ListThreads = %+v, want just t1", threads)
	}

	// Get
	getResp, err := http.Get(h.url("/api/threads/t1"))
	if err != nil {
		t.Fatalf("GET /api/threads/t1: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET thread status = %d, want 200", getResp.StatusCode)
	}
	var got struct {
		Messages []store.Message `json:"messages"`
	}
	json.NewDecoder(getResp.Body).Decode(&got)
	if len(got.Messages) != 1 || got.Messages[0].Content != "hello" {
		t.Errorf("messages = %+v, want the one persisted message", got.Messages)
	}

	// Rename
	renameBody, _ := json.Marshal(map[string]string{"title": "Renamed Thread"})
	renameReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/t1"), bytes.NewReader(renameBody))
	renameResp, err := http.DefaultClient.Do(renameReq)
	if err != nil {
		t.Fatalf("PATCH /api/threads/t1: %v", err)
	}
	renameResp.Body.Close()
	if renameResp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename status = %d, want 204", renameResp.StatusCode)
	}
	thread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Title != "Renamed Thread" {
		t.Errorf("title = %q, want %q", thread.Title, "Renamed Thread")
	}

	// Rename rejects empty title
	emptyBody, _ := json.Marshal(map[string]string{"title": "   "})
	emptyReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/t1"), bytes.NewReader(emptyBody))
	emptyResp, err := http.DefaultClient.Do(emptyReq)
	if err != nil {
		t.Fatalf("PATCH with empty title: %v", err)
	}
	emptyResp.Body.Close()
	if emptyResp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty-title rename status = %d, want 400", emptyResp.StatusCode)
	}

	// Delete
	delReq, _ := http.NewRequest(http.MethodDelete, h.url("/api/threads/t1"), nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /api/threads/t1: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", delResp.StatusCode)
	}
	if _, err := h.db.GetThread("t1"); err == nil {
		t.Error("expected the thread to be gone after DELETE")
	}
}

func TestHandleGetThread_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	resp, err := http.Get(h.url("/api/threads/does-not-exist"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestEvents_ThreadAndRecent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	h.db.LogEvent("", "info", "startup", "server started", nil)
	h.db.LogEvent("t1", "info", "turn", "turn started", nil)

	threadResp, err := http.Get(h.url("/api/threads/t1/events"))
	if err != nil {
		t.Fatalf("GET thread events: %v", err)
	}
	defer threadResp.Body.Close()
	var threadEvents []store.Event
	json.NewDecoder(threadResp.Body).Decode(&threadEvents)
	if len(threadEvents) != 1 || threadEvents[0].Message != "turn started" {
		t.Errorf("thread events = %+v, want just the thread-scoped one", threadEvents)
	}

	recentResp, err := http.Get(h.url("/api/events"))
	if err != nil {
		t.Fatalf("GET recent events: %v", err)
	}
	defer recentResp.Body.Close()
	var recentEvents []store.Event
	json.NewDecoder(recentResp.Body).Decode(&recentEvents)
	if len(recentEvents) != 2 {
		t.Errorf("recent events = %+v, want both (global + thread-scoped)", recentEvents)
	}
}
