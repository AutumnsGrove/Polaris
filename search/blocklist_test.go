package search

import "testing"

func TestLoadBlocklist_ParsesDomainsAndComments(t *testing.T) {
	path := writeBlocklistFile(t, `
# a full-line comment
grokipedia.com
Example.com   # trailing comment, mixed case

   spaced.com
`)
	bl, err := LoadBlocklist(path)
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}
	for _, url := range []string{
		"https://grokipedia.com/x",
		"https://en.grokipedia.com/x",
		"https://example.com/x",
		"https://spaced.com/x",
	} {
		if !bl.Blocked(url) {
			t.Errorf("Blocked(%q) = false, want true", url)
		}
	}
	if bl.Blocked("https://not-blocked.com/x") {
		t.Error("Blocked(unrelated domain) = true, want false")
	}
}

func TestLoadBlocklist_MissingFileIsNotAnError(t *testing.T) {
	bl, err := LoadBlocklist("/nonexistent/path/blocked_sources.txt")
	if err != nil {
		t.Fatalf("LoadBlocklist returned error for a missing file: %v", err)
	}
	if bl.Blocked("https://anything.com") {
		t.Error("an empty blocklist blocked a URL")
	}
}

func TestLoadBlocklist_EmptyPathIsNotAnError(t *testing.T) {
	bl, err := LoadBlocklist("")
	if err != nil {
		t.Fatalf("LoadBlocklist(\"\") returned error: %v", err)
	}
	if bl.Blocked("https://anything.com") {
		t.Error("an empty-path blocklist blocked a URL")
	}
}

func TestBlocklist_NilIsSafe(t *testing.T) {
	var bl *Blocklist
	if bl.Blocked("https://grokipedia.com") {
		t.Error("nil *Blocklist reported a block")
	}
}

func TestBlocklist_MatchesTrailingDotFQDN(t *testing.T) {
	// "grokipedia.com." (a root-relative FQDN, trailing dot) is
	// DNS-equivalent to "grokipedia.com" and resolves/fetches identically —
	// it must not be a way to slip past the blocklist.
	bl, err := LoadBlocklist(writeBlocklistFile(t, "grokipedia.com\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}
	if !bl.Blocked("https://grokipedia.com./page") {
		t.Error("Blocked(trailing-dot FQDN) = false, want true")
	}
	if !bl.Blocked("https://www.grokipedia.com./page") {
		t.Error("Blocked(www + trailing-dot FQDN) = false, want true")
	}
}

func TestBlocklist_DoesNotMatchUnrelatedSuffix(t *testing.T) {
	bl, err := LoadBlocklist(writeBlocklistFile(t, "wiki.com\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}
	// "notwiki.com" ends in "wiki.com" as a raw string, but isn't a
	// subdomain of it — must not match.
	if bl.Blocked("https://notwiki.com") {
		t.Error("Blocked(\"notwiki.com\") = true, want false (not a subdomain of wiki.com)")
	}
}
