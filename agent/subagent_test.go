package agent

import (
	"context"
	"errors"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/tools"
)

// TestNewSubAgentContext_ScopesAndShares covers the two things
// newSubAgentContext must get right: scoping this Context down to a
// sub-agent (SubAgentRole set, DeepResearch on, its own empty Citations)
// while still sharing the session-wide dependencies every sub-agent in a
// fan-out needs to cooperate through (ResearchBudget, SearchDedup —
// same pointers, not copies).
func TestNewSubAgentContext_ScopesAndShares(t *testing.T) {
	budget := tools.NewResearchBudget()
	base := &tools.Context{
		Ctx:            context.Background(),
		ResearchBudget: budget,
		DisabledTools:  map[string]bool{"movies": true},
	}
	base.AddCitation(tools.Citation{Title: "parent", URL: "https://example.com/parent"})

	mock := &llmtest.MockClient{}
	sub := newSubAgentContext(base, mock)

	if sub.SubAgentRole == "" {
		t.Error("SubAgentRole is empty, want it set for a sub-agent context")
	}
	if !sub.DeepResearch {
		t.Error("DeepResearch = false, want true (sub-agents get the same widened leash)")
	}
	if sub.LLM != mock {
		t.Error("LLM was not overridden to the passed-in client")
	}
	if len(sub.Citations) != 0 {
		t.Errorf("Citations = %+v, want empty — must not inherit the parent's accumulated citations", sub.Citations)
	}
	if sub.ResearchBudget != budget {
		t.Error("ResearchBudget is not the same shared pointer as base's")
	}
	if !sub.DisabledTools["movies"] {
		t.Error("DisabledTools was not carried over from base")
	}
}

// TestRunSubAgent_ReturnsParsedReport is the end-to-end path: a
// sub-agent that answers with structured JSON (no tool calls needed for
// this test) should come back as a parsed SubAgentReport, with the
// orchestrator-supplied objective always winning over anything in the
// model's own JSON.
func TestRunSubAgent_ReturnsParsedReport(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					Content: `{"objective":"model's own guess","findings":[{"claim":"X is true","sources":["https://example.com/x"]}]}`,
				},
				Chunks: []string{`{"objective":"model's own guess","findings":[{"claim":"X is true","sources":["https://example.com/x"]}]}`},
			},
		},
	}
	base := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}

	report, err := RunSubAgent(context.Background(), base, mock, SubAgentTask{
		Objective: "find out if X is true",
		Guidance:  "check at least two sources",
	})
	if err != nil {
		t.Fatalf("RunSubAgent returned error: %v", err)
	}
	if report.Objective != "find out if X is true" {
		t.Errorf("Objective = %q, want the orchestrator-supplied task objective, not the model's echo", report.Objective)
	}
	if len(report.Findings) != 1 || report.Findings[0].Claim != "X is true" {
		t.Errorf("Findings = %+v, want the one parsed finding", report.Findings)
	}
}

// TestRunSubAgent_PropagatesRunError confirms a failed underlying Run
// call surfaces as an error rather than a silently empty report.
func TestRunSubAgent_PropagatesRunError(t *testing.T) {
	wantErr := errors.New("llm unavailable")
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Err: wantErr}},
	}
	base := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}

	_, err := RunSubAgent(context.Background(), base, mock, SubAgentTask{Objective: "anything"})
	if err == nil {
		t.Fatal("RunSubAgent returned nil error, want the underlying Run failure propagated")
	}
}

// TestRunSubAgent_OnlyOffersSubAgentTools confirms the tool-scoping
// wiring (tools.Context.SubAgentRole, tools/catalog.go's offered() gate)
// actually reaches the model call — the offered tool list sent to the
// LLM must be restricted to web_search/web_read/think, not the full
// catalog a normal turn would get.
func TestRunSubAgent_OnlyOffersSubAgentTools(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: `{"findings":[]}`}, Chunks: []string{`{"findings":[]}`}},
		},
	}
	base := &tools.Context{
		Ctx:          context.Background(),
		Emit:         func(string, map[string]interface{}) {},
		TMDBAPIKey:   "configured", // would normally offer "movies" — must still be excluded
		LastFMAPIKey: "configured",
	}

	_, err := RunSubAgent(context.Background(), base, mock, SubAgentTask{Objective: "anything"})
	if err != nil {
		t.Fatalf("RunSubAgent returned error: %v", err)
	}
	if len(mock.Calls) == 0 {
		t.Fatal("mock recorded no calls")
	}
	offered := make(map[string]bool)
	for _, def := range mock.Calls[0].Tools {
		offered[def.Function.Name] = true
	}
	for _, want := range []string{"web_search", "web_read", "think", "reference_lookup"} {
		if !offered[want] {
			t.Errorf("offered tools = %v, want %q included", offered, want)
		}
	}
	for _, exclude := range []string{"movies", "music", "memory"} {
		if offered[exclude] {
			t.Errorf("offered tools = %v, want %q excluded for a sub-agent", offered, exclude)
		}
	}
}
