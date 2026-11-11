package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/embed"
	"polaris/store"
)

// synonymEmbedServer is a fake provider whose vectors are concept-based
// rather than lexical: every word in one group produces the same vector, so
// a query can be semantically close to a document with no shared wording.
// That's the property hybrid retrieval exists to exploit, and the keyword
// fake in embedsvc_test.go deliberately can't express it.
func synonymEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	groups := [][]string{
		{"coffee", "espresso", "french press", "caffeine"},
		{"database", "sqlite", "postgres"},
		{"bike", "cycling", "bicycle"},
	}
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
			vec := make([]float32, len(groups)+1)
			vec[len(groups)] = 0.01 // never all-zero
			for g, words := range groups {
				for _, w := range words {
					if containsFold(text, w) {
						vec[g] = 1
						break
					}
				}
			}
			data[i] = item{Index: i, Embedding: vec}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": req.Model,
			"data":  data,
			"usage": map[string]any{"prompt_tokens": len(req.Input), "cost": 0.0},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func containsFold(haystack, needle string) bool {
	h := []rune(lowerASCII(haystack))
	n := []rune(lowerASCII(needle))
	if len(n) == 0 || len(h) < len(n) {
		return false
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if h[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// hybridFixture seeds a store with stars that exercise each side of the
// fusion, then indexes them so the semantic tier is live.
type hybridFixture struct {
	db         *store.Store
	svc        *embedService
	coffeeID   int64 // body says "french press", title says nothing about coffee
	sqliteID   int64 // exact-keyword hit for "sqlite"
	rejectedID int64 // rejected star with coffee content (Weaver-only)
	disabledID int64
	bikeID     int64
}

func newHybridFixture(t *testing.T) hybridFixture {
	t.Helper()
	db := newEmbedTestStore(t)
	f := hybridFixture{db: db}

	mk := func(star store.Star) int64 {
		id, err := db.CreateStar(star)
		if err != nil {
			t.Fatalf("CreateStar: %v", err)
		}
		return id
	}
	f.coffeeID = mk(store.Star{
		Title: "Morning routine", Category: "personal", Status: "auto",
		Summary: "how the day starts", Body: "two cups from the french press before 7am",
	})
	f.sqliteID = mk(store.Star{
		Title: "SQLite notes", Category: "tech", Status: "auto",
		Summary: "durability and locking", Body: "WAL mode and busy_timeout",
	})
	f.rejectedID = mk(store.Star{
		Title: "Espresso machines", Category: "personal", Status: "rejected",
		Summary: "was asked no", Body: "considered upgrading the caffeine setup",
	})
	f.disabledID = mk(store.Star{
		Title: "Coffee gear", Category: "personal", Status: "auto",
		Summary: "hidden", Body: "espresso grinder notes",
	})
	f.bikeID = mk(store.Star{
		Title: "Bike maintenance", Category: "personal", Status: "confirmed",
		Summary: "chain care", Body: "cleaning and oiling the bicycle chain",
	})
	if err := db.SetStarDisabled(f.disabledID, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	f.svc = newEmbedService(db, embed.NewOpenRouter("k", synonymEmbedServer(t).URL, "test-model"))
	if _, err := f.svc.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	return f
}

func idsOfStars(stars []store.Star) []int64 {
	ids := make([]int64, len(stars))
	for i, s := range stars {
		ids[i] = s.ID
	}
	return ids
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestSearchLibraryStarsHybrid_SemanticOnlyHit is the whole point: a star
// whose wording never appears in the query is still found. FTS5 returns
// nothing for this pair — "coffee" is not in the star's text at all.
func TestSearchLibraryStarsHybrid_SemanticOnlyHit(t *testing.T) {
	f := newHybridFixture(t)

	fts, err := f.db.SearchLibraryStars("coffee", 30)
	if err != nil {
		t.Fatalf("SearchLibraryStars: %v", err)
	}
	if containsID(idsOfStars(fts), f.coffeeID) {
		t.Skip("fixture is degenerate: FTS already matches this star, so the test proves nothing")
	}

	results, err := f.svc.SearchLibraryStarsHybrid(context.Background(), "coffee", 10)
	if err != nil {
		t.Fatalf("SearchLibraryStarsHybrid: %v", err)
	}
	if !containsID(idsOfStars(results), f.coffeeID) {
		t.Errorf("hybrid results = %v, want the french-press star %d — semantic retrieval found nothing", idsOfStars(results), f.coffeeID)
	}
}

// TestSearchLibraryStarsHybrid_ExactMatchStillRanksFirst guards the other
// direction: adding semantics must not demote a literal keyword hit.
func TestSearchLibraryStarsHybrid_ExactMatchStillRanksFirst(t *testing.T) {
	f := newHybridFixture(t)
	results, err := f.svc.SearchLibraryStarsHybrid(context.Background(), "sqlite", 10)
	if err != nil {
		t.Fatalf("SearchLibraryStarsHybrid: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results for sqlite")
	}
	if results[0].ID != f.sqliteID {
		t.Errorf("top result = %d, want the sqlite star %d", results[0].ID, f.sqliteID)
	}
}

// TestSearchLibraryStarsHybrid_RespectsLibraryEligibility pins that the
// library surface never renders a rejected or disabled star, even though the
// semantic index holds both.
func TestSearchLibraryStarsHybrid_RespectsLibraryEligibility(t *testing.T) {
	f := newHybridFixture(t)
	results, err := f.svc.SearchLibraryStarsHybrid(context.Background(), "espresso coffee", 30)
	if err != nil {
		t.Fatalf("SearchLibraryStarsHybrid: %v", err)
	}
	for _, s := range results {
		if s.Status != "auto" && s.Status != "confirmed" {
			t.Errorf("library search returned a %q star (%d)", s.Status, s.ID)
		}
		if s.ID == f.disabledID {
			t.Errorf("library search returned a disabled star (%d)", s.ID)
		}
	}
}

// TestSearchStarsHybrid_IncludesRejectedButNotDisabled covers Weaver's own
// lead-finder: rejected stars are the "was this already said no to" signal,
// so they must stay reachable, while a disabled star is a hard no.
func TestSearchStarsHybrid_IncludesRejectedButNotDisabled(t *testing.T) {
	f := newHybridFixture(t)
	results, err := f.svc.SearchStarsHybrid(context.Background(), "espresso coffee", 30)
	if err != nil {
		t.Fatalf("SearchStarsHybrid: %v", err)
	}
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	if !containsID(ids, f.rejectedID) {
		t.Errorf("results = %v, want the rejected star %d (Weaver's dedup guard)", ids, f.rejectedID)
	}
	if containsID(ids, f.disabledID) {
		t.Errorf("results = %v, must not include disabled star %d", ids, f.disabledID)
	}
}

// TestHybrid_NilServiceFallsBackToKeyword is the degradation guarantee: no
// embedding service means exactly today's FTS-only behavior, not an error
// and not an empty result.
func TestHybrid_NilServiceFallsBackToKeyword(t *testing.T) {
	f := newHybridFixture(t)

	want, err := f.db.SearchLibraryStars("sqlite", 10)
	if err != nil {
		t.Fatalf("SearchLibraryStars: %v", err)
	}
	got, err := searchLibraryStarsHybrid(nil, f.db, context.Background(), "sqlite", 10)
	if err != nil {
		t.Fatalf("searchLibraryStarsHybrid(nil): %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want the %d FTS-only ones", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Errorf("result %d = %d, want %d (FTS order preserved)", i, got[i].ID, want[i].ID)
		}
	}

	wantLeads, err := f.db.SearchStars("sqlite", 10)
	if err != nil {
		t.Fatalf("SearchStars: %v", err)
	}
	gotLeads, err := searchStarsHybrid(nil, f.db, context.Background(), "sqlite", 10)
	if err != nil {
		t.Fatalf("searchStarsHybrid(nil): %v", err)
	}
	if len(gotLeads) != len(wantLeads) {
		t.Errorf("got %d leads, want %d", len(gotLeads), len(wantLeads))
	}
}

func TestFuseRRF_BothListsAgreeKeepsOrder(t *testing.T) {
	got := fuseRRF([]int64{1, 2, 3}, []int64{1, 2, 3})
	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFuseRRF_DisjointListsInterleave(t *testing.T) {
	// Two retrievers that agree on nothing must not collapse to one list's
	// order: 1 and 10 are both rank 1 for their own retriever.
	got := fuseRRF([]int64{1, 2}, []int64{10, 20})
	if len(got) != 4 {
		t.Fatalf("got %v, want all four ids", got)
	}
	// Both rank-1 entries score identically, so the tie-break (lower id)
	// puts 1 first; 10 and 2 are then tied at rank 2/second respectively.
	if got[0] != 1 || got[1] != 10 {
		t.Errorf("got %v, want the two rank-1 entries first (1, 10)", got)
	}
}

func TestFuseRRF_AgreementOutranksSingleRetriever(t *testing.T) {
	// 2 is mediocre in both lists; 9 is first in one and absent from the
	// other. Fusion should prefer the row both retrievers liked.
	got := fuseRRF([]int64{9, 1, 2}, []int64{5, 2})
	if got[0] != 2 {
		t.Errorf("got %v, want 2 first — agreement across retrievers should beat a single top hit", got)
	}
}

func TestFuseRRF_EmptyInputs(t *testing.T) {
	if got := fuseRRF(); len(got) != 0 {
		t.Errorf("fuseRRF() = %v, want empty", got)
	}
	if got := fuseRRF(nil, nil); len(got) != 0 {
		t.Errorf("fuseRRF(nil, nil) = %v, want empty", got)
	}
	if got := fuseRRF(nil, []int64{4, 5}); len(got) != 2 || got[0] != 4 {
		t.Errorf("fuseRRF(nil, [4 5]) = %v, want [4 5]", got)
	}
}

func TestFuseRRF_StableAcrossCalls(t *testing.T) {
	// Determinism matters because result order is what the model reads; map
	// iteration would otherwise reshuffle ties run to run.
	first := fuseRRF([]int64{3, 1}, []int64{2, 1})
	for i := 0; i < 20; i++ {
		again := fuseRRF([]int64{3, 1}, []int64{2, 1})
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("run %d = %v, first run = %v", i, again, first)
			}
		}
	}
}

// chatFixture seeds threads whose messages split cleanly between "FTS finds
// it" and "only meaning finds it".
func chatFixture(t *testing.T) (db *store.Store, svc *embedService, morningMsgID, exactMsgID int64) {
	t.Helper()
	db = newEmbedTestStore(t)

	if err := db.CreateThread("routine", "Morning routine", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	var err error
	morningMsgID, err = db.AddMessage("routine", "assistant",
		"Anyway, the french press makes a nice cup before 7am.", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := db.CreateThread("sqlite", "SQLite notes", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	exactMsgID, err = db.AddMessage("sqlite", "user",
		"what does sqlite do about busy_timeout", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	svc = newEmbedService(db, embed.NewOpenRouter("k", synonymEmbedServer(t).URL, "test-model"))
	if _, err := svc.IndexOnce(context.Background(), 100); err != nil {
		t.Fatalf("IndexOnce: %v", err)
	}
	return db, svc, morningMsgID, exactMsgID
}

// TestSearchChatsHybrid_SemanticOnlyHit is search_chats' version of the
// star case: the query shares no wording with the message it should find.
func TestSearchChatsHybrid_SemanticOnlyHit(t *testing.T) {
	db, svc, morningMsgID, _ := chatFixture(t)

	fts, err := db.SearchMessages("coffee", 30)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	for _, r := range fts {
		if r.MessageID == morningMsgID {
			t.Skip("fixture is degenerate: FTS already matches this message")
		}
	}

	results, err := svc.SearchChatsHybrid(context.Background(), "coffee", 10)
	if err != nil {
		t.Fatalf("SearchChatsHybrid: %v", err)
	}
	found := false
	for _, r := range results {
		if r.MessageID == morningMsgID {
			found = true
			// A semantic hit has no matched span, so its snippet must be
			// plain text — no \x02/\x03 markers for a caller to render as
			// highlights that don't exist.
			if strings.ContainsAny(r.Snippet, "\x02\x03") {
				t.Errorf("semantic snippet contains highlight markers: %q", r.Snippet)
			}
			if !strings.Contains(r.Snippet, "french press") {
				t.Errorf("semantic snippet = %q, want a preview of the message", r.Snippet)
			}
		}
	}
	if !found {
		t.Errorf("results = %+v, want the french-press message %d", results, morningMsgID)
	}
}

// TestSearchChatsHybrid_ExactMatchKeepsItsHighlight covers the other
// direction: a keyword hit keeps its highlighted snippet and its position.
func TestSearchChatsHybrid_ExactMatchKeepsItsHighlight(t *testing.T) {
	_, svc, _, exactMsgID := chatFixture(t)

	results, err := svc.SearchChatsHybrid(context.Background(), "busy_timeout", 10)
	if err != nil {
		t.Fatalf("SearchChatsHybrid: %v", err)
	}
	if len(results) == 0 || results[0].MessageID != exactMsgID {
		t.Fatalf("top result = %+v, want the busy_timeout message %d", results, exactMsgID)
	}
	if !strings.ContainsAny(results[0].Snippet, "\x02") {
		t.Errorf("keyword hit lost its highlight markers: %q", results[0].Snippet)
	}
}

// TestSearchChatsHybrid_NeverSurfacesAGhost is the privacy check at the
// retrieval layer: even with a vector somehow present for a ghost thread's
// message, a search must not return it.
func TestSearchChatsHybrid_NeverSurfacesAGhost(t *testing.T) {
	db := newEmbedTestStore(t)
	if err := db.CreateGhostThread("g", "incognito", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	id, err := db.AddMessage("g", "user", "espresso machine research", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	// Force the worst case: a vector for a ghost message.
	if err := db.UpsertEmbedding(store.EmbeddingKindMessage, id, "test-model", 4, embed.EncodeInt8([]float32{1, 0, 0, 0.01}), "espresso machine research"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	svc := newEmbedService(db, embed.NewOpenRouter("k", synonymEmbedServer(t).URL, "test-model"))
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	results, err := svc.SearchChatsHybrid(context.Background(), "espresso", 10)
	if err != nil {
		t.Fatalf("SearchChatsHybrid: %v", err)
	}
	for _, r := range results {
		if r.MessageID == id {
			t.Fatal("chat search returned a ghost thread's message")
		}
	}
}

// TestSearchChatsHybrid_NilServiceFallsBackToFTS pins the degradation path
// for the tool that has the most traffic.
func TestSearchChatsHybrid_NilServiceFallsBackToFTS(t *testing.T) {
	db, _, _, _ := chatFixture(t)
	want, err := db.SearchMessages("sqlite", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	got, err := searchChatsHybrid(nil, db, context.Background(), "sqlite", 10)
	if err != nil {
		t.Fatalf("searchChatsHybrid(nil): %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want the %d FTS-only ones", len(got), len(want))
	}
	for i := range got {
		if got[i].MessageID != want[i].MessageID {
			t.Errorf("result %d = %d, want %d", i, got[i].MessageID, want[i].MessageID)
		}
	}
}

func TestAboveFloor_DropsWeakHitsKeepsRankOrder(t *testing.T) {
	hits := []SimilarityHit{{RefID: 7, Score: 0.9}, {RefID: 3, Score: 0.32}, {RefID: 5, Score: 0.319}, {RefID: 1, Score: 0.1}}
	got := aboveFloor(hits, minMessageSimilarity)
	if len(got) != 2 || got[0] != 7 || got[1] != 3 {
		t.Errorf("aboveFloor = %v, want [7 3] (>= floor, rank order preserved)", got)
	}
	if got := aboveFloor(nil, minMessageSimilarity); got == nil || len(got) != 0 {
		t.Errorf("aboveFloor(nil) = %v, want empty non-nil", got)
	}
}
