package agent

import (
	"encoding/json"
	"testing"
)

func TestParsePseudoToolCalls_MiMoFormat(t *testing.T) {
	content := `<tool_call><function=web_search><parameter=query>latest golang release</parameter><parameter=max_results>3</parameter></function></tool_call>`

	calls := parsePseudoToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].name != "web_search" {
		t.Errorf("name = %q, want web_search", calls[0].name)
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(calls[0].argsJSON), &args); err != nil {
		t.Fatalf("argsJSON not valid JSON: %v", err)
	}
	if args["query"] != "latest golang release" {
		t.Errorf("query = %v, want %q", args["query"], "latest golang release")
	}
	// max_results must come through as a JSON number, not a numeric
	// string — web_search's handler expects an integer for this field.
	if n, ok := args["max_results"].(float64); !ok || n != 3 {
		t.Errorf("max_results = %v (%T), want numeric 3", args["max_results"], args["max_results"])
	}
}

func TestParsePseudoToolCalls_DSMLFormat_MultipleInvokes(t *testing.T) {
	content := `<｜｜DSML｜｜invoke name="think"><｜｜DSML｜｜parameter name="thought">step one</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke>` +
		`<｜｜DSML｜｜invoke name="web_search"><｜｜DSML｜｜parameter name="query">step two</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke>`

	calls := parsePseudoToolCalls(content)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].name != "think" || calls[1].name != "web_search" {
		t.Errorf("names = %q, %q", calls[0].name, calls[1].name)
	}
}

func TestParsePseudoToolCalls_NoMatch(t *testing.T) {
	calls := parsePseudoToolCalls("just a normal answer, no tool call syntax here")
	if calls != nil {
		t.Errorf("got %v, want nil", calls)
	}
}

func TestParsePseudoToolCalls_EmptyFunctionName(t *testing.T) {
	// A malformed/empty function name shouldn't produce a call that
	// Dispatch would then reject as "unknown tool ''".
	content := `<tool_call><function=   ><parameter=query>x</parameter></function></tool_call>`
	if calls := parsePseudoToolCalls(content); calls != nil {
		t.Errorf("got %v, want nil for blank function name", calls)
	}
}

func TestBuildArgsJSON_IntCoercion(t *testing.T) {
	argsJSON, ok := buildArgsJSON([][2]string{{"max_results", "5"}, {"query", "test"}})
	if !ok {
		t.Fatal("buildArgsJSON returned ok=false")
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if _, ok := args["max_results"].(float64); !ok {
		t.Errorf("max_results should decode as a number, got %T", args["max_results"])
	}
	if _, ok := args["query"].(string); !ok {
		t.Errorf("query should decode as a string, got %T", args["query"])
	}
}

func TestStreamSniffer_PassesThroughPlainContent(t *testing.T) {
	var emitted string
	s := &streamSniffer{
		emit:     func(chunk string) { emitted += chunk },
		prefixes: pseudoToolCallPrefixes,
	}
	s.onChunk("Hello, ")
	s.onChunk("world!")
	s.flush()

	if emitted != "Hello, world!" {
		t.Errorf("emitted = %q, want %q", emitted, "Hello, world!")
	}
}

func TestStreamSniffer_SuppressesPseudoToolCallSyntax(t *testing.T) {
	var emitted string
	s := &streamSniffer{
		emit:     func(chunk string) { emitted += chunk },
		prefixes: pseudoToolCallPrefixes,
	}
	// Fed in small chunks, as a real streaming response would arrive.
	full := `<tool_call><function=think><parameter=thought>hi</parameter></function></tool_call>`
	for i := 0; i < len(full); i += 4 {
		end := i + 4
		if end > len(full) {
			end = len(full)
		}
		s.onChunk(full[i:end])
	}
	s.flush()

	if emitted != "" {
		t.Errorf("emitted = %q, want empty — pseudo tool call syntax should never reach the user", emitted)
	}
}

func TestStreamSniffer_ShortResponseBelowPrefixLen(t *testing.T) {
	// A very short plain answer (shorter than the longest prefix) never
	// crosses the buffering threshold in onChunk — flush must still emit
	// it rather than silently dropping it as "not yet resolved".
	var emitted string
	s := &streamSniffer{
		emit:     func(chunk string) { emitted += chunk },
		prefixes: pseudoToolCallPrefixes,
	}
	s.onChunk("ok")
	s.flush()

	if emitted != "ok" {
		t.Errorf("emitted = %q, want %q", emitted, "ok")
	}
}
