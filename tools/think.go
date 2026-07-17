package tools

import (
	"encoding/json"

	"localassistant/llm"
)

var thinkDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "think",
		Description: "Reason privately about what to do next before acting: plan a search strategy, " +
			"evaluate whether you already have enough information, or decide which tool to call next. " +
			"The user sees this as a visible 'thinking' step, so keep it short and in first person.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"thought": map[string]interface{}{
					"type":        "string",
					"description": "Your reasoning, 1-3 sentences.",
				},
			},
			"required": []string{"thought"},
		},
	},
}

func init() { Register("think", handleThink) }

func handleThink(argsJSON string, ctx *Context) string {
	var args struct {
		Thought string `json:"thought"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "error: " + err.Error()
	}
	ctx.Emit("thinking", map[string]interface{}{"content": args.Thought})
	return "noted"
}
