package oraclebench

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// ProductionTimeout mirrors gateway's oracleTimeout (2.5s): the point past which
// a real Oracle turn silently drops the decision. The bench's own scoring budget
// (weights.yaml, 5s) is deliberately looser, so the report draws both.
const ProductionTimeout = 2500 * time.Millisecond

// LatencyShare is one model's share of calls finishing inside the production
// timeout and the bench budget, counted over calls that returned an answer.
// It is computed from the raw per-call log (which is gitignored), then saved
// beside the committed leaderboard so the report can be regenerated without it.
type LatencyShare struct {
	Answered  int `json:"answered"`
	Under2_5s int `json:"under_2_5s"`
	Under5s   int `json:"under_5s"`
}

// callLine is the subset of a raw call record the latency tally needs.
type callLine struct {
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	Err       string `json:"err"`
}

// TallyLatency reads one or more raw calls JSONL files and counts, per model,
// how many successful calls fit inside the production timeout.
func TallyLatency(paths []string) (map[string]LatencyShare, error) {
	out := map[string]LatencyShare{}
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
			if err := json.Unmarshal([]byte(line), &c); err != nil || c.Err != "" {
				continue
			}
			s := out[c.Model]
			s.Answered++
			if time.Duration(c.LatencyMS)*time.Millisecond <= ProductionTimeout {
				s.Under2_5s++
			}
			if time.Duration(c.LatencyMS)*time.Millisecond <= 5*time.Second {
				s.Under5s++
			}
			out[c.Model] = s
		}
	}
	return out, nil
}
