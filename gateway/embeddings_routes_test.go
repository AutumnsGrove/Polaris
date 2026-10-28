package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"polaris/embed"
	"polaris/store"
)

// newEmbeddingsRouteServer builds a Server with a real store, a real
// (fake-transport) embed client and no frontend — enough to exercise the
// admin endpoints over real HTTP.
func newEmbeddingsRouteServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	db := newEmbedTestStore(t)
	s := &Server{db: db}
	s.embeds = newEmbedService(db, fakeEmbedClient(t, 0.0001))
	return s, db
}

// fakeEmbedClient builds a real *embed.Client pointed at the fake provider,
// so these tests exercise the same HTTP path production does.
func fakeEmbedClient(t *testing.T, cost float64) *embed.Client {
	t.Helper()
	return embed.NewOpenRouter("test-key", fakeEmbedServer(t, cost).URL, "test-model")
}

// postEmbeddings drives one admin endpoint and decodes the JSON body.
func postEmbeddings(t *testing.T, s *Server, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /api/embeddings/backfill", s.handleEmbeddingsBackfill)
	s.mux.HandleFunc("POST /api/embeddings/reindex", s.handleEmbeddingsReindex)
	s.mux.HandleFunc("GET /api/embeddings/status", s.handleEmbeddingsStatus)

	req := httptest.NewRequest(http.MethodPost, path, nil)
	s.mux.ServeHTTP(rec, req)

	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

func TestEmbeddingsRoutes_BackfillThenStatus(t *testing.T) {
	s, db := newEmbeddingsRouteServer(t)
	addThreadMessage(t, db, "t1", "alpha one")
	addThreadMessage(t, db, "t2", "beta two")

	rec, body := postEmbeddings(t, s, "/api/embeddings/backfill?kind=all")
	if rec.Code != http.StatusOK {
		t.Fatalf("backfill status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if body["messages"].(float64) != 2 {
		t.Errorf("messages = %v, want 2", body["messages"])
	}
	if body["cost_usd"].(float64) != 0.0001 {
		t.Errorf("cost_usd = %v, want the provider's reported 0.0001", body["cost_usd"])
	}

	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/embeddings/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var status EmbeddingStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	if !status.Configured || status.Model != "test-model" {
		t.Errorf("status = %+v, want configured with test-model", status)
	}
	if got := status.Index[store.EmbeddingKindMessage]; got.Indexed != 2 || got.Eligible != 2 {
		t.Errorf("message coverage = %+v, want 2/2", got)
	}
}

func TestEmbeddingsRoutes_KindFilterHonored(t *testing.T) {
	s, db := newEmbeddingsRouteServer(t)
	addThreadMessage(t, db, "t1", "alpha one")
	if _, err := db.CreateStar(store.Star{Title: "T", Summary: "s", Body: "b", Status: "auto", Category: "c"}); err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	rec, body := postEmbeddings(t, s, "/api/embeddings/backfill?kind=star")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["stars"].(float64) != 1 || body["messages"].(float64) != 0 {
		t.Errorf("progress = %v, want 1 star and 0 messages (kind=star must not touch messages)", body)
	}
}

func TestEmbeddingsRoutes_InvalidKindIsBadRequest(t *testing.T) {
	s, _ := newEmbeddingsRouteServer(t)
	rec, _ := postEmbeddings(t, s, "/api/embeddings/backfill?kind=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestEmbeddingsRoutes_UnconfiguredIsConflict(t *testing.T) {
	db := newEmbedTestStore(t)
	s := &Server{db: db} // embeds stays nil
	rec, _ := postEmbeddings(t, s, "/api/embeddings/backfill")
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for an unconfigured deployment", rec.Code)
	}
}

// TestEmbeddingsRoutes_SecondRunIsRejectedWhileOneIsRunning pins the
// single-flight guard: two concurrent CLI invocations must not double the
// provider spend or race writes on the same rows.
func TestEmbeddingsRoutes_SecondRunIsRejectedWhileOneIsRunning(t *testing.T) {
	s, db := newEmbeddingsRouteServer(t)
	addThreadMessage(t, db, "t1", "alpha one")

	// Stand in for a run already in progress.
	if !embeddingBackfillRunning.CompareAndSwap(false, true) {
		t.Fatal("guard was already set; a previous test leaked it")
	}
	defer embeddingBackfillRunning.Store(false)

	rec, _ := postEmbeddings(t, s, "/api/embeddings/backfill")
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 while another backfill runs", rec.Code)
	}
}

// TestEmbeddingsRoutes_ReindexClearsThenRebuilds covers the model-swap
// recovery path: stale rows from the old model must be gone, and coverage
// must come back to 100% under the new one.
func TestEmbeddingsRoutes_ReindexClearsThenRebuilds(t *testing.T) {
	s, db := newEmbeddingsRouteServer(t)
	addThreadMessage(t, db, "t1", "alpha one")

	// A leftover row from a previous model.
	if err := db.UpsertEmbedding(store.EmbeddingKindMessage, 1, "old-model", 4, []byte{1, 2, 3, 4}, "alpha one"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	rec, body := postEmbeddings(t, s, "/api/embeddings/reindex?kind=all")
	if rec.Code != http.StatusOK {
		t.Fatalf("reindex status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if body["messages"].(float64) != 1 {
		t.Errorf("messages = %v, want 1 rebuilt", body["messages"])
	}

	cov, err := db.EmbeddingCoverage(store.EmbeddingKindMessage, "test-model")
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.StaleModel != 0 {
		t.Errorf("StaleModel = %d after a reindex, want 0", cov.StaleModel)
	}
	if cov.Indexed != 1 || cov.Eligible != 1 {
		t.Errorf("coverage = %+v, want 1/1", cov)
	}
}

// TestBuildStats_EmbeddingBucketSumsBackToTotal pins the invariant the
// settings panel relies on: every bucket adds back to the headline exactly,
// so a new bucket that isn't added to both grand totals would show up here.
func TestBuildStats_EmbeddingBucketSumsBackToTotal(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "t1", "alpha")

	embedSvc := newEmbedService(db, fakeEmbedClient(t, 0.00042))
	if _, err := embedSvc.IndexOnce(context.Background(), 10); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}

	stats, err := db.GetStats(0)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.CostBySource.Embeddings.TotalCostUSD != 0.00042 {
		t.Errorf("Embeddings bucket = %v, want 0.00042", stats.CostBySource.Embeddings.TotalCostUSD)
	}
	sum := stats.CostBySource.Polaris.TotalCostUSD + stats.CostBySource.Pulsar.TotalCostUSD +
		stats.CostBySource.Daily.TotalCostUSD + stats.CostBySource.Constellation.TotalCostUSD +
		stats.CostBySource.Comet.TotalCostUSD + stats.CostBySource.Embeddings.TotalCostUSD
	if diff := sum - stats.TotalCostUSD; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("buckets sum to %v but TotalCostUSD is %v — the settings panel would show a breakdown that doesn't add up", sum, stats.TotalCostUSD)
	}
}
