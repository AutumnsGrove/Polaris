package oraclebench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ProductionTimeout mirrors gateway's oracleTimeout (2.5s): the point past which
// a real Oracle turn silently drops the decision. The bench's own scoring budget
// (weights.yaml, 5s) is deliberately looser, so the report draws both.
const ProductionTimeout = 2500 * time.Millisecond

// CallStats is what the report needs from one model's raw per-call log: how many
// calls were answered and how many of those fit inside the production timeout,
// plus how many were refused and which question families the refusals landed
// on. The log is gitignored, so this is computed once and saved beside the
// committed leaderboard, letting the report be regenerated without it.
type CallStats struct {
	Calls     int `json:"calls"`
	Answered  int `json:"answered"`
	Under2_5s int `json:"under_2_5s"`
	Under5s   int `json:"under_5s"`
	Refused   int `json:"refused"`
	Failed    int `json:"failed"` // every error kind: refusals, timeouts, upstream errors

	CallsByTrack   map[string]int `json:"calls_by_track,omitempty"`
	RefusedByTrack map[string]int `json:"refused_by_track,omitempty"`

	// CostUSDFresh and FreshCalls are the production cost basis recovered from
	// the raw log: the first send of each case's state (repeat 0), not the
	// average over repeats, which a provider's prefix cache makes cheaper and so
	// understates. They exist because a leaderboard JSON written before
	// fresh-cost capture has only the all-call total, while the raw log still
	// holds the repeat index — this is how an older run gets the honest figure
	// without being re-run. Only meaningful when a model's log is passed exactly
	// once: the tally adds logs together, so two runs of one model would
	// double-count (same rule as the other per-call tallies here).
	CostUSDFresh float64 `json:"cost_usd_fresh,omitempty"`
	FreshCalls   int     `json:"fresh_calls,omitempty"`
	// InputTokens and CachedTokens total the provider-reported split, when the
	// log has it (runs made after 2026-11-04 do).
	InputTokens  int `json:"input_tokens,omitempty"`
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// callLine is the subset of a raw call record the tally needs.
type callLine struct {
	Model     string  `json:"model"`
	Track     string  `json:"track"`
	Repeat    int     `json:"repeat"`
	LatencyMS int64   `json:"latency_ms"`
	Err       ErrKind `json:"err"`
	CostUSD   float64 `json:"cost_usd"`
	// Tokens are absent from logs written before 2026-11-04, which is why they
	// are optional and why the tally treats "no cache split" as unknown.
	InputTokens  int `json:"input_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

// TallyCalls reads one or more raw calls JSONL files and counts, per model,
// answered calls against the production timeout and refusals by track.
func TallyCalls(paths []string) (map[string]CallStats, error) {
	out := map[string]CallStats{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var c callLine
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue
			}
			s := out[c.Model]
			s.Calls++
			s.InputTokens += c.InputTokens
			s.CachedTokens += c.CachedTokens
			// Repeat 0 is the only call per case that sends a fresh state, so it is
			// the one whose cost matches a production turn.
			if c.Repeat == 0 {
				s.CostUSDFresh += c.CostUSD
				s.FreshCalls++
			}
			if s.CallsByTrack == nil {
				s.CallsByTrack = map[string]int{}
			}
			s.CallsByTrack[c.Track]++
			switch {
			case c.Err == ErrRefusal:
				s.Refused++
				s.Failed++
				if s.RefusedByTrack == nil {
					s.RefusedByTrack = map[string]int{}
				}
				s.RefusedByTrack[c.Track]++
			case c.Err != "":
				s.Failed++
			default:
				s.Answered++
				if d := time.Duration(c.LatencyMS) * time.Millisecond; d <= ProductionTimeout {
					s.Under2_5s++
				}
				if time.Duration(c.LatencyMS)*time.Millisecond <= 5*time.Second {
					s.Under5s++
				}
			}
			out[c.Model] = s
		}
	}
	return out, nil
}

// CostPer1k answers "what does one Oracle turn cost in production?" — the
// first send of a state, which is all production ever does. The average over
// repeats understates any model whose provider prefix-caches, since repeats
// 2..n of the identical state bill cheaper (morph/reflex bills ~11% less on a
// repeat). known is false when neither the leaderboard nor the raw-log tally
// has a fresh-cost figure, i.e. the run predates capturing it; callers mark
// those rows so two bases never share a column silently.
func CostPer1k(e Entry, shares map[string]CallStats) (usd float64, known bool) {
	if s := e.Score; s.FreshCalls > 0 {
		return s.FreshCostUSD / float64(s.FreshCalls) * 1000, true
	}
	if sh, ok := shares[e.Model]; ok && sh.FreshCalls > 0 {
		return sh.CostUSDFresh / float64(sh.FreshCalls) * 1000, true
	}
	if s := e.Score; s.Calls > 0 {
		return s.CostUSD / float64(s.Calls) * 1000, false
	}
	return 0, false
}

// MergeLeaderboards combines runs of different models into one leaderboard. The
// runs must have scored the same cases the same way (cases, repeats, concurrency
// and weights), otherwise their numbers would not be comparable and merging
// would quietly mislead. A model present in several runs takes the latest.
//
// allowConcurrencyMismatch relaxes only the concurrency check: accuracy doesn't
// depend on load, only latency does, and a model whose provider needs fewer
// calls in flight (Drex's split makes each call two requests) has a real reason
// to run lower. Such models are recorded in RanAtConcurrency so the report can
// flag their latency; cases, repeats and weights must still match.
func MergeLeaderboards(boards []Leaderboard, allowConcurrencyMismatch bool) (Leaderboard, error) {
	if len(boards) == 0 {
		return Leaderboard{}, errors.New("no leaderboards to merge")
	}
	out := boards[0]
	out.Entries = nil
	out.RanAtConcurrency = nil
	idx := map[string]int{}
	for i, b := range boards {
		concurrencyDiffers := b.Concurrency != out.Concurrency
		if b.Cases != out.Cases || b.Repeats != out.Repeats || b.WeightsHash != out.WeightsHash || (concurrencyDiffers && !allowConcurrencyMismatch) {
			return Leaderboard{}, fmt.Errorf("run %d (%s) used different settings (cases %d/%d, repeats %d/%d, concurrency %d/%d, weights %s/%s); its numbers are not comparable",
				i+1, b.RunAt, b.Cases, out.Cases, b.Repeats, out.Repeats, b.Concurrency, out.Concurrency, b.WeightsHash, out.WeightsHash)
		}
		for _, e := range b.Entries {
			// A later run of the same model replaces the earlier one, and with it
			// the earlier run's concurrency note.
			delete(out.RanAtConcurrency, e.Model)
			if concurrencyDiffers {
				if out.RanAtConcurrency == nil {
					out.RanAtConcurrency = map[string]int{}
				}
				out.RanAtConcurrency[e.Model] = b.Concurrency
			}
			if j, ok := idx[e.Model]; ok {
				out.Entries[j] = e
				continue
			}
			idx[e.Model] = len(out.Entries)
			out.Entries = append(out.Entries, e)
		}
	}
	return BuildLeaderboard(out), nil
}
