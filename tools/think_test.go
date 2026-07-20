package tools

import "testing"

func TestHandleThink(t *testing.T) {
	var emitted []struct {
		eventType string
		payload   map[string]interface{}
	}
	ctx := &Context{
		Emit: func(eventType string, payload map[string]interface{}) {
			emitted = append(emitted, struct {
				eventType string
				payload   map[string]interface{}
			}{eventType, payload})
		},
	}

	result := handleThink(`{"thought":"I should search for this"}`, ctx)
	if result != "noted" {
		t.Errorf("result = %q, want %q", result, "noted")
	}
	if len(emitted) != 1 || emitted[0].eventType != "thinking" {
		t.Fatalf("emitted = %+v, want one \"thinking\" event", emitted)
	}
	if emitted[0].payload["content"] != "I should search for this" {
		t.Errorf("payload content = %v, want the thought text", emitted[0].payload["content"])
	}
}

func TestHandleThink_InvalidJSON(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	result := handleThink(`not json`, ctx)
	if result == "noted" {
		t.Error("expected an error result for invalid JSON, got \"noted\"")
	}
}
