package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/store"
)

func TestHandleConstellationBusy_ReflectsInFlightRun(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	getBusy := func() bool {
		resp, err := http.Get(h.url("/api/constellation/busy"))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Busy bool `json:"busy"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return body.Busy
	}

	if getBusy() {
		t.Error("busy = true with no runs at all, want false")
	}

	threadID := seedWeaverThread(t, h.db, "hello")
	runID, err := h.db.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if !getBusy() {
		t.Error("busy = false with a started, unfinished run, want true")
	}

	if err := h.db.FinishShootingStarRun(runID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	if getBusy() {
		t.Error("busy = true after the run finished, want false")
	}
}

func TestHandleGetConstellationConfig_CreatesDefaultsOnFirstRead(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/constellation/config"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var cfg store.ConstellationConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if cfg.PollIntervalMinutes != 60 || cfg.Enabled {
		t.Errorf("cfg = %+v, want the column defaults", cfg)
	}
}

func TestHandleUpdateConstellationConfig_RoundTrips(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, _ := json.Marshal(map[string]interface{}{"enabled": true, "poll_interval_minutes": 30, "model": "deepseek-pro"})
	req, _ := http.NewRequest(http.MethodPut, h.url("/api/constellation/config"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var cfg store.ConstellationConfig
	json.NewDecoder(resp.Body).Decode(&cfg)
	if !cfg.Enabled || cfg.PollIntervalMinutes != 30 || cfg.Model != "deepseek-pro" {
		t.Errorf("cfg after update = %+v, want the values just written", cfg)
	}
}

func TestHandleListConstellationStars_SectionsFilterCorrectly(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	auto, _ := h.db.CreateStar(store.Star{Title: "Auto star", Category: "technology", Status: "auto"})
	personal, _ := h.db.CreateStar(store.Star{Title: "Personal star", Category: "identity", Status: "auto", IsPersonal: true})
	if err := h.db.SetStarStatus(personal, "confirmed"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	proposed, _ := h.db.CreateStar(store.Star{Title: "Proposed star", Category: "technology", Status: "proposed"})
	rejected, _ := h.db.CreateStar(store.Star{Title: "Rejected star", Category: "technology", Status: "proposed"})
	if err := h.db.SetStarStatus(rejected, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}

	tests := []struct {
		section string
		wantID  int64
	}{
		{"library", auto},
		{"about_you", personal},
		{"inbox", proposed},
		{"rejected", rejected},
	}
	for _, tc := range tests {
		resp, err := http.Get(h.url("/api/constellation/stars?section=" + tc.section))
		if err != nil {
			t.Fatalf("GET section=%s: %v", tc.section, err)
		}
		var stars []store.Star
		if err := json.NewDecoder(resp.Body).Decode(&stars); err != nil {
			t.Fatalf("decoding section=%s: %v", tc.section, err)
		}
		resp.Body.Close()
		if len(stars) != 1 || stars[0].ID != tc.wantID {
			t.Errorf("section=%s = %+v, want exactly star id %d", tc.section, stars, tc.wantID)
		}
	}
}

func TestHandleListConstellationStars_InboxIncludesReasoning(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	proposed, _ := h.db.CreateStar(store.Star{Title: "Proposed star", Category: "technology", Status: "proposed"})
	if err := h.db.CreateThread("thread-1", "Test thread", "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	runID, err := h.db.StartShootingStarRun("thread-1", 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := h.db.RecordShootingStarCandidate(runID, "Proposed star", "unsure", "new_star", "only mentioned once, low confidence", &proposed); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}

	resp, err := http.Get(h.url("/api/constellation/stars?section=inbox"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var stars []store.Star
	if err := json.NewDecoder(resp.Body).Decode(&stars); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(stars) != 1 || stars[0].Reasoning != "only mentioned once, low confidence" {
		t.Errorf("stars = %+v, want the inbox star's Reasoning bulk-populated from shooting_star_candidates", stars)
	}
}

func TestHandleGetConstellationStar_IncludesSourcesAndEdges(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	threadID := seedWeaverThread(t, h.db, "hello")
	a, _ := h.db.CreateStar(store.Star{Title: "A", Category: "technology", Status: "auto"})
	b, _ := h.db.CreateStar(store.Star{Title: "B", Category: "technology", Status: "auto"})
	if err := h.db.LinkStarSource(a, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := h.db.LinkStars(a, b, "both concern edge compute"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}

	resp, err := http.Get(h.url("/api/constellation/stars/" + itoa(a)))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got constellationStarDetail
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Star.Title != "A" {
		t.Errorf("Star.Title = %q, want A", got.Star.Title)
	}
	if len(got.Sources) != 1 || got.Sources[0].ThreadID != threadID {
		t.Errorf("Sources = %+v, want the linked thread", got.Sources)
	}
	if len(got.Edges) != 1 || got.Edges[0].OtherStarID != b {
		t.Errorf("Edges = %+v, want the link to star B", got.Edges)
	}
}

func TestHandleGetConstellationStar_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	resp, err := http.Get(h.url("/api/constellation/stars/999999"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleReviewConstellationStar_Approve(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "proposed"})

	body, _ := json.Marshal(map[string]string{"action": "approve"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/review"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed after approve", star.Status)
	}
}

func TestHandleReviewConstellationStar_Discard(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "proposed"})

	body, _ := json.Marshal(map[string]string{"action": "discard"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/review"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "rejected" {
		t.Errorf("Status = %q, want rejected after discard", star.Status)
	}
}

// TestHandleReviewConstellationStar_RefineStaysInReview covers the real bug
// this was written to fix: Refine used to resolve the review in the same
// step as correcting the star (SetStarStatusAndRecordReview(..., "confirmed",
// ...) unconditionally), so the person never actually saw the revision
// before it left the Inbox queue — it just looked like Refine silently
// auto-accepted. reconcileAndSaveStar's UpdateStar call already resets
// status back to "proposed" on any content change; the fix was for the
// refine case to stop stomping that back to "confirmed" and just log the
// review action instead (RecordStarReview), leaving the star in review with
// its new content so it can be approved, discarded, or refined again.
func TestHandleReviewConstellationStar_RefineStaysInReview(t *testing.T) {
	srv := fakeLLMServer(t, "reconcile", "INVALIDATES: false\n\nSUMMARY: Refined summary\nBODY: Refined body text.")
	h := newTestHarness(t, srv.URL)
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Summary: "old summary", Body: "old body", Status: "proposed"})

	body, _ := json.Marshal(map[string]string{"action": "refine", "correction": "actually it's more nuanced than that"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/review"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "proposed" {
		t.Errorf("Status = %q, want proposed (refine must NOT auto-confirm — the person hasn't approved it yet)", star.Status)
	}
	if star.Summary != "Refined summary" || star.Body != "Refined body text." {
		t.Errorf("content not updated by refine: summary=%q body=%q", star.Summary, star.Body)
	}
}

func TestHandleRestoreConstellationStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "proposed"})
	if err := h.db.SetStarStatus(id, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}

	resp, err := http.Post(h.url("/api/constellation/stars/"+itoa(id)+"/restore"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "confirmed" {
		t.Errorf("Status after restore = %q, want confirmed (not proposed — a direct human decision)", star.Status)
	}
}

func TestHandleRestoreConstellationStar_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	resp, err := http.Post(h.url("/api/constellation/stars/999999/restore"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleRestoreConstellationStar_RejectsNonRejectedStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "confirmed"})

	resp, err := http.Post(h.url("/api/constellation/stars/"+itoa(id)+"/restore"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 (only a rejected star can be restored)", resp.StatusCode)
	}
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "confirmed" {
		t.Errorf("Status = %q, want left unchanged", star.Status)
	}
}

func TestHandleReviewConstellationStar_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	body, _ := json.Marshal(map[string]string{"action": "approve"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/999999/review"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandleReviewConstellationStar_RejectsAlreadyResolvedStar covers the
// double-tap case: approving a star that's already confirmed (e.g. a second
// tap racing the first request, or a stale Inbox list) used to silently
// re-run SetStarStatusAndRecordReview and write a second, spurious
// star_reviews row instead of refusing.
func TestHandleReviewConstellationStar_RejectsAlreadyResolvedStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "confirmed"})

	body, _ := json.Marshal(map[string]string{"action": "approve"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/review"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 (star is not awaiting review)", resp.StatusCode)
	}
}

// TestHandleEditConstellationStar_RejectsRejectedStar covers the gap where
// Edit star (meant for an already-confirmed/auto star past the review
// stage) had no precondition at all — it could previously be called
// against a rejected star and silently rewrite its content while it sat
// hidden in the Library's Rejected section, a mutation outside every flow
// the UI actually exposes for that state.
func TestHandleEditConstellationStar_RejectsRejectedStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Summary: "s", Status: "proposed"})
	if err := h.db.SetStarStatus(id, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"correction": "actually this is wrong"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/edit"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cannot edit a rejected star)", resp.StatusCode)
	}
}

func TestHandleEditConstellationStar_UpdatesConfirmedStar(t *testing.T) {
	srv := fakeLLMServer(t, "reconcile", "INVALIDATES: false\n\nSUMMARY: Edited summary\nBODY: Edited body text.")
	h := newTestHarness(t, srv.URL)
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Summary: "old summary", Body: "old body", Status: "confirmed"})

	body, _ := json.Marshal(map[string]string{"correction": "actually I finished this one"})
	req, _ := http.NewRequest(http.MethodPost, h.url("/api/constellation/stars/"+itoa(id)+"/edit"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Summary != "Edited summary" || star.Body != "Edited body text." {
		t.Errorf("content not updated by edit: summary=%q body=%q", star.Summary, star.Body)
	}
}

func TestHandleRenameConstellationStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "Old title", Category: "technology", Status: "auto"})

	body, _ := json.Marshal(map[string]string{"title": "New title"})
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/constellation/stars/"+itoa(id)), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Title != "New title" {
		t.Errorf("Title = %q, want New title", star.Title)
	}
}

func TestHandleDisableConstellationStar(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	id, _ := h.db.CreateStar(store.Star{Title: "X", Category: "technology", Status: "confirmed"})

	body, _ := json.Marshal(map[string]bool{"disabled": true})
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/constellation/stars/"+itoa(id)), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	star, err := h.db.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if !star.Disabled {
		t.Error("Disabled should be true after PATCH disabled=true")
	}
	if star.Status != "confirmed" {
		t.Errorf("Status = %q, want unchanged (confirmed) — disable is independent of status", star.Status)
	}
}

func TestConstellationDigest_ZeroSignalOmitsHighlight(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/constellation/digest"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var digest constellationDigest
	if err := json.NewDecoder(resp.Body).Decode(&digest); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if digest.NewCount != 0 || digest.LinksCount != 0 {
		t.Errorf("digest = %+v, want zero counts on an empty install", digest)
	}
	if digest.Show {
		t.Error("Show should be false when both counts are zero — no signal, no output")
	}
}

func TestConstellationDigest_ReflectsRecentActivity(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	a, _ := h.db.CreateStar(store.Star{Title: "A", Category: "technology", Status: "auto"})
	b, _ := h.db.CreateStar(store.Star{Title: "B", Category: "technology", Status: "auto"})
	if err := h.db.LinkStars(a, b, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}

	resp, err := http.Get(h.url("/api/constellation/digest"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var digest constellationDigest
	json.NewDecoder(resp.Body).Decode(&digest)
	if !digest.Show {
		t.Error("Show should be true once there's real signal this week")
	}
	if digest.NewCount != 2 || digest.LinksCount != 1 {
		t.Errorf("digest = %+v, want new=2 links=1", digest)
	}
	if digest.Highlight == "" {
		t.Error("Highlight should be set when a link was created this week")
	}
}

func TestHandleGetConstellationMap_ReturnsStarsAndEdges(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	a, _ := h.db.CreateStar(store.Star{Title: "A", Category: "technology", Status: "auto"})
	b, _ := h.db.CreateStar(store.Star{Title: "B", Category: "technology", Status: "auto"})
	if err := h.db.LinkStars(a, b, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}

	resp, err := http.Get(h.url("/api/constellation/map"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got constellationMap
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Stars) != 2 {
		t.Errorf("Stars = %+v, want 2", got.Stars)
	}
	if len(got.Edges) != 1 {
		t.Errorf("Edges = %+v, want 1", got.Edges)
	}
}

func TestReconcileStarContent_ParsesSummaryAndBody(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "INVALIDATES: false\n\nSUMMARY: Now finished, not just researching\nBODY: Full rewritten body text here."}},
	}}

	title, summary, body, invalidated, _, err := reconcileStarContent(context.Background(), mock, store.Star{Title: "X", Summary: "old summary", Body: "old body"}, "actually I finished this one")
	if err != nil {
		t.Fatalf("reconcileStarContent: %v", err)
	}
	if invalidated {
		t.Errorf("invalidated = true, want false")
	}
	if title != "" {
		t.Errorf("title = %q, want empty (blank TITLE: line means unchanged)", title)
	}
	if summary != "Now finished, not just researching" {
		t.Errorf("summary = %q", summary)
	}
	if body != "Full rewritten body text here." {
		t.Errorf("body = %q", body)
	}
}

func TestReconcileStarContent_ParsesTitleWhenPremiseChanges(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "INVALIDATES: false\n\nTITLE: Reads fantasy novels\nSUMMARY: Enjoys fantasy, not sci-fi as previously recorded\nBODY: Full rewritten body text here."}},
	}}

	title, summary, body, invalidated, _, err := reconcileStarContent(context.Background(), mock, store.Star{Title: "Reads science fiction", Summary: "old summary", Body: "old body"}, "actually it's fantasy, not sci-fi")
	if err != nil {
		t.Fatalf("reconcileStarContent: %v", err)
	}
	if invalidated {
		t.Errorf("invalidated = true, want false")
	}
	if title != "Reads fantasy novels" {
		t.Errorf("title = %q, want the model's new title", title)
	}
	if summary == "" || body == "" {
		t.Errorf("summary/body = %q/%q, want non-empty", summary, body)
	}
}

// TestReconcileStarContent_FlatDenialInvalidates covers the real bug this
// was written to fix: a correction that denies the star's whole premise
// ("that wasn't me at all") previously got folded into the star's own
// body as if it were a normal revision, and the caller then confirmed the
// star anyway — producing a "confirmed" star whose entire content was the
// model narrating that the star was wrong. INVALIDATES: true short-
// circuits before SUMMARY/BODY are read at all.
func TestReconcileStarContent_FlatDenialInvalidates(t *testing.T) {
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "INVALIDATES: true\n\nSUMMARY: should not be read\nBODY: should not be read"}},
	}}

	title, summary, body, invalidated, _, err := reconcileStarContent(context.Background(), mock, store.Star{Title: "X", Summary: "old summary", Body: "old body"}, "yeah that wasn't me at all")
	if err != nil {
		t.Fatalf("reconcileStarContent: %v", err)
	}
	if !invalidated {
		t.Fatalf("invalidated = false, want true")
	}
	if title != "" || summary != "" || body != "" {
		t.Errorf("title/summary/body = %q/%q/%q, want empty when invalidated", title, summary, body)
	}
}

func itoa(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
