package search

import (
	"os"
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
