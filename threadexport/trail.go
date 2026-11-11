package threadexport

import (
	"encoding/json"
	"strings"
)

// buildTrail returns a turn's research trail. The transcript is preferred
// because it is permanent and untruncated; a turn that predates transcripts
// falls back to reconstructing from the events log. A transcript that fails
// to parse also falls back rather than dropping the trail silently.
func buildTrail(transcript string, events []Event) []TraceStep {
	if transcript != "" {
		if steps := trailFromTranscript(transcript); steps != nil {
			return steps
		}
	}
	return trailFromEvents(events)
}

// trailFromTranscript normalizes a stored wire transcript into ordered
// tool-call steps. Returns nil (not an empty slice) on a decode failure so
// the caller can tell "unparseable, try events" from "genuinely no tools".
func trailFromTranscript(transcript string) []TraceStep {
	var wire []struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls,omitempty"`
		ToolCallID string `json:"tool_call_id,omitempty"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(transcript), &wire); err != nil {
		return nil
	}

	// Results keyed first, then calls emitted in order: a call and its
	// result are separate wire messages, and results can arrive out of
	// order when calls ran concurrently.
	results := map[string]string{}
	for _, m := range wire {
		if m.Role == "tool" && m.ToolCallID != "" {
			results[m.ToolCallID] = m.Content
		}
	}

	steps := []TraceStep{}
	for _, m := range wire {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				continue
			}
			steps = append(steps, TraceStep{
				Tool:   tc.Function.Name,
				Args:   tc.Function.Arguments,
				Result: results[tc.ID],
			})
		}
	}
	return steps
}

// trailFromEvents reconstructs tool steps from the audit log — the
// pre-transcript path. A started call with no matching finished event is
// still emitted (with an empty result) rather than dropped, since a turn
// that crashed mid-call is exactly the kind of gap an audit wants surfaced.
func trailFromEvents(events []Event) []TraceStep {
	type pending struct {
		tool, callID, args string
		result             string
		done               bool
	}
	var calls []*pending
	for _, e := range events {
		if !strings.HasPrefix(e.Source, "tool.") {
			continue
		}
		var data struct {
			Args   map[string]any `json:"args"`
			Result string         `json:"result"`
			CallID string         `json:"call_id"`
		}
		if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
			continue
		}
		tool := strings.TrimPrefix(e.Source, "tool.")
		switch e.Message {
		case "tool call started":
			args := "{}"
			if len(data.Args) > 0 {
				if b, err := json.Marshal(data.Args); err == nil {
					args = string(b)
				}
			}
			calls = append(calls, &pending{tool: tool, callID: data.CallID, args: args})
		case "tool call finished":
			// Match the most recent unfinished call with the same tool and
			// call id: several calls can share a tool, and concurrent calls
			// can finish out of order.
			for i := len(calls) - 1; i >= 0; i-- {
				c := calls[i]
				if !c.done && c.tool == tool && c.callID == data.CallID {
					c.result = data.Result
					c.done = true
					break
				}
			}
		}
	}

	steps := make([]TraceStep, 0, len(calls))
	for _, c := range calls {
		steps = append(steps, TraceStep{Tool: c.tool, Args: c.args, Result: c.result})
	}
	return steps
}

// privacy holds the tool-name sets the two privacy toggles suppress, filled
// by the caller from the tool catalog.
type privacy struct {
	memory   map[string]bool
	personal map[string]bool
}

// filter drops steps whose tool a enabled privacy toggle suppresses,
// returning the kept steps plus the distinct tool names it removed so the
// caller can record an omission. Reverting this filter must make the
// privacy regression test fail — that is the point of the test.
func (p privacy) filter(steps []TraceStep, opts Options) (kept []TraceStep, memoryDropped, personalDropped []string) {
	for _, s := range steps {
		if opts.Private && p.memory[s.Tool] {
			memoryDropped = appendUnique(memoryDropped, s.Tool)
			continue
		}
		if opts.PrivatePersonal && p.personal[s.Tool] {
			personalDropped = appendUnique(personalDropped, s.Tool)
			continue
		}
		kept = append(kept, s)
	}
	return kept, memoryDropped, personalDropped
}

// appendUnique adds s to list if not already present, keeping insertion
// order so the omission manifest reads in the order tools actually ran.
func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}
