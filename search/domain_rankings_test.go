package search

import (
	"os"
	"strings"
	"testing"
	"time"
)

func writeRankingsFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/domain_rankings.yaml"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing domain rankings file: %v", err)
	}
	return path
}

func TestLoadDomainRankings_ParsesStates(t *testing.T) {
	path := writeRankingsFile(t, "reddit.com: raise\npinterest.com: lower\nspam.example: block\npinned.example: pin\n")
	r := LoadDomainRankings(path)

	cases := []struct {
		url  string
		want RankState
	}{
		{"https://reddit.com/r/rust", RankRaise},
		{"https://old.reddit.com/r/rust", RankRaise}, // subdomain match
		{"https://pinterest.com/x", RankLower},
		{"https://spam.example/x", RankBlock},
		{"https://pinned.example/x", RankPin},
		{"https://unrelated.com/x", RankDefault},
	}
	for _, c := range cases {
		if got := r.State(c.url); got != c.want {
			t.Errorf("State(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestLoadDomainRankings_SubdomainOverridesParentDeterministically(t *testing.T) {
	path := writeRankingsFile(t, "reddit.com: block\nold.reddit.com: raise\n")
	r := LoadDomainRankings(path)

	// Run repeatedly — a bug where the first map-iteration match wins
	// would only fail nondeterministically across separate processes, so
	// a single run in-process wouldn't reliably catch it. The fix makes
	// this deterministic regardless of map iteration order, so it should
	// hold every time even within one process.
	for i := 0; i < 20; i++ {
		if got := r.State("https://old.reddit.com/r/rust"); got != RankRaise {
			t.Fatalf("run %d: State(old.reddit.com) = %q, want raise (more specific match should win over the parent domain's block)", i, got)
		}
		if got := r.State("https://reddit.com/r/rust"); got != RankBlock {
			t.Fatalf("run %d: State(reddit.com) = %q, want block", i, got)
		}
	}
}

func TestLoadDomainRankings_MissingPathIsEmpty(t *testing.T) {
	r := LoadDomainRankings("/nonexistent/domain_rankings.yaml")
	if r.State("https://anything.com") != RankDefault {
		t.Error("missing rankings file should report RankDefault for everything")
	}
}

func TestLoadDomainRankings_EmptyPathIsEmpty(t *testing.T) {
	r := LoadDomainRankings("")
	if r.State("https://anything.com") != RankDefault {
		t.Error("empty path should report RankDefault for everything")
	}
}

func TestDomainRankings_NilIsSafe(t *testing.T) {
	var r *DomainRankings
	if r.State("https://anything.com") != RankDefault {
		t.Error("nil *DomainRankings should report RankDefault")
	}
}

func TestLoadDomainRankings_DropsUnrecognizedStateButKeepsRest(t *testing.T) {
	path := writeRankingsFile(t, "good.com: raise\nbad.com: boost\n") // "boost" isn't a real state
	r := LoadDomainRankings(path)

	if r.State("https://good.com") != RankRaise {
		t.Error("valid entry should still load despite a sibling invalid one")
	}
	if r.State("https://bad.com") != RankDefault {
		t.Error("unrecognized state should be dropped, not applied")
	}
}

func TestLoadDomainRankings_HotReloadsOnFileChange(t *testing.T) {
	path := writeRankingsFile(t, "example.com: lower\n")
	if got := LoadDomainRankings(path).State("https://example.com"); got != RankLower {
		t.Fatalf("initial load: State = %q, want lower", got)
	}

	if err := os.WriteFile(path, []byte("example.com: pin\n"), 0o644); err != nil {
		t.Fatalf("rewriting rankings file: %v", err)
	}
	// Force a distinct, later mtime — back-to-back WriteFile calls can land
	// within the same filesystem mtime granularity, which would make this
	// test flaky instead of actually exercising the reload path.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("setting mtime: %v", err)
	}

	if got := LoadDomainRankings(path).State("https://example.com"); got != RankPin {
		t.Errorf("after hot-reload: State = %q, want pin", got)
	}
}

func TestSetDomainRanking_WritesAndTakesEffectImmediately(t *testing.T) {
	path := writeRankingsFile(t, "existing.com: lower\n")

	if err := SetDomainRanking(path, "reddit.com", RankRaise); err != nil {
		t.Fatalf("SetDomainRanking: %v", err)
	}

	// No mtime-wait needed — the cache is updated synchronously as part of
	// the write, not just on-disk (see SetDomainRanking's doc comment).
	if got := LoadDomainRankings(path).State("https://reddit.com"); got != RankRaise {
		t.Errorf("State(reddit.com) = %q, want raise (immediately after write, no reload delay)", got)
	}
	// The pre-existing entry must survive a read-modify-write, not just
	// whatever was set in this call.
	if got := LoadDomainRankings(path).State("https://existing.com"); got != RankLower {
		t.Errorf("State(existing.com) = %q, want lower (pre-existing entry should survive)", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if !strings.Contains(string(data), "reddit.com: raise") {
		t.Errorf("file contents = %q, want it to contain the new entry", data)
	}
}

func TestSetDomainRanking_DefaultRemovesEntry(t *testing.T) {
	path := writeRankingsFile(t, "reddit.com: raise\nother.com: pin\n")

	if err := SetDomainRanking(path, "reddit.com", RankDefault); err != nil {
		t.Fatalf("SetDomainRanking: %v", err)
	}

	if got := LoadDomainRankings(path).State("https://reddit.com"); got != RankDefault {
		t.Errorf("State(reddit.com) = %q, want default after clearing", got)
	}
	if got := LoadDomainRankings(path).State("https://other.com"); got != RankPin {
		t.Error("clearing one domain should not touch a sibling entry")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if strings.Contains(string(data), "reddit.com") {
		t.Errorf("file contents = %q, want reddit.com entry removed entirely, not written as \"default\"", data)
	}
}

func TestSetDomainRanking_CreatesFileIfMissing(t *testing.T) {
	path := t.TempDir() + "/domain_rankings.yaml"

	if err := SetDomainRanking(path, "reddit.com", RankPin); err != nil {
		t.Fatalf("SetDomainRanking: %v", err)
	}
	if got := LoadDomainRankings(path).State("https://reddit.com"); got != RankPin {
		t.Errorf("State(reddit.com) = %q, want pin", got)
	}
}

func TestSetDomainRanking_RejectsInvalidState(t *testing.T) {
	path := writeRankingsFile(t, "")
	if err := SetDomainRanking(path, "reddit.com", RankState("boost")); err == nil {
		t.Fatal("expected an error for an invalid rank state")
	}
}

func TestSetDomainRanking_RejectsEmptyPath(t *testing.T) {
	if err := SetDomainRanking("", "reddit.com", RankRaise); err == nil {
		t.Fatal("expected an error when no domain rankings file is configured")
	}
}
