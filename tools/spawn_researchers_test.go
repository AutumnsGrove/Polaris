package tools

import (
	"strings"
	"testing"
)

func TestHandleSpawnResearchers_NoClosureConfigured(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	result := handleSpawnResearchers(`{"tasks":[{"objective":"x"}]}`, ctx)
	if !strings.Contains(strings.ToLower(result), "error") {
		t.Errorf("result = %q, want an error when SpawnResearchers is unconfigured", result)
	}
}

func TestHandleSpawnResearchers_InvalidJSON(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	result := handleSpawnResearchers(`not json`, ctx)
	if result == "" {
		t.Error("expected an error result for invalid JSON")
	}
}

func TestHandleSpawnResearchers_EmptyTasksRequiresAtLeastOne(t *testing.T) {
	ctx := &Context{Emit: func(string, map[string]interface{}) {}}
	result := handleSpawnResearchers(`{"tasks":[]}`, ctx)
	if !strings.Contains(strings.ToLower(result), "error") {
		t.Errorf("result = %q, want an error for zero tasks", result)
	}
}

func TestHandleSpawnResearchers_PassesThroughTasksAndFormatsReports(t *testing.T) {
	var gotTasks []SubAgentTask
	ctx := &Context{
		Emit: func(string, map[string]interface{}) {},
		SpawnResearchers: func(ctx *Context, tasks []SubAgentTask) []SubAgentReport {
			gotTasks = tasks
			return []SubAgentReport{
				{
					Objective: tasks[0].Objective,
					Findings: []SubAgentFinding{
						{Claim: "City X grew 12%", Sources: []string{"https://example.com/a"}},
					},
					Citations: []Citation{{Title: "Growth Report", URL: "https://example.com/a"}},
				},
			}
		},
	}

	result := handleSpawnResearchers(`{"tasks":[{"objective":"research city X","guidance":"focus on 2020-2025"}]}`, ctx)

	if len(gotTasks) != 1 || gotTasks[0].Objective != "research city X" || gotTasks[0].Guidance != "focus on 2020-2025" {
		t.Errorf("gotTasks = %+v, want the parsed objective/guidance passed through", gotTasks)
	}
	if !strings.Contains(result, "City X grew 12%") {
		t.Errorf("result = %q, want it to contain the sub-agent's claim", result)
	}
	if !strings.Contains(result, "https://example.com/a") {
		t.Errorf("result = %q, want it to contain the claim's source URL", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].Title != "Growth Report" {
		t.Errorf("Citations = %+v, want the sub-agent's real citation merged in", ctx.Citations)
	}
}

func TestHandleSpawnResearchers_MultipleReportsAllFormattedAndCited(t *testing.T) {
	ctx := &Context{
		Emit: func(string, map[string]interface{}) {},
		SpawnResearchers: func(ctx *Context, tasks []SubAgentTask) []SubAgentReport {
			reports := make([]SubAgentReport, len(tasks))
			for i, task := range tasks {
				reports[i] = SubAgentReport{
					Objective: task.Objective,
					Findings:  []SubAgentFinding{{Claim: "claim for " + task.Objective}},
					Citations: []Citation{{Title: "t", URL: "https://example.com/" + task.Objective}},
				}
			}
			return reports
		},
	}

	result := handleSpawnResearchers(`{"tasks":[{"objective":"alpha"},{"objective":"beta"}]}`, ctx)

	if !strings.Contains(result, "claim for alpha") || !strings.Contains(result, "claim for beta") {
		t.Errorf("result = %q, want both sub-agents' claims present", result)
	}
	if len(ctx.Citations) != 2 {
		t.Errorf("Citations = %+v, want one merged per sub-agent report", ctx.Citations)
	}
}
