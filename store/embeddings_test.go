package store

import (
	"strings"
	"testing"
)

// embeddingFixture builds one of every thread shape the eligibility
// predicate is supposed to distinguish, all containing the same rare token
// so a single FTS query matches all of them and the two paths can be
// compared directly.
//
// Returns the thread ids that must be indexable/searchable.
type embeddingFixture struct {
	eligible []string
	all      []string
}

func buildEmbeddingFixture(t *testing.T, s *Store) embeddingFixture {
	t.Helper()
	const token = "quetzalcoatl"

	mk := func(id, title, source string, ghost bool) string {
		var err error
		if ghost {
			err = s.CreateGhostThread(id, title, "m", source)
		} else {
			err = s.CreateThread(id, title, "m", source)
		}
		if err != nil {
			t.Fatalf("creating thread %s: %v", id, err)
		}
		if _, err := s.AddMessage(id, "user", "tell me about "+token, "[]", "[]", 0, ""); err != nil {
			t.Fatalf("adding message to %s: %v", id, err)
		}
		return id
	}

	f := embeddingFixture{}
	f.eligible = append(f.eligible,
		mk("web", "web thread", "web", false),
	)
	mk("atlas-continued", "atlas continued", "atlas", false)
	if err := s.MarkThreadContinued("atlas-continued"); err != nil {
		t.Fatalf("MarkThreadContinued: %v", err)
	}
	f.eligible = append(f.eligible, "atlas-continued")

	// Everything below must be invisible to BOTH paths.
	mk("ghost", "ghost thread", "web", true)
	mk("atlas", "atlas never continued", "atlas", false)
	mk("pulsar", "pulsar routine", "pulsar", false)
	mk("weaver", "shooting star", "weaver", false)
	mk("comet", "comet approach", "comet_approach", false)
	mk("deleted", "soft deleted", "web", false)
	if err := s.DeleteThread("deleted"); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	// A Field that opted out of chat search.
	field, err := s.CreateField(Field{Name: "private", ExcludeFromChatSearch: true})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	optedOut := mk("opted-out", "opt-out thread", "web", false)
	if err := s.SetThreadField(optedOut, &field.ID); err != nil {
		t.Fatalf("SetThreadField: %v", err)
	}

	f.all = []string{"web", "atlas-continued", "ghost", "atlas", "pulsar", "weaver", "comet", "deleted", "opted-out"}
	return f
}

// TestEmbeddings_EligibilityParityWithSearch is the one test that matters
// most in this file. The semantic index and FTS search share one predicate
// (searchableThreadWhere); if they ever disagree, either a hidden thread
// leaks into results or an indexable one silently never gets indexed.
//
// Compared by thread id, not by row count, so a fixture that shrinks can't
// make it pass vacuously.
func TestEmbeddings_EligibilityParityWithSearch(t *testing.T) {
	s := openTestStore(t)
	f := buildEmbeddingFixture(t, s)

	const model = "test-embed-model"
	embeddable, err := s.MessagesNeedingEmbedding(model, 0, 100)
	if err != nil {
		t.Fatalf("MessagesNeedingEmbedding: %v", err)
	}

	searchHits, err := s.SearchMessages("quetzalcoatl", 100)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}

	// Index the embeddable ones, then re-query: nothing should be left, and
	// the count must equal the eligible set — i.e. every searchable message
	// was indexable.
	for _, m := range embeddable {
		if err := s.UpsertEmbedding(EmbeddingKindMessage, m.ID, model, 4, []byte{1, 2, 3, 4}, m.Text); err != nil {
			t.Fatalf("UpsertEmbedding: %v", err)
		}
	}
	remaining, err := s.MessagesNeedingEmbedding(model, 0, 100)
	if err != nil {
		t.Fatalf("MessagesNeedingEmbedding (second pass): %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d messages still need embedding after indexing them all", len(remaining))
	}

	var searchThreads []string
	seen := map[string]bool{}
	for _, h := range searchHits {
		if !seen[h.ThreadID] {
			seen[h.ThreadID] = true
			searchThreads = append(searchThreads, h.ThreadID)
		}
	}

	want := map[string]bool{}
	for _, id := range f.eligible {
		want[id] = true
	}
	got := map[string]bool{}
	for _, id := range searchThreads {
		got[id] = true
	}
	if len(got) != len(want) {
		t.Fatalf("searchable threads = %v, want exactly %v", searchThreads, f.eligible)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("eligible thread %q is not searchable", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("thread %q is searchable but should not be", id)
		}
	}

	// And the indexed count must agree with the eligible count the coverage
	// number reports.
	cov, err := s.EmbeddingCoverage(EmbeddingKindMessage, model)
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.Eligible != len(embeddable) || cov.Indexed != len(embeddable) {
		t.Errorf("coverage = %+v, want eligible=indexed=%d", cov, len(embeddable))
	}
	if cov.StaleModel != 0 {
		t.Errorf("StaleModel = %d, want 0", cov.StaleModel)
	}
}

