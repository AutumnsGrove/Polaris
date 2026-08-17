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
	if len(models) != 6 {
		t.Fatalf("got %d models, want 6 from registry", len(models))
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

	// Should still have all 6 registry models
	if len(models) != 6 {
		t.Errorf("got %d models after config rewrite, want 6", len(models))
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
	if settings["default_model"] != "mimo-pro" {
		t.Errorf("default_model = %v, want mimo-pro (config.yaml's default)", settings["default_model"])
	}
	if settings["voice_input_mode"] != "toggle" {
		t.Errorf("voice_input_mode = %v, want toggle by default", settings["voice_input_mode"])
	}
}

func TestHandlePutSettings_UpdatesAndPersists(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"theme": "light", "default_model": "deepseek"})
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
	if settings["theme"] != "light" || settings["default_model"] != "deepseek" {
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

func TestHandlePutSettings_DefaultFocusModeRoundTrips(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"default_focus_mode": "socratic"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
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
	if settings["default_focus_mode"] != "socratic" {
		t.Errorf("default_focus_mode = %v, want socratic", settings["default_focus_mode"])
	}
}

func TestHandlePutSettings_DefaultFocusModeOffClearsIt(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	setBody, _ := json.Marshal(map[string]interface{}{"default_focus_mode": "brief"})
	setReq, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(setBody))
	http.DefaultClient.Do(setReq)

	offBody, _ := json.Marshal(map[string]interface{}{"default_focus_mode": "off"})
	offReq, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(offBody))
	resp, err := http.DefaultClient.Do(offReq)
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
	if settings["default_focus_mode"] != "" {
		t.Errorf("default_focus_mode = %v, want empty string after setting to off", settings["default_focus_mode"])
	}
}

func TestHandlePutSettings_VoiceInputModeRoundTrips(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"voice_input_mode": "hold"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
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
	if settings["voice_input_mode"] != "hold" {
		t.Errorf("voice_input_mode = %v, want hold", settings["voice_input_mode"])
	}
}

func TestHandlePutSettings_RejectsUnknownVoiceInputMode(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"voice_input_mode": "carrier_pigeon"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown voice_input_mode", resp.StatusCode)
	}
}

func TestHandlePutSettings_RejectsUnknownFocusMode(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"default_focus_mode": "not_a_real_mode"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/settings"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown focus mode", resp.StatusCode)
	}
}

