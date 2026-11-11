package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"polaris/embed"
	"polaris/store"
)

// fakeEmbedValues turns text into a tiny deterministic vector by keyword, so
// a test can say "this message is the alpha one" and then assert which row a
// semantic search returns. Keeping the vector width at 4 also exercises the
// dims bookkeeping cheaply.
var fakeEmbedKeywords = []string{"alpha", "beta", "gamma"}

func fakeEmbedValues(text string) []float32 {
	vec := []float32{0, 0, 0, 0}
	for i, kw := range fakeEmbedKeywords {
		if strings.Contains(strings.ToLower(text), kw) {
			vec[i] = 1
		}
	}
	vec[3] = 0.01 // never all-zero, matching a real embedding
	return vec
}

// fakeEmbedServer stands in for OpenRouter's embeddings endpoint. cost is
// reported per request so cost accounting can be asserted.
func fakeEmbedServer(t *testing.T, costPerRequest float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding embed request: %v", err)
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, text := range req.Input {
			data[i] = item{Index: i, Embedding: fakeEmbedValues(text)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": req.Model,
			"data":  data,
			"usage": map[string]any{"prompt_tokens": len(req.Input), "cost": costPerRequest},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newEmbedTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// addThreadMessage creates a web thread with one message and returns the
// message id.
func addThreadMessage(t *testing.T, db *store.Store, threadID, content string) int64 {
	t.Helper()
	if err := db.CreateThread(threadID, threadID, "m", "web"); err != nil {
		t.Fatalf("CreateThread(%s): %v", threadID, err)
	}
	id, err := db.AddMessage(threadID, "user", content, "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage(%s): %v", threadID, err)
	}
	return id
}

func TestEmbedIndexer_NilServiceIsSafe(t *testing.T) {
	var v *embedService
	if got := v.Model(); got != "" {
		t.Errorf("nil Model() = %q, want empty", got)
	}
	v.Kick()
	if got := v.CachedCount("message"); got != 0 {
		t.Errorf("nil CachedCount() = %d, want 0", got)
	}
	if got := v.Search("message", []float32{1, 0}, 5); got != nil {
		t.Errorf("nil Search() = %v, want nil", got)
	}
	if got := v.Coverage(); len(got) != 0 {
		t.Errorf("nil Coverage() = %v, want empty", got)
	}
	if p, err := v.IndexOnce(context.Background(), 10); err != nil || p != (IndexProgress{}) {
		t.Errorf("nil IndexOnce() = %+v, %v; want zero, nil", p, err)
	}
	if _, err := v.Backfill(context.Background(), "", 0, nil); err == nil {
		t.Error("nil Backfill should error, want errEmbeddingsUnavailable")
	}
	v.RunEmbedIndexer(make(chan struct{})) // must return immediately, not block
}

func TestEmbedIndexer_IndexesAndSemanticallyFinds(t *testing.T) {
	db := newEmbedTestStore(t)
	alphaID := addThreadMessage(t, db, "alpha-thread", "the alpha question")
	addThreadMessage(t, db, "beta-thread", "the beta question")

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0.0001).URL, "test-model")
	v := newEmbedService(db, client)

	p, err := v.IndexOnce(context.Background(), 100)
	if err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	if p.Messages != 2 || p.Stars != 0 {
		t.Errorf("progress = %+v, want 2 messages and 0 stars", p)
	}
	if got := v.CachedCount(store.EmbeddingKindMessage); got != 2 {
		t.Fatalf("CachedCount = %d, want 2", got)
	}

	hits := v.Search(store.EmbeddingKindMessage, fakeEmbedValues("alpha"), 5)
	if len(hits) == 0 {
		t.Fatal("Search returned nothing")
	}
	if hits[0].RefID != alphaID {
		t.Errorf("top hit = %d, want the alpha message %d", hits[0].RefID, alphaID)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("scores not ordered descending: %v", hits)
	}
}

