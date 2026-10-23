package search

import (
	"strings"
	"testing"
)

func FuzzNormalizeDomain(f *testing.F) {
	f.Add("WWW.Example.COM.")
	f.Add("www.")
	f.Add("")
	f.Fuzz(func(t *testing.T, host string) {
		if got := normalizeDomain(host); got != strings.ToLower(got) {
			t.Errorf("normalizeDomain(%q) = %q is not lowercase", host, got)
		}
	})
}

// Blocked runs on every URL a search returns, including hostile ones.
func FuzzBlocklistBlocked(f *testing.F) {
	f.Add("https://en.blocked.com/a")
	f.Add("//blocked.com")
	f.Add("http://[::1]:80/")
	f.Add("%zz")
	bl := &Blocklist{domains: map[string]struct{}{"blocked.com": {}}}
	f.Fuzz(func(t *testing.T, raw string) {
		_ = bl.Blocked(raw)
	})
}