func TestThreadsCRUD(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.CreateThread("t1", "Original Title", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "hello", "[]", "[]", 0, ""); err != nil {
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

	// Favorite — independent of title, doesn't require one in the body
	favBody, _ := json.Marshal(map[string]interface{}{"favorite": true})
	favReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/t1"), bytes.NewReader(favBody))
	favResp, err := http.DefaultClient.Do(favReq)
	if err != nil {
		t.Fatalf("PATCH favorite: %v", err)
	}
	favResp.Body.Close()
	if favResp.StatusCode != http.StatusNoContent {
		t.Fatalf("favorite status = %d, want 204", favResp.StatusCode)
	}
	favThread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread after favorite: %v", err)
	}
	if !favThread.Favorite {
		t.Error("expected favorite to be true after PATCH")
	}

	// Un-favorite
	unfavBody, _ := json.Marshal(map[string]interface{}{"favorite": false})
	unfavReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/t1"), bytes.NewReader(unfavBody))
	unfavResp, err := http.DefaultClient.Do(unfavReq)
	if err != nil {
		t.Fatalf("PATCH un-favorite: %v", err)
	}
	unfavResp.Body.Close()
	unfavThread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread after un-favorite: %v", err)
	}
	if unfavThread.Favorite {
		t.Error("expected favorite to be false after un-favoriting")
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

func TestHandleUpdateThread_NonexistentIDReturns404(t *testing.T) {
	h := newTestHarness(t, "")

	favBody, _ := json.Marshal(map[string]interface{}{"favorite": true})
	favReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/does-not-exist"), bytes.NewReader(favBody))
	favResp, err := http.DefaultClient.Do(favReq)
	if err != nil {
		t.Fatalf("PATCH favorite: %v", err)
	}
	favResp.Body.Close()
	if favResp.StatusCode != http.StatusNotFound {
		t.Errorf("favorite status = %d, want 404 for a thread id that doesn't exist", favResp.StatusCode)
	}

	titleBody, _ := json.Marshal(map[string]string{"title": "New Title"})
	titleReq, _ := http.NewRequest(http.MethodPatch, h.url("/api/threads/does-not-exist"), bytes.NewReader(titleBody))
	titleResp, err := http.DefaultClient.Do(titleReq)
	if err != nil {
		t.Fatalf("PATCH title: %v", err)
	}
	titleResp.Body.Close()
	if titleResp.StatusCode != http.StatusNotFound {
		t.Errorf("title status = %d, want 404 for a thread id that doesn't exist", titleResp.StatusCode)
	}
}

func TestHandleRegenerateTitle(t *testing.T) {
	srv := fakeLLMServer(t, "thread title (full context)", "Vacation Budget Planning")
	h := newTestHarness(t, srv.URL)

	if err := h.db.CreateThread("t1", "vacation ideas", "mimo-pro", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "where should I go on vacation", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "assistant", "Japan is a great choice.", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	resp, err := http.Post(h.url("/api/threads/t1/regenerate-title"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST regenerate-title: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Title != "Vacation Budget Planning" {
		t.Errorf("title = %q, want %q", got.Title, "Vacation Budget Planning")
	}

	thread, err := h.db.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Title != "Vacation Budget Planning" {
		t.Errorf("persisted title = %q, want %q", thread.Title, "Vacation Budget Planning")
	}
	if thread.CostUSD <= 0 {
		t.Errorf("thread.CostUSD = %v, want the fake LLM call's cost recorded", thread.CostUSD)
	}
}

func TestHandleRegenerateTitle_NoMessages(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "empty thread", "mimo-pro", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	resp, err := http.Post(h.url("/api/threads/t1/regenerate-title"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST regenerate-title: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a thread with no messages", resp.StatusCode)
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

// TestHandleGetThread_TransientDBErrorIsNot404 is a regression test for a
// bug found while investigating the "thread bump-back" reports: GetThread
// returning ANY error — sql.ErrNoRows for a genuinely missing thread, but
// also a busy timeout, a locked file, or any other transient database
// failure — used to collapse into the same 404. openThread()
// (state.svelte.ts) treats a 404 as "nothing to show, do nothing" with no
// retry and no visible error, so a transient DB hiccup (exactly what the
// restart window's old-process/new-process overlap can produce — see
// cmd/run.go) silently left the UI stuck on stale content with zero
// indication anything went wrong. A real "not found" must still 404; any
// other failure must not.
func TestHandleGetThread_TransientDBErrorIsNot404(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Force a non-ErrNoRows failure deterministically: close the
	// underlying connection out from under the handler, same shape of
	// error a busy/locked file under real cross-process contention would
	// surface as (some database/sql-level error, not "no rows").
	if err := h.db.Close(); err != nil {
		t.Fatalf("closing db: %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/t1"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("status = 404, want anything BUT 404 for a transient DB error — it wrongly looks like the thread doesn't exist")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// TestHandleGetThread_IncludesVariantsMapAndAppliesActiveContent builds a
// forked thread directly at the store layer (no need to go through a full
// WS turn for this) and checks the HTTP response: messages/cost/context
// come from whichever variant is active, not necessarily root's own row,
// and the variants map correctly lists both alternatives with the right
// one marked active.
func TestHandleGetThread_IncludesVariantsMapAndAppliesActiveContent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("root", "user", "q1", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := h.db.AddMessage("root", "assistant", "original answer", "[]", "[]", 0.01, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	forkID, err := h.db.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := h.db.AddMessage(forkID, "assistant", "regenerated answer", "[]", "[]", 0.02, ""); err != nil {
		t.Fatalf("AddMessage(fork): %v", err)
	}
	if err := h.db.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/root"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got struct {
		store.Thread
		Messages []store.Message `json:"messages"`
		Variants map[string]struct {
			IDs    []string `json:"ids"`
			Active string   `json:"active"`
		} `json:"variants"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if got.ID != "root" {
		t.Errorf("id = %q, want %q (root's own identity, not the fork's)", got.ID, "root")
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "regenerated answer" {
		t.Fatalf("messages = %+v, want the active fork's content", got.Messages)
	}
	if got.CostUSD != 0.02 {
		t.Errorf("cost_usd = %v, want the active variant's own 0.02, not root's 0.01", got.CostUSD)
	}

	group, ok := got.Variants["1"]
	if !ok {
		t.Fatalf("variants = %+v, want an entry at position 1", got.Variants)
	}
	if len(group.IDs) != 2 || group.IDs[0] != "root" || group.IDs[1] != forkID {
		t.Errorf("variants[1].ids = %v, want [root, %s]", group.IDs, forkID)
	}
	if group.Active != forkID {
		t.Errorf("variants[1].active = %q, want the fork %q", group.Active, forkID)
	}
}

// TestHandleGetThread_NoVariantsMapWhenNothingForked confirms an ordinary
// thread that's never been edited/regenerated gets no variants key at
// all — the frontend uses its absence to decide not to render a switcher
// anywhere in the timeline.
func TestHandleGetThread_NoVariantsMapWhenNothingForked(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/t1"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Variants map[string]interface{} `json:"variants"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Variants) != 0 {
		t.Errorf("variants = %+v, want none for a never-forked thread", got.Variants)
	}
}

// TestHandleSwapVariant_SwitchesActiveContent exercises the swap endpoint
// end-to-end: switching to the fork must make GetThread show its content,
// and switching back to root must restore root's own.
func TestHandleSwapVariant_SwitchesActiveContent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("root", "assistant", "original", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	forkID, err := h.db.ForkThread("root", "root", 0)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := h.db.AddMessage(forkID, "assistant", "regenerated", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(fork): %v", err)
	}

	swapTo := func(variantID string) []store.Message {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"variant_id": variantID})
		resp, err := http.Post(h.url("/api/threads/root/variant"), "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/threads/root/variant: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("swap to %q status = %d, want 200", variantID, resp.StatusCode)
		}
		var got struct {
			Messages []store.Message `json:"messages"`
		}
		json.NewDecoder(resp.Body).Decode(&got)
		return got.Messages
	}

	msgs := swapTo(forkID)
	if len(msgs) != 1 || msgs[0].Content != "regenerated" {
		t.Fatalf("after swapping to fork, messages = %+v, want [regenerated]", msgs)
	}

	msgs = swapTo("root")
	if len(msgs) != 1 || msgs[0].Content != "original" {
		t.Fatalf("after swapping back to root, messages = %+v, want [original]", msgs)
	}
}

// TestHandleSwapVariant_RejectsUnrelatedThreadID is the important
// security-adjacent case: swapping must only ever accept an id VariantsAt
// actually returned for THIS thread, never an arbitrary client-supplied
// thread id — otherwise one thread's turn could be pointed at a
// completely unrelated thread's content.
func TestHandleSwapVariant_RejectsUnrelatedThreadID(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	if err := h.db.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := h.db.CreateThread("unrelated", "Someone else's thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(unrelated): %v", err)
	}

	body, _ := json.Marshal(map[string]string{"variant_id": "unrelated"})
	resp, err := http.Post(h.url("/api/threads/root/variant"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an id that was never one of root's own variants", resp.StatusCode)
	}

	effective, err := h.db.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != "root" {
		t.Errorf("EffectiveThreadID = %q, want root untouched after the rejected swap", effective)
	}
}

func TestEvents_ThreadAndRecent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	h.db.LogEvent("", "info", "startup", "server started", nil, "")
	h.db.LogEvent("t1", "info", "turn", "turn started", nil, "")

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

// TestHandleThreadEvents_ResolvesActiveVariant verifies GET
// /api/threads/{id}/events follows EffectiveThreadID the same way
// GetThread does — events for a turn that ran on a forked variant are
// logged under the fork's own id (see turn.go's storageThreadID), so
// fetching by root's raw id while that variant is active must still find
// them, not silently return nothing.
func TestHandleThreadEvents_ResolvesActiveVariant(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	h.db.LogEvent("root", "info", "turn", "root's own turn", nil, "")

	forkID, err := h.db.ForkThread("root", "root", 0)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	h.db.LogEvent(forkID, "info", "turn", "the fork's own turn", nil, "")

	// While root's own content is active, only root's own event should
	// come back.
	resp, err := http.Get(h.url("/api/threads/root/events"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var events []store.Event
	json.NewDecoder(resp.Body).Decode(&events)
	resp.Body.Close()
	if len(events) != 1 || events[0].Message != "root's own turn" {
		t.Fatalf("events (root active) = %+v, want just root's own", events)
	}

	// After browsing to the fork, the same URL (still root's id — the
	// client never learns the fork's id directly) must return the
	// fork's events instead.
	if err := h.db.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}
	resp, err = http.Get(h.url("/api/threads/root/events"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&events)
	if len(events) != 1 || events[0].Message != "the fork's own turn" {
		t.Fatalf("events (fork active) = %+v, want just the fork's own", events)
	}
}
