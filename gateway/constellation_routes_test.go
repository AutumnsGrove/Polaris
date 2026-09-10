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

	summary, body, invalidated, _, err := reconcileStarContent(context.Background(), mock, store.Star{Title: "X", Summary: "old summary", Body: "old body"}, "actually I finished this one")
	if err != nil {
		t.Fatalf("reconcileStarContent: %v", err)
	}
	if invalidated {
		t.Errorf("invalidated = true, want false")
	}
	if summary != "Now finished, not just researching" {
		t.Errorf("summary = %q", summary)
	}
	if body != "Full rewritten body text here." {
		t.Errorf("body = %q", body)
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

	summary, body, invalidated, _, err := reconcileStarContent(context.Background(), mock, store.Star{Title: "X", Summary: "old summary", Body: "old body"}, "yeah that wasn't me at all")
	if err != nil {
		t.Fatalf("reconcileStarContent: %v", err)
	}
	if !invalidated {
		t.Fatalf("invalidated = false, want true")
	}
	if summary != "" || body != "" {
		t.Errorf("summary/body = %q/%q, want empty when invalidated", summary, body)
	}
}

func itoa(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
