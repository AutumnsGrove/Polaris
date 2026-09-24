package gateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/store"
)

// replayedCall is one tool call rebuilt from the events log, paired with
// the result it returned — legacy turns only, see loadHistory.
type replayedCall struct {
	tool   string
	args   string // JSON-encoded, as ToolCall.Function.Arguments expects
	result string
}

// loadHistory builds the model's view of a thread's earlier turns: every
// turn exactly as it went over the wire the first time, the way any other
// chat product sends a conversation. Each assistant message stores its
// turn's own transcript (agent.Result.Transcript — the model-facing user
// message, every tool-call round with its original ids and batching, full
// tool results, nudges, image messages, the final answer), and this just
// concatenates them. Nothing is reassembled, so a stable run of earlier
// turns is byte-identical from one request to the next and the provider's
// exact-prefix cache covers everything up to the newest turn's own answer.
// See docs/plans/verbatim-turn-transcripts.md for why this replaced a
// reconstruction step: rebuilt history dropped the pages the model had
// actually read (so it could see it had cited a source but not what the
// source said), and differed from what was originally sent in enough ways
// that cross-turn caching almost never hit.
//
// A turn's user message is skipped whenever its assistant message carries
// a transcript, since the transcript opens with the model-facing version
// of it (attachment notes and all), not the bare text the UI shows.
//
// Turns from before transcripts existed (Transcript == "") fall back to
// the older reconstruction: that turn's tool calls rebuilt from the events
// log — each result capped at store's maxEventDataBytes, gone entirely
// once past the events' 90-day retention (store.PruneEvents) — ahead of
// its answer, which carries store.appendCitedSources' source note. A
// thread can mix both shapes: an old thread continued today gets verbatim
// turns from here on. The legacy part is still deterministic (no
// per-request filtering), so it caches too once it's been sent once.
//
// Legacy tool call ids are regenerated rather than reused: the logged
// call_id is whatever the provider issued at the time, and sub-agent calls
// (spawn_researchers shares its parent's Emit, so their events land in the
// same turn) can reuse the same ids as the parent's own calls. Every
// replayed call gets its own assistant message immediately followed by its
// own result — never a multi-call batch — so the wire protocol's "every
// tool_calls message is followed by all of its results" rule holds by
// construction.
func (s *Server) loadHistory(threadID string) ([]llm.ChatMessage, error) {
	entries, err := s.historyEntries(threadID, 0)
	if err != nil {
		return nil, err
	}

	// Decode up front so a corrupt transcript can fall back to the legacy
	// shape for that turn — including keeping its user message, which is
	// only skipped for turns whose transcript actually decoded.
	transcripts := make(map[int][]llm.ChatMessage)
	transcribedTurns := make(map[string]bool)
	needsEvents := false
	for i, e := range entries {
		if e.Role != "assistant" || e.TurnID == "" {
			continue
		}
		if e.Transcript != "" {
			msgs, err := llm.DecodeTranscript(e.Transcript)
			if err == nil && len(msgs) > 0 {
				transcripts[i] = msgs
				transcribedTurns[e.TurnID] = true
				continue
			}
			log.Warn("stored turn transcript unreadable, rebuilding that turn instead", "thread", threadID, "turn", e.TurnID, "err", err)
		}
		needsEvents = true
	}

	var callsByTurn map[string][]replayedCall
	if needsEvents {
		events, err := s.db.ToolEventsForThread(threadID)
		if err != nil {
			return nil, err
		}
		callsByTurn = groupToolEventsByTurn(events)
	}

	history := make([]llm.ChatMessage, 0, len(entries))
	for i, e := range entries {
		if msgs, ok := transcripts[i]; ok {
			history = append(history, msgs...)
			continue
		}
		if e.Role == "user" && transcribedTurns[e.TurnID] {
			continue // its transcript above already opens with it
		}
		// A turn's user and assistant entries share the same TurnID (see
		// EffectiveHistory) — gating on Role here is what keeps a turn's
		// calls from being replayed twice, once per entry sharing that id.
		if e.Role == "assistant" && e.TurnID != "" {
			for n, c := range callsByTurn[e.TurnID] {
				id := fmt.Sprintf("replay_%d_%d", i, n)
				history = append(history,
					llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
						ID:       id,
						Type:     "function",
						Function: llm.FunctionCall{Name: c.tool, Arguments: c.args},
					}}},
					llm.ChatMessage{Role: "tool", ToolCallID: id, Content: c.result},
				)
			}
		}
		history = append(history, llm.ChatMessage{Role: e.Role, Content: e.Content})
	}
	return history, nil
}

// loadAnswerHistory is the thread as plain question/answer pairs, with no
// tool rounds: for side calls that only need the gist of the
// conversation (regenerateTitle, compactThread) and run under their own
// system prompt, so they never shared the main turn's cached prefix
// anyway. Also keeps them to plain user/assistant messages, which every
// provider accepts without a tools list.
func (s *Server) loadAnswerHistory(threadID string) ([]llm.ChatMessage, error) {
	entries, err := s.historyEntries(threadID, 0)
	if err != nil {
		return nil, err
	}
	history := make([]llm.ChatMessage, len(entries))
	for i, e := range entries {
		history[i] = llm.ChatMessage{Role: e.Role, Content: e.Content}
	}
	return history, nil
}

// groupToolEventsByTurn pairs each "tool call started" event with the
// first later unmatched "tool call finished" event for the same tool and
// call_id, per turn, in call order. A call with no recorded result (the
// process died mid-call, or the result row failed to write) is dropped —
// replaying a tool_calls message with no following result is exactly the
// malformed shape providers reject.
func groupToolEventsByTurn(events []store.Event) map[string][]replayedCall {
	type pending struct {
		tool, callID, args string
		result             *string
	}
	perTurn := make(map[string][]*pending)
	var order []string
	for _, ev := range events {
		tool := strings.TrimPrefix(ev.Source, "tool.")
		var data struct {
			Args   map[string]any `json:"args"`
			Result string         `json:"result"`
			CallID string         `json:"call_id"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
			continue
		}
		switch ev.Message {
		case "tool call started":
			args := "{}"
			if len(data.Args) > 0 {
				if b, err := json.Marshal(data.Args); err == nil {
					args = string(b)
				}
			}
			if _, seen := perTurn[ev.TurnID]; !seen {
				order = append(order, ev.TurnID)
			}
			perTurn[ev.TurnID] = append(perTurn[ev.TurnID], &pending{tool: tool, callID: data.CallID, args: args})
		case "tool call finished":
			for _, p := range perTurn[ev.TurnID] {
				if p.result == nil && p.tool == tool && p.callID == data.CallID {
					r := data.Result
					p.result = &r
					break
				}
			}
		}
	}

	out := make(map[string][]replayedCall, len(order))
	for _, turnID := range order {
		for _, p := range perTurn[turnID] {
			if p.result == nil {
				continue
			}
			out[turnID] = append(out[turnID], replayedCall{tool: p.tool, args: p.args, result: *p.result})
		}
	}
	return out
}
