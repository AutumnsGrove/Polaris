package search

import (
	"fmt"
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

// domainRankingsHeader is prepended to every SetDomainRanking-written file
// — this file is both hand-editable and UI-written, unlike
// blocked_sources.txt (hand-editable only), so a reminder of the valid
// states belongs in the file itself, not just in docs. A round-trip
// through yaml.Marshal on a plain map can't preserve a human's own
// comments, though — a UI edit will silently drop any hand-added ones.
// Acceptable for v1 (see docs/plans/local-search-frontend.md); revisit if
// that turns out to matter in practice.
const domainRankingsHeader = "# domain_rankings.yaml — hand-editable, or written by Atlas's ranking popover.\n" +
	"# State: block | lower | raise | pin. Omitted domains are implicitly \"default\".\n"

// SetDomainRanking sets domain's ranking state in the file at path,
// creating it if it doesn't exist yet. RankDefault removes the entry
// entirely rather than writing it explicitly — omitting a domain already
// means default, so there's nothing to persist. Read-modify-write happens
// under rankingsMu (the same lock LoadDomainRankings' cache uses) so a
// popover click can't race a concurrent load or another click into
// silently dropping one of them, and the in-memory cache is updated
// immediately afterward so the very next search reflects the change
// without waiting on filesystem mtime resolution (coarse — as little as
// 1-second granularity on some filesystems — to notice the write).
func SetDomainRanking(path, domain string, state RankState) error {
	if path == "" {
		return fmt.Errorf("no domain rankings file configured")
	}
	if !state.valid() {
		return fmt.Errorf("invalid rank state %q", state)
	}
	domain = normalizeDomain(domain)
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	rankingsMu.Lock()
	defer rankingsMu.Unlock()

	raw := map[string]RankState{}
	if data, err := os.ReadFile(path); err == nil {
		// Tolerate a stale/corrupt on-disk file here — this write is about
		// to replace it with a valid one regardless, and failing outright
		// would mean one hand-edit typo permanently blocks the popover
		// from ever writing again.
		_ = yaml.Unmarshal(data, &raw)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if state == RankDefault {
		delete(raw, domain)
	} else {
		raw[domain] = state
	}

	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encoding domain rankings: %w", err)
	}
	if err := os.WriteFile(path, append([]byte(domainRankingsHeader), data...), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s after write: %w", path, err)
	}
	states := make(map[string]RankState, len(raw))
	for d, s := range raw {
		states[normalizeDomain(d)] = s
	}
	rankingsCache[path] = &rankingsCacheEntry{parsed: &DomainRankings{states: states}, modTime: info.ModTime()}

	blocklistLog.Info("updated domain ranking", "path", path, "domain", domain, "state", state)
	return nil
}
