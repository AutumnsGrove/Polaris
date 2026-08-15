package tools

import (
	"encoding/json"

	"polaris/llm"
)

var thinkDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "think",
		// Description is set in init() from tools/descriptions/think.yaml.
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

func init() {
	Register("think", handleThink)
	thinkDef.Function.Description = catalogDescription("think")
}

func handleThink(argsJSON string, ctx *Context) string {
	var args struct {
		Thought string `json:"thought"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "think", nil, "error: "+err.Error())
	}
	ctx.Emit("thinking", map[string]interface{}{"content": args.Thought})
	return "noted"
}
