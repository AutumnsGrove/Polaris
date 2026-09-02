package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"polaris/llm"
)

var spawnResearchersDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "spawn_researchers",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/spawn_researchers.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"tasks": map[string]interface{}{
					"type":        "array",
					"description": "One entry per sub-agent to spawn, each investigating a distinct, non-overlapping angle.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"objective": map[string]interface{}{
								"type":        "string",
								"description": "This sub-agent's specific research objective — one focused question or angle, not the whole original question.",
							},
							"guidance": map[string]interface{}{
								"type":        "string",
								"description": "Optional: which sources/angle to focus on, or boundaries to stay within.",
							},
						},
						"required": []string{"objective"},
					},
				},
			},
			"required": []string{"tasks"},
		},
	},
}

func init() { Register("spawn_researchers", handleSpawnResearchers) }

func handleSpawnResearchers(argsJSON string, ctx *Context) string {
	var args struct {
		Tasks []struct {
			Objective string `json:"objective"`
			Guidance  string `json:"guidance"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "spawn_researchers", nil, "error: "+err.Error())
	}
	if len(args.Tasks) == 0 {
		return emitToolError(ctx, "spawn_researchers", nil, "error: at least one task is required")
	}
	if ctx.SpawnResearchers == nil {
		result := "error: spawn_researchers is not available in this context"
		log.Warn("spawn_researchers called with no SpawnResearchers closure configured")
		return emitToolError(ctx, "spawn_researchers", map[string]interface{}{"task_count": len(args.Tasks)}, result)
	}

	tasks := make([]SubAgentTask, len(args.Tasks))
	for i, t := range args.Tasks {
		tasks[i] = SubAgentTask{Objective: t.Objective, Guidance: t.Guidance}
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "spawn_researchers",
		"args": map[string]interface{}{"task_count": len(tasks)},
	})

	reports := ctx.SpawnResearchers(ctx, tasks)

	var sb strings.Builder
	for i, report := range reports {
		for _, cit := range report.Citations {
			ctx.AddCitation(cit)
		}
		fmt.Fprintf(&sb, "## Sub-agent %d: %s\n\n", i+1, report.Objective)
		if len(report.Findings) == 0 {
			sb.WriteString("(no findings)\n\n")
			continue
		}
		for _, f := range report.Findings {
			fmt.Fprintf(&sb, "- %s\n", f.Claim)
			if len(f.Sources) > 0 {
				fmt.Fprintf(&sb, "  Sources: %s\n", strings.Join(f.Sources, ", "))
			}
		}
		sb.WriteString("\n")
	}
	formatted := sb.String()

	log.Info("spawn_researchers", "task_count", len(tasks), "report_count", len(reports))
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "spawn_researchers",
		"result":    formatted,
		"citations": ctx.CitationsSnapshot(),
	})
	return formatted
}