func TestEmbedIndexer_IgnoresGhostAndInternalThreads(t *testing.T) {
	db := newEmbedTestStore(t)

	if err := db.CreateGhostThread("ghost", "incognito", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := db.AddMessage("ghost", "user", "alpha secret", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := db.CreateThread("weaver", "shooting star", "m", "weaver"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := db.AddMessage("weaver", "user", "alpha weaver transcript", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0).URL, "test-model")
	v := newEmbedService(db, client)
	if _, err := v.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	if got := v.CachedCount(store.EmbeddingKindMessage); got != 0 {
		t.Errorf("CachedCount = %d, want 0 — a ghost or internal thread reached the index", got)
	}
}

func TestEmbedIndexer_PurgesAfterFieldOptOut(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "t", "alpha content")

	field, err := db.CreateField(store.Field{Name: "private"})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if err := db.SetThreadField("t", &field.ID); err != nil {
		t.Fatalf("SetThreadField: %v", err)
	}

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0).URL, "test-model")
	v := newEmbedService(db, client)
	if _, err := v.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	if got := v.CachedCount(store.EmbeddingKindMessage); got != 1 {
		t.Fatalf("CachedCount = %d, want 1 before the opt-out", got)
	}

	// Opting the Field out must make the vector disappear — both from the
	// cache and, on the next sweep, from disk (UpdateField's own purge plus
	// this sweep's anti-join).
	yes := true
	if _, err := db.UpdateField(field.ID, store.FieldUpdate{ExcludeFromChatSearch: &yes}); err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	if _, err := v.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce after opt-out: %v", err)
	}
	if got := v.CachedCount(store.EmbeddingKindMessage); got != 0 {
		t.Errorf("CachedCount = %d after the Field opted out, want 0", got)
	}
	n, err := db.CountEmbeddings(store.EmbeddingKindMessage)
	if err != nil {
		t.Fatalf("CountEmbeddings: %v", err)
	}
	if n != 0 {
		t.Errorf("%d embedding rows survived the opt-out, want 0", n)
	}
}

func TestEmbedIndexer_RecordsReportedCost(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "t", "alpha")

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0.00042).URL, "test-model")
	v := newEmbedService(db, client)
	p, err := v.IndexOnce(context.Background(), 100)
	if err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	if p.CostUSD != 0.00042 {
		t.Errorf("reported cost = %v, want the provider's 0.00042", p.CostUSD)
	}

	cost, err := db.AuxCostByKind(store.AuxKindEmbedding)
	if err != nil {
		t.Fatalf("AuxCostByKind: %v", err)
	}
	if cost != 0.00042 {
		t.Errorf("recorded cost = %v, want 0.00042 — the settings panel shows this number", cost)
	}
}

func TestEmbedIndexer_BackfillIndexesEverythingThenStops(t *testing.T) {
	db := newEmbedTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		addThreadMessage(t, db, id, "alpha in "+id)
	}

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0.000001).URL, "test-model")
	v := newEmbedService(db, client)

	var progressCalls int
	total, err := v.Backfill(context.Background(), store.EmbeddingKindMessage, 0, func(IndexProgress) { progressCalls++ })
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if total.Messages != 3 {
		t.Errorf("backfilled %d messages, want 3", total.Messages)
	}
	if progressCalls == 0 {
		t.Error("progress callback never fired")
	}

	// Resumable for free: a second run derives "nothing pending" from what's
	// stored and does no work.
	again, err := v.Backfill(context.Background(), store.EmbeddingKindMessage, 0, nil)
	if err != nil {
		t.Fatalf("second Backfill: %v", err)
	}
	if again.Messages != 0 {
		t.Errorf("second backfill processed %d messages, want 0", again.Messages)
	}
}

func TestEmbedIndexer_ReloadSkipsCorruptRows(t *testing.T) {
	db := newEmbedTestStore(t)
	id := addThreadMessage(t, db, "t", "alpha")

	client := embed.NewOpenRouter("k", fakeEmbedServer(t, 0).URL, "test-model")
	v := newEmbedService(db, client)
	if _, err := v.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}

	// A row whose blob length disagrees with its recorded dims can only be
	// corruption; loading it would make CosineSimilarity answer "nothing
	// matches" silently.
	if err := db.SetEmbeddingDimsForTest(id, 99); err != nil {
		t.Fatalf("corrupting the row: %v", err)
	}
	if err := v.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := v.CachedCount(store.EmbeddingKindMessage); got != 0 {
		t.Errorf("CachedCount = %d, want 0 (the corrupt row skipped)", got)
	}
}

func TestEmbedIndexer_ModelSwapEmptiesCache(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "t", "alpha")

	server := fakeEmbedServer(t, 0)
	oldSvc := newEmbedService(db, embed.NewOpenRouter("k", server.URL, "old-model"))
	if _, err := oldSvc.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	if got := oldSvc.CachedCount(store.EmbeddingKindMessage); got != 1 {
		t.Fatalf("CachedCount = %d, want 1", got)
	}

	// Pointing at a new model must fail closed: nothing comparable to load,
	// so semantic search is off (FTS keeps working) until a reindex runs.
	newSvc := newEmbedService(db, embed.NewOpenRouter("k", server.URL, "new-model"))
	if err := newSvc.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := newSvc.CachedCount(store.EmbeddingKindMessage); got != 0 {
		t.Errorf("CachedCount = %d under a new model, want 0", got)
	}
	cov := newSvc.Coverage()
	if cov[store.EmbeddingKindMessage].StaleModel != 1 {
		t.Errorf("StaleModel = %d, want 1 (the settings-panel reindex warning)", cov[store.EmbeddingKindMessage].StaleModel)
	}
}

