package threadexport

import (
	"encoding/json"
	"strings"
)

// thinkingFromEvents extracts a turn's reasoning bursts from its events,
// in the order they were recorded. Reasoning lives only in the events
// table (messages.transcript has tool calls and results but no reasoning),
// one row per agent per burst, so a Deep Research turn can yield several
// entries with different Agent values.
func thinkingFromEvents(events []Event) []Thinking {
	var out []Thinking
	for _, e := range events {
		if e.Source != "turn" || e.Message != "reasoning" {
			continue
		}
		var data struct {
			Content    string `json:"content"`
			AgentID    string `json:"agent_id"`
			DurationMs int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
			continue
		}
		if strings.TrimSpace(data.Content) == "" {
			continue
		}
		out = append(out, Thinking{Agent: data.AgentID, Content: data.Content, DurationMs: data.DurationMs})
	}
	return out
}
