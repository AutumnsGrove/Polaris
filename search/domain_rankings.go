package search

import (
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// RankState is one of the five states a domain can be assigned via the
// ranking popover UI. Distinct from Blocklist: that's a maintainer-curated
// "unreliable by construction" list (grokipedia.com and friends) shared by
// search and web_read, whereas DomainRankings holds the user's own
// per-domain search-ranking preferences — see
// docs/plans/local-search-frontend.md for the full design.
type RankState string

const (
	RankBlock   RankState = "block"
	RankLower   RankState = "lower"
	RankDefault RankState = "default"
	RankRaise   RankState = "raise"
	RankPin     RankState = "pin"
)

// raiseMultiplier and lowerMultiplier scale a result's fused RRF score.
// Starting points per the plan doc, meant to be tuned by feel once this
// is running against real queries rather than derived analytically.
const (
	raiseMultiplier = 1.75
	lowerMultiplier = 0.5
)

func (s RankState) valid() bool {
	switch s {
	case RankBlock, RankLower, RankDefault, RankRaise, RankPin:
		return true
	default:
		return false
	}
}

// DomainRankings is a hand-editable, hot-reloaded domain -> RankState map —
// loaded the same way prompts.Get() reloads prompts.yaml: re-parsed only
// when the file's mtime changes, so the ranking popover UI (which writes
// to this file) and hand edits both take effect on the next search with no
// restart needed.
type DomainRankings struct {
	states map[string]RankState
}

// State reports rawURL's host's ranking state — RankDefault if the host
// isn't in the map, on parse failure, or on a nil *DomainRankings, so
// callers that don't wire one up don't need their own nil checks. Matches
// Blocklist.Blocked's subdomain semantics: a state set on "reddit.com"
// also applies to "old.reddit.com".
func (d *DomainRankings) State(rawURL string) RankState {
	if d == nil || len(d.states) == 0 {
		return RankDefault
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return RankDefault
	}
	host := normalizeDomain(u.Hostname())
	for h, s := range d.states {
		if host == h || strings.HasSuffix(host, "."+h) {
			return s
		}
	}
	return RankDefault
}

var (
	rankingsMu    sync.Mutex
	rankingsCache = map[string]*rankingsCacheEntry{}
)

type rankingsCacheEntry struct {
	parsed  *DomainRankings
	modTime time.Time
}

// LoadDomainRankings returns the current rankings for path, re-reading and
// re-parsing only when the file's mtime has changed since the last call.
// A missing, unreadable, or malformed file falls back to the last
// successfully parsed rankings (or an empty set on the very first call) —
// same degrade-gracefully behavior as prompts.Get(), so a hand-edit typo
// doesn't wipe out ranking behavior mid-session. Unrecognized RankState
// values in the file are dropped individually (with a log warning) rather
// than failing the whole load.
func LoadDomainRankings(path string) *DomainRankings {
	rankingsMu.Lock()
	defer rankingsMu.Unlock()

	entry := rankingsCache[path]

	if path == "" {
		if entry == nil {
			return &DomainRankings{}
		}
		return entry.parsed
	}

	info, err := os.Stat(path)
	if err != nil {
		if entry == nil {
			return &DomainRankings{}
		}
		return entry.parsed
	}
	if entry != nil && info.ModTime().Equal(entry.modTime) {
		return entry.parsed
	}

	data, err := os.ReadFile(path)
	if err != nil {
		blocklistLog.Warn("reading domain rankings failed, using last-known rankings", "path", path, "err", err)
		if entry == nil {
			return &DomainRankings{}
		}
		return entry.parsed
	}

	var raw map[string]RankState
	if err := yaml.Unmarshal(data, &raw); err != nil {
		blocklistLog.Warn("parsing domain rankings failed, using last-known rankings", "path", path, "err", err)
		if entry == nil {
			return &DomainRankings{}
		}
		return entry.parsed
	}

	states := make(map[string]RankState, len(raw))
	for domain, state := range raw {
		if !state.valid() {
			blocklistLog.Warn("ignoring unrecognized domain ranking state", "domain", domain, "state", state)
			continue
		}
		states[normalizeDomain(domain)] = state
	}

	parsed := &DomainRankings{states: states}
	rankingsCache[path] = &rankingsCacheEntry{parsed: parsed, modTime: info.ModTime()}
	blocklistLog.Info("loaded domain rankings", "path", path, "domains", len(parsed.states))
	return parsed
}
