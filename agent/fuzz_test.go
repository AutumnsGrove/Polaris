package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// Pseudo tool calls are parsed out of free-form model text when a model emits
// them in content instead of the structured tool_calls field.
func FuzzParsePseudoToolCalls(f *testing.F) {
	f.Add(`<tool_call><function=web_search><parameter=query>latest golang release</parameter><parameter=max_results>3</parameter></function></tool_call>`)
	f.Add(`<｜｜DSML｜｜invoke name="think"><｜｜DSML｜｜parameter name="thought">step one</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke>`)
	f.Add(`<tool_call><function=   ><parameter=query>x</parameter></function></tool_call>`)
	f.Add(`<tool_call><function=think><parameter=thought>hi`)
	f.Fuzz(func(t *testing.T, content string) {
		for _, c := range parsePseudoToolCalls(content) {
			if strings.TrimSpace(c.name) == "" {
				t.Errorf("call with empty name from %q", content)
			}
			if !json.Valid([]byte(c.argsJSON)) {
				t.Errorf("call %q has invalid args JSON %q", c.name, c.argsJSON)
			}
		}
	})
}

func FuzzExtractSearchQuery(f *testing.F) {
	f.Add(`{"query":"golang"}`)
	f.Add(`not json`)
	f.Add(`{"query":5}`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, args string) {
		_ = extractSearchQuery(args)
	})
}
