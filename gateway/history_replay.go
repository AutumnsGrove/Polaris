package gateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/store"
)

// fullTurnHistoryContextWindowTokens is the auto-compaction threshold used
// while the full-turn-history setting is on, in place of
// config.ContextWindowTokens (whichever is larger). Replaying every prior
// turn's tool results multiplies how fast a researched thread's context
// grows, so the default 100K threshold would compact almost immediately
// and throw away the very results this mode exists to keep. 200K sits
// safely under every registry model's real window (the smallest, Mercury
// 2.5, is 260K per OpenRouter's /models as of 2026-09) — re-check that if
// a smaller-window model is ever added to models/models.go.
const fullTurnHistoryContextWindowTokens = 200_000

// effectiveContextWindowTokens is the compaction threshold actually in
// force — see fullTurnHistoryContextWindowTokens. Used by handleTurn's
// auto-compaction check and reported to the settings panel as
// context_window_tokens, so the context-usage % shown next to a thread's
// cost stays measured against the threshold that will really trigger.
func effectiveContextWindowTokens(configured int, fullTurnHistory bool) int {
	if fullTurnHistory && configured < fullTurnHistoryContextWindowTokens {
		return fullTurnHistoryContextWindowTokens
	}
	return configured
}

// replayedCall is one tool call rebuilt from the events log, paired with
// the result it returned.
type replayedCall struct {
	tool   string
	args   string // JSON-encoded, as ToolCall.Function.Arguments expects
	result string
}

// loadHistoryWithToolResults is loadHistory plus, for each prior turn,
// the tool calls it made and what they returned — replayed as native
// assistant tool_calls + "tool" result messages ahead of that turn's
// answer, the same shape agent.Run itself builds mid-turn. Without it, a
// follow-up turn sees only prior answers (plus their source list, see
// store.appendCitedSources), and has to re-run searches it already did to
// get back at any detail the answer didn't spell out.
//
// Rebuilt from the durable events log (logTurnEvent's "tool call
// started"/"tool call finished" rows), so it only covers what that log
// kept: each result is already capped at store's maxEventDataBytes, and
// events past their 90-day retention (store.PruneEvents) are simply gone —
// those turns fall back to answer-plus-sources, same as with this setting
// off. Every turn's calls are replayed unconditionally, no per-turn or
// shared-budget filtering — an earlier version tried to cap how much
// replayed text one request could carry, but any filtering rule re-decided
// per request (even one keyed only on a turn's own size, evaluated fresh
// each time) makes an EARLIER turn's reconstructed shape something that
// can still depend on what a LATER request looks like, or on nothing
// changing about the ordering/logic in between. That's the wrong property
// to build on top of: providers that cache on an exact-prefix match
// (DeepSeek included) invalidate the entire cached prefix the moment one
// earlier message differs by so much as a byte, so a history-reconstruction
// step whose output isn't guaranteed identical between two requests that
// share the same earlier turns defeats caching for the whole conversation,
// not just the newest turn. Unconditional replay has no decision to make,
// so there's nothing to make differently — the reconstructed prefix for a
// stable run of earlier turns is byte-identical every time, by
// construction. The real ceiling on how large this can get is
// handleTurn's own auto-compaction check (against
// effectiveContextWindowTokens) — same backstop that already exists for
// organic context growth, not a second bespoke limit layered on top of it.
//
// Tool call IDs are regenerated rather than reused: the logged call_id is
// whatever the provider issued at the time, and sub-agent calls
// (spawn_researchers shares its parent's Emit, so their events land in the
// same turn) can reuse the same ids as the parent's own calls. Every
// replayed call gets its own assistant message immediately followed by its
// own result — never a multi-call batch — so there's no batch boundary to
// reconstruct and the wire protocol's "every tool_calls message is
// followed by all of its results" rule holds by construction.
func (s *Server) loadHistoryWithToolResults(threadID string) ([]llm.ChatMessage, error) {
	entries, err := s.historyEntries(threadID, 0)
	if err != nil {
		return nil, err
	}
	events, err := s.db.ToolEventsForThread(threadID)
	if err != nil {
		return nil, err
	}
	callsByTurn := groupToolEventsByTurn(events)

	history := make([]llm.ChatMessage, 0, len(entries))
	for i, e := range entries {
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
