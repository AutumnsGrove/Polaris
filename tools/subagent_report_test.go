package tools

import (
	"reflect"
	"testing"
)

func TestParseSubAgentReport_ValidJSON(t *testing.T) {
	raw := `{"objective":"population trends","findings":[{"claim":"City X grew 12% from 2020-2025","sources":["https://example.com/a"]}]}`
	report := ParseSubAgentReport("fallback objective", raw, nil)

	// fallbackObjective (the orchestrator's own record of what it
	// assigned) always wins over whatever the JSON echoes back — see
	// ParseSubAgentReport's doc comment for why the model's own claimed
	// objective isn't trusted here.
	if report.Objective != "fallback objective" {
		t.Errorf("Objective = %q, want %q (the orchestrator-supplied objective, not the model's echo)", report.Objective, "fallback objective")
	}
	want := []SubAgentFinding{{Claim: "City X grew 12% from 2020-2025", Sources: []string{"https://example.com/a"}}}
	if !reflect.DeepEqual(report.Findings, want) {
		t.Errorf("Findings = %+v, want %+v", report.Findings, want)
	}
}

func TestParseSubAgentReport_FencedJSON(t *testing.T) {
	raw := "Here's what I found:\n\n```json\n" +
		`{"objective":"housing","findings":[{"claim":"Median rent is $2,100","sources":["https://example.com/b"]}]}` +
		"\n```\n\nLet me know if you need more."
	report := ParseSubAgentReport("fallback", raw, nil)

	if len(report.Findings) != 1 || report.Findings[0].Claim != "Median rent is $2,100" {
		t.Errorf("Findings = %+v, want the one fenced finding parsed out", report.Findings)
	}
}

func TestParseSubAgentReport_BareJSONWithSurroundingProse(t *testing.T) {
	raw := `Sure, here's the structured result: {"objective":"weather","findings":[{"claim":"Highs near 75F","sources":["https://example.com/c"]}]} hope that helps!`
	report := ParseSubAgentReport("fallback", raw, nil)

	if len(report.Findings) != 1 || report.Findings[0].Claim != "Highs near 75F" {
		t.Errorf("Findings = %+v, want the one bare-JSON finding parsed out", report.Findings)
	}
}

func TestParseSubAgentReport_MalformedJSONFallsBackToSingleFinding(t *testing.T) {
	raw := "I looked into it but couldn't find a clean structured answer, here's my summary in prose."
	citations := []Citation{{Title: "A", URL: "https://example.com/x"}, {Title: "B", URL: "https://example.com/y"}}

	report := ParseSubAgentReport("my objective", raw, citations)

	if report.Objective != "my objective" {
		t.Errorf("Objective = %q, want the passed-in fallback objective", report.Objective)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly one fallback finding", report.Findings)
	}
	if report.Findings[0].Claim != raw {
		t.Errorf("fallback Claim = %q, want the full raw answer %q", report.Findings[0].Claim, raw)
	}
	want := []string{"https://example.com/x", "https://example.com/y"}
	if !reflect.DeepEqual(report.Findings[0].Sources, want) {
		t.Errorf("fallback Sources = %v, want %v (from citations)", report.Findings[0].Sources, want)
	}
}

func TestParseSubAgentReport_EmptyFindingsArrayFallsBack(t *testing.T) {
	raw := `{"objective":"nothing found","findings":[]}`
	report := ParseSubAgentReport("fallback objective", raw, nil)

	if len(report.Findings) != 1 {
		t.Fatalf("Findings = %+v, want a fallback finding when the parsed array was empty", report.Findings)
	}
	if report.Findings[0].Claim != raw {
		t.Errorf("fallback Claim = %q, want the raw answer treated as one finding", report.Findings[0].Claim)
	}
}

func TestParseSubAgentReport_JSONOmittingObjectiveUsesPassedInFallback(t *testing.T) {
	raw := `{"findings":[{"claim":"x","sources":[]}]}`
	report := ParseSubAgentReport("the real objective", raw, nil)

	if report.Objective != "the real objective" {
		t.Errorf("Objective = %q, want the passed-in fallback since JSON omitted it", report.Objective)
	}
}

// TestParseSubAgentReport_ValidJSONStillCarriesFullCitations covers a gap
// found while wiring the spawn_researchers tool: even when the model's
// JSON parses successfully, the sub-agent's own gathered citations (full
// title+URL, from its actual web_search/web_read/reference_lookup calls)
// must still travel back on the report — not just the bare URL strings
// the model happened to echo in "sources" — so the orchestrator can merge
// real, well-titled citations into the final answer instead of untitled
// URLs. This is what actually backs Polaris's "sourcing is the product"
// goal one level into the multi-agent case.
func TestParseSubAgentReport_ValidJSONStillCarriesFullCitations(t *testing.T) {
	raw := `{"findings":[{"claim":"X is true","sources":["https://example.com/a"]}]}`
	citations := []Citation{{Title: "Real Page Title", URL: "https://example.com/a"}}

	report := ParseSubAgentReport("objective", raw, citations)

	if !reflect.DeepEqual(report.Citations, citations) {
		t.Errorf("Citations = %+v, want %+v (the sub-agent's own gathered citations, not discarded on the successful-parse path)", report.Citations, citations)
	}
}