// TestEmbeddings_NoGhostRowEverWritten pins the first belt on the ghost
// hazard: a ghost thread's messages must be invisible to the indexer's own
// query, not merely filtered later.
func TestEmbeddings_NoGhostRowEverWritten(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g", "incognito", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	id, err := s.AddMessage("g", "user", "something I want forgotten", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := s.MessagesNeedingEmbedding("m", 0, 100)
	if err != nil {
		t.Fatalf("MessagesNeedingEmbedding: %v", err)
	}
	for _, m := range msgs {
		if m.ID == id {
			t.Fatal("a ghost thread's message was offered for embedding")
		}
	}

	cov, err := s.EmbeddingCoverage(EmbeddingKindMessage, "m")
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.Eligible != 0 {
		t.Errorf("Eligible = %d, want 0 with only a ghost thread present", cov.Eligible)
	}
}

// TestEmbeddings_GhostHardDeleteRemovesVectors pins the second belt. The
// embeddings table has no FK to messages, so nothing cascades — without the
// explicit purge in deleteGhostThreads' transaction, a forgotten
// conversation's vectors would survive on disk.
func TestEmbeddings_GhostHardDeleteRemovesVectors(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g", "incognito", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	msgID, err := s.AddMessage("g", "user", "secret plan", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	// Simulate the worst case: a vector got written anyway (an indexer race,
	// or a row inserted by a future caller that forgets the ghost check).
	if err := s.UpsertEmbedding(EmbeddingKindMessage, msgID, "m", 4, []byte{1, 2, 3, 4}, "secret plan"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	if err := s.DeleteThreadPermanently("g"); err != nil {
		t.Fatalf("DeleteThreadPermanently: %v", err)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&n); err != nil {
		t.Fatalf("counting embeddings: %v", err)
	}
	if n != 0 {
		t.Errorf("%d embedding rows survived a ghost thread hard-delete, want 0", n)
	}
}

// TestEmbeddings_PurgeIneligible covers the self-healing sweep: rows for a
// thread that became ineligible after indexing (soft-deleted, moved into an
// opted-out Field) must be removed, and eligible ones left alone.
func TestEmbeddings_PurgeIneligible(t *testing.T) {
	s := openTestStore(t)
	f := buildEmbeddingFixture(t, s)

	const model = "m"
	// Index every message in the fixture, including the ineligible ones, as
	// if they had been indexed before the rules changed.
	rows, err := s.db.Query(`SELECT id, content FROM messages`)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	type msg struct {
		id      int64
		content string
	}
	var all []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.content); err != nil {
			t.Fatalf("scanning message: %v", err)
		}
		all = append(all, m)
	}
	rows.Close()
	for _, m := range all {
		if err := s.UpsertEmbedding(EmbeddingKindMessage, m.id, model, 4, []byte{1, 2, 3, 4}, m.content); err != nil {
			t.Fatalf("UpsertEmbedding: %v", err)
		}
	}

	purged, err := s.PurgeIneligibleEmbeddings()
	if err != nil {
		t.Fatalf("PurgeIneligibleEmbeddings: %v", err)
	}
	if purged == 0 {
		t.Fatal("purged 0 rows, want the ineligible ones removed")
	}

	cov, err := s.EmbeddingCoverage(EmbeddingKindMessage, model)
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.Indexed != len(f.eligible) {
		t.Errorf("Indexed = %d after purge, want %d (one per eligible thread)", cov.Indexed, len(f.eligible))
	}

	// Idempotent: a second pass has nothing left to do.
	if again, err := s.PurgeIneligibleEmbeddings(); err != nil || again != 0 {
		t.Errorf("second purge = %d, %v; want 0, nil", again, err)
	}
}

