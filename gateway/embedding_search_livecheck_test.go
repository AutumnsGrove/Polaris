//go:build livecheck

package gateway

// Real-data check for hybrid star retrieval — the "does this actually find
// things keyword search can't, on a real library" step. Needs a real API key
// and a real polaris.db, so it's gated behind the `livecheck` build tag and
// never runs in CI:
//
//	OPENROUTER_API_KEY=sk-or-... POLARIS_EMBED_LIVECHECK_DB=/path/to/polaris.db \
//	  go test -tags livecheck ./gateway/ -run TestHybridStarsLiveCheck -v
//
// It indexes every star in the given database (a few cents at most for a
// real library), then runs a small set of deliberately paraphrase-y queries
// and reports, per query, what FTS found vs what fusion found. Nothing is
// asserted about quality — the point is to watch the two tiers disagree on
// real data, which is what docs/plans/embeddings.md's probe did for chat
// messages and what a star index has to reproduce.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"polaris/embed"
	"polaris/store"
)

func TestHybridStarsLiveCheck(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	dbPath := os.Getenv("POLARIS_EMBED_LIVECHECK_DB")
	if key == "" || dbPath == "" {
		t.Skip("set OPENROUTER_API_KEY and POLARIS_EMBED_LIVECHECK_DB")
	}

	// Work on a copy: the check writes embeddings into the database, and
	// pointing it at a live one by accident should not be possible.
	src := filepath.Join(t.TempDir(), "livecheck.db")
	if err := copyFile(dbPath, src); err != nil {
		t.Fatalf("copying %s: %v", dbPath, err)
	}
	db, err := store.Open(src)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	client := embed.NewOpenRouter(key, "", "")
	svc := newEmbedService(db, client)
	svc.model = client.Model()

	progress, err := svc.Backfill(context.Background(), store.EmbeddingKindStar, 0, nil)
	if err != nil {
		t.Fatalf("star backfill: %v", err)
	}
	t.Logf("indexed %d stars for $%.6f", progress.Stars, progress.CostUSD)

	cov, err := db.EmbeddingCoverage(store.EmbeddingKindStar, svc.model)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	t.Logf("coverage: %d / %d eligible", cov.Indexed, cov.Eligible)
	if cov.Eligible > 0 && cov.Indexed != cov.Eligible {
		t.Errorf("indexed %d of %d eligible stars", cov.Indexed, cov.Eligible)
	}

	// Chat side, too: message-level fusion is the same helper, but on the
	// surface with the most traffic, and the plan's measured FTS5 failures
	// were all chat queries.
	msgProgress, err := svc.Backfill(context.Background(), store.EmbeddingKindMessage, 0, nil)
	if err != nil {
		t.Fatalf("message backfill: %v", err)
	}
	t.Logf("indexed %d messages for $%.6f", msgProgress.Messages, msgProgress.CostUSD)

	chatQueries := []string{
		"computer less powerful than a raspberry pi that I deploy things on",
		"how do astronauts use the bathroom in space",
		"fiction about AI companions called AFs",
		"astronauts",
	}
	for _, q := range chatQueries {
		fts, err := db.SearchMessages(q, 3)
		if err != nil {
			t.Fatalf("FTS chat search %q: %v", q, err)
		}
		hybrid, err := svc.SearchChatsHybrid(context.Background(), q, 3)
		if err != nil {
			t.Fatalf("hybrid chat search %q: %v", q, err)
		}
		fmt.Printf("\nQ: %s\n  FTS (%d): %s\n  HYB (%d): %s\n",
			q, len(fts), chatSummary(fts), len(hybrid), chatSummary(hybrid))
	}

	queries := []string{
		"what do I like to drink in the morning",
		"how does she keep her notes organized",
		"places to eat near that jazz club",
		"things I am learning about databases",
		"music that sounds like longing",
	}
	for _, q := range queries {
		fts, err := db.SearchLibraryStars(q, 5)
		if err != nil {
			t.Fatalf("FTS search %q: %v", q, err)
		}
		hybrid, err := svc.SearchLibraryStarsHybrid(context.Background(), q, 5)
		if err != nil {
			t.Fatalf("hybrid search %q: %v", q, err)
		}
		fmt.Printf("\nQ: %s\n  FTS (%d): %s\n  HYB (%d): %s\n", q, len(fts), titlesOf(fts), len(hybrid), starTitlesOf(hybrid))
	}
}

// chatSummary renders message hits as "thread title :: snippet prefix", long
// enough to judge relevance at a glance.
func chatSummary(results []store.MessageSearchResult) string {
	if len(results) == 0 {
		return "(no hits)"
	}
	out := ""
	for i, r := range results {
		if i > 0 {
			out += " | "
		}
		snippet := r.Snippet
		if len(snippet) > 60 {
			snippet = snippet[:60]
		}
		out += fmt.Sprintf("%s :: %s", r.ThreadTitle, snippet)
	}
	return out
}

func titlesOf(stars []store.Star) string {
	if len(stars) == 0 {
		return "(no hits)"
	}
	out := ""
	for i, s := range stars {
		if i > 0 {
			out += " | "
		}
		out += s.Title
	}
	return out
}

func starTitlesOf(stars []store.Star) string { return titlesOf(stars) }

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}
