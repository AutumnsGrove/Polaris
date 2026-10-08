package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"polaris/store"
)

func newEmitterForTest(t *testing.T) (*turnEmitter, *store.Store, *[]ServerEvent) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateThread("t1", "title", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	var sent []ServerEvent
	e := newTurnEmitter(&Server{db: db}, func(evt ServerEvent) { sent = append(sent, evt) }, "t1", "t1", "turn1")
	return e, db, &sent
}

func eventData(t *testing.T, ev store.Event) map[string]interface{} {
	t.Helper()
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatalf("event data %q: %v", ev.Data, err)
	}
	return data
}

// TestEmitter_ReasoningBurstsAreKeptPerAgent guards the Deep Research
// "everything merges into one mess" bug: with a single shared reasoning
// buffer, two researchers thinking at once were spliced into one row, and any
// agent's tool call cut every other agent's thought short.
func TestEmitter_ReasoningBurstsAreKeptPerAgent(t *testing.T) {
	e, db, _ := newEmitterForTest(t)

	e.emit("reasoning", map[string]interface{}{"content": "A1 ", "agent_id": "c.0"})
	e.emit("reasoning", map[string]interface{}{"content": "B1 ", "agent_id": "c.1"})
	e.emit("reasoning", map[string]interface{}{"content": "A2", "agent_id": "c.0"})
	// c.1 calls a tool: that must close c.1's burst only.
	e.emit("tool_call", map[string]interface{}{"tool": "web_search", "call_id": "x", "agent_id": "c.1"})
	e.emit("reasoning", map[string]interface{}{"content": "B2", "agent_id": "c.1"})
	e.emit("reasoning", map[string]interface{}{"content": "main", "agent_id": ""})
	e.flushReasoning()

	events, err := db.ListEvents("t1", 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	got := map[string][]string{} // agent_id -> reasoning rows, in order
	for _, ev := range events {
		if ev.Source == "turn" && ev.Message == "reasoning" {
			d := eventData(t, ev)
			id, _ := d["agent_id"].(string)
			got[id] = append(got[id], d["content"].(string))
		}
	}
	if want := []string{"A1 A2"}; strings.Join(got["c.0"], "|") != strings.Join(want, "|") {
		t.Errorf("c.0 reasoning rows = %q, want %q (c.1's tool call must not split it)", got["c.0"], want)
	}
	if want := "B1 |B2"; strings.Join(got["c.1"], "|") != want {
		t.Errorf("c.1 reasoning rows = %q, want %q (split exactly at its own tool call)", got["c.1"], want)
	}
	if want := "main"; strings.Join(got[""], "|") != want || len(got) != 3 {
		t.Errorf("orchestrator rows = %q (agents seen: %d), want only %q untagged", got[""], len(got), want)
	}
}

func TestEmitter_SubAgentEventsArePersistedWithTheirAgentID(t *testing.T) {
	e, db, sent := newEmitterForTest(t)

	e.emit("subagent_start", map[string]interface{}{"agent_id": "c.0", "call_id": "c", "objective": "look into X"})
	e.emit("tool_call", map[string]interface{}{"tool": "web_read", "call_id": "r1", "agent_id": "c.0", "args": map[string]interface{}{"url": "u"}})
	e.emit("tool_result", map[string]interface{}{"tool": "web_read", "call_id": "r1", "agent_id": "c.0", "result": "page"})
	e.emit("subagent_end", map[string]interface{}{"agent_id": "c.0", "call_id": "c", "objective": "look into X", "status": "done", "result": "- finding"})

	if len(*sent) != 4 || (*sent)[0].AgentID != "c.0" || (*sent)[0].Objective != "look into X" || (*sent)[3].AgentStatus != "done" || (*sent)[3].Result != "- finding" {
		t.Errorf("streamed events lost their sub-agent fields: %+v", *sent)
	}

	events, _ := db.ListEvents("t1", 100)
	seen := map[string]bool{}
	for _, ev := range events {
		d := eventData(t, ev)
		switch {
		case ev.Source == "subagent" && ev.Message == "subagent started":
			seen["start"] = d["agent_id"] == "c.0" && d["call_id"] == "c" && d["objective"] == "look into X"
		case ev.Source == "subagent" && ev.Message == "subagent finished":
			seen["end"] = d["agent_id"] == "c.0" && d["status"] == "done" && d["result"] == "- finding"
		case ev.Source == "tool.web_read":
			seen[ev.Message] = d["agent_id"] == "c.0"
		}
	}
	for _, k := range []string{"start", "end", "tool call started", "tool call finished"} {
		if !seen[k] {
			t.Errorf("persisted %q row missing or lacking agent_id/fields (seen=%v)", k, seen)
		}
	}
}