func TestEmbeddings_StarNeedsReembeddingOnlyWhenTextChanges(t *testing.T) {
	s := openTestStore(t)
	const model = "m"

	starID, err := s.CreateStar(Star{Title: "Coffee", Summary: "likes it black", Body: "every morning", Status: "auto", Category: "personal"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	needing, err := s.StarsNeedingEmbedding(model)
	if err != nil {
		t.Fatalf("StarsNeedingEmbedding: %v", err)
	}
	if len(needing) != 1 || needing[0].ID != starID {
		t.Fatalf("first pass = %+v, want the one star", needing)
	}

	if err := s.UpsertEmbedding(EmbeddingKindStar, starID, model, 4, []byte{1, 2, 3, 4}, needing[0].Text); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	if again, err := s.StarsNeedingEmbedding(model); err != nil || len(again) != 0 {
		t.Errorf("after indexing = %+v, %v; want nothing", again, err)
	}

	// Editing the body must make it eligible again — the hash is what
	// catches a star whose text moved on, unlike an immutable message.
	body := "every morning, two cups"
	if err := s.UpdateStar(starID, "Coffee", "likes it black", body, nil, "", nil, "manual_edit"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	if again, err := s.StarsNeedingEmbedding(model); err != nil || len(again) != 1 {
		t.Errorf("after edit = %+v, %v; want the star again", again, err)
	}
}

func TestEmbeddings_StarPurgeSkipsDisabled(t *testing.T) {
	s := openTestStore(t)
	starID, err := s.CreateStar(Star{Title: "T", Summary: "s", Body: "b", Status: "auto", Category: "c"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	if err := s.UpsertEmbedding(EmbeddingKindStar, starID, "m", 4, []byte{1, 2, 3, 4}, "T\n\ns\n\nb"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	if err := s.SetStarDisabled(starID, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	if _, err := s.PurgeIneligibleEmbeddings(); err != nil {
		t.Fatalf("PurgeIneligibleEmbeddings: %v", err)
	}
	cov, err := s.EmbeddingCoverage(EmbeddingKindStar, "m")
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.Indexed != 0 || cov.Eligible != 0 {
		t.Errorf("coverage = %+v, want 0/0 with the only star disabled", cov)
	}
}

func TestEmbeddings_LoadExcludesOtherModels(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t", "title", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	id, err := s.AddMessage("t", "user", "hello", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.UpsertEmbedding(EmbeddingKindMessage, id, "old-model", 4, []byte{1, 2, 3, 4}, "hello"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	rows, err := s.LoadEmbeddings(EmbeddingKindMessage, "new-model")
	if err != nil {
		t.Fatalf("LoadEmbeddings: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("loaded %d vectors from a different model, want 0 (mixing them would silently mean 'nothing matches')", len(rows))
	}

	// The stale row is still visible to coverage, which is what drives the
	// settings panel's "needs reindex" warning.
	cov, err := s.EmbeddingCoverage(EmbeddingKindMessage, "new-model")
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if cov.StaleModel != 1 {
		t.Errorf("StaleModel = %d, want 1", cov.StaleModel)
	}

	// Re-embedding under the new model replaces the row rather than adding a
	// second one (UNIQUE kind, ref_id).
	if err := s.UpsertEmbedding(EmbeddingKindMessage, id, "new-model", 4, []byte{5, 6, 7, 8}, "hello"); err != nil {
		t.Fatalf("UpsertEmbedding (new model): %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE ref_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows for one ref_id after re-embedding, want 1", n)
	}
}

func TestEmbeddings_UpsertValidation(t *testing.T) {
	s := openTestStore(t)
	cases := []struct {
		name string
		kind string
		ref  int64
		mod  string
		dims int
		vec  []byte
	}{
		{"no kind", "", 1, "m", 4, []byte{1, 2, 3, 4}},
		{"no ref", EmbeddingKindMessage, 0, "m", 4, []byte{1, 2, 3, 4}},
		{"no model", EmbeddingKindMessage, 1, "", 4, []byte{1, 2, 3, 4}},
		{"no dims", EmbeddingKindMessage, 1, "m", 0, []byte{1, 2, 3, 4}},
		{"no vec", EmbeddingKindMessage, 1, "m", 4, nil},
		{"dims mismatch", EmbeddingKindMessage, 1, "m", 8, []byte{1, 2, 3, 4}},
	}
	for _, c := range cases {
		if err := s.UpsertEmbedding(c.kind, c.ref, c.mod, c.dims, c.vec, "text"); err == nil {
			t.Errorf("%s: UpsertEmbedding accepted an invalid row", c.name)
		}
	}
}

// TestEmbeddings_StarTextIncludesBody guards the one deliberate divergence
// from stars_fts: the body is where a star's substance is, and dropping it
// would make "what do you know about X" miss stars whose title is a label.
func TestEmbeddings_StarTextIncludesBody(t *testing.T) {
	text := starEmbeddingText("Morning Routine", "coffee and a walk", "two cups, black, before 7am")
	for _, want := range []string{"Morning Routine", "coffee and a walk", "two cups, black"} {
		if !strings.Contains(text, want) {
			t.Errorf("star text %q missing %q", text, want)
		}
	}
}
