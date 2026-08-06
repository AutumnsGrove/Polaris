package search

import (
	"bufio"
	"net/url"
	"os"
	"strings"

	"polaris/logger"
)

var blocklistLog = logger.WithPrefix("search")

// Blocklist is a set of domains whose search results and page reads are
// rejected outright — for sources that are unreliable by construction
// (e.g. an AI-generated encyclopedia), not merely low-ranked.
type Blocklist struct {
	domains map[string]struct{}
}

// LoadBlocklist reads one domain per line from path — blank lines and "#"
// comments (whole-line or trailing) are ignored. A missing file is not an
// error, just an empty blocklist, so deployments that don't need this can
// leave it absent entirely rather than requiring a config change.
func LoadBlocklist(path string) (*Blocklist, error) {
	bl := &Blocklist{domains: map[string]struct{}{}}
	if path == "" {
		return bl, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return bl, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		bl.domains[normalizeDomain(line)] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	blocklistLog.Info("loaded source blocklist", "path", path, "domains", len(bl.domains))
	return bl, nil
}

func normalizeDomain(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}

// Blocked reports whether rawURL's host is on the blocklist — an exact
// match or a subdomain of a blocked entry, so blocking "grokipedia.com"
// also covers "www.grokipedia.com" and "en.grokipedia.com". Safe to call
// on a nil *Blocklist (always reports false) so callers that don't wire
// one up don't need their own nil checks.
func (b *Blocklist) Blocked(rawURL string) bool {
	if b == nil || len(b.domains) == 0 {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := normalizeDomain(u.Hostname())
	for d := range b.domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