func TestEmbedIndexer_CoverageReportsEligibleNotRaw(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "visible", "alpha")
	if err := db.CreateThread("hidden", "hidden", "m", "weaver"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := db.AddMessage("hidden", "user", "alpha transcript", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	v := newEmbedService(db, embed.NewOpenRouter("k", fakeEmbedServer(t, 0).URL, "test-model"))
	if _, err := v.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}

	cov := v.Coverage()[store.EmbeddingKindMessage]
	if cov.Eligible != 1 || cov.Indexed != 1 {
		t.Errorf("coverage = %+v, want 1 eligible and 1 indexed — the raw count (2) would show a permanent 50%%", cov)
	}
}

// TestBackfill_KindFilterTouchesOnlyThatKind is a regression test: IndexOnce
// always embedded both kinds, so a kind-filtered Backfill silently embedded
// the other one too — which made `--kind message` report a count that didn't
// match the corpus (1,198 of 1,598 on real history).
func TestBackfill_KindFilterTouchesOnlyThatKind(t *testing.T) {
	db := newEmbedTestStore(t)
	addThreadMessage(t, db, "t1", "alpha one")
	if _, err := db.CreateStar(store.Star{Title: "T", Summary: "s", Body: "b", Status: "auto", Category: "c"}); err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	svc := newEmbedService(db, embed.NewOpenRouter("k", fakeEmbedServer(t, 0.0001).URL, "test-model"))
	progress, err := svc.Backfill(context.Background(), store.EmbeddingKindMessage, 0, nil)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if progress.Messages != 1 {
		t.Errorf("messages = %d, want 1", progress.Messages)
	}
	if progress.Stars != 0 {
		t.Errorf("stars = %d, want 0 — a message-only backfill must not embed stars", progress.Stars)
	}

	starCount, err := db.CountEmbeddings(store.EmbeddingKindStar)
	if err != nil {
		t.Fatalf("CountEmbeddings: %v", err)
	}
	if starCount != 0 {
		t.Errorf("%d star embeddings written by a message-only backfill, want 0", starCount)
	}

	// And the reverse direction.
	progress, err = svc.Backfill(context.Background(), store.EmbeddingKindStar, 0, nil)
	if err != nil {
		t.Fatalf("Backfill(star): %v", err)
	}
	if progress.Stars != 1 || progress.Messages != 0 {
		t.Errorf("progress = %+v, want 1 star and 0 messages", progress)
	}
}

// TestBackfill_ReportsCost pins that a backfill's reported spend is the sum
// of what the provider charged, since that number is what the CLI prints and
// what lands in the settings panel.
func TestBackfill_ReportsCost(t *testing.T) {
	db := newEmbedTestStore(t)
	for _, id := range []string{"a", "b"} {
		addThreadMessage(t, db, id, "alpha "+id)
	}
	svc := newEmbedService(db, embed.NewOpenRouter("k", fakeEmbedServer(t, 0.0001).URL, "test-model"))

	progress, err := svc.Backfill(context.Background(), store.EmbeddingKindMessage, 0, nil)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if progress.CostUSD != 0.0001 {
		t.Errorf("CostUSD = %v, want the provider's 0.0001 (recorded once for the whole run, not per row)", progress.CostUSD)
	}
}

// A backfill that fails partway still owes the ledger for the batches the
// provider already billed. Reverting the deferred recordSpend in Backfill
// makes this fail with a zero ledger.
func TestBackfill_FailedRunStillRecordsSpend(t *testing.T) {
	db := newEmbedTestStore(t)
	for i := 0; i < indexBudget+1; i++ {
		addThreadMessage(t, db, "t"+strconv.Itoa(i), "alpha "+strconv.Itoa(i))
	}
	good := fakeEmbedServer(t, 0.0001)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// indexBudget (200) rows chunk into two provider requests (MaxBatch is
		// 128), so the first successful batch is calls 1-2; the 201st row's
		// request is call 3.
		if calls > 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		// Replay the first request against the well-behaved fake.
		resp, err := http.Post(good.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			t.Errorf("proxying to fake: %v", err)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	svc := newEmbedService(db, embed.NewOpenRouter("k", srv.URL, "test-model"))

	if _, err := svc.Backfill(context.Background(), store.EmbeddingKindMessage, 0, nil); err == nil {
		t.Fatal("Backfill: want the second batch's error, got nil")
	}
	spent, err := db.AuxCostByKind(store.AuxKindEmbedding)
	if err != nil {
		t.Fatalf("AuxCostByKind: %v", err)
	}
	if spent < 0.00019 || spent > 0.00021 {
		t.Errorf("recorded spend = %v, want ~0.0002 for the two requests that succeeded", spent)
	}
}
