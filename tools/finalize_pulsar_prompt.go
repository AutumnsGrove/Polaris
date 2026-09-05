// finalize_pulsar_prompt lets the model end a "help me write this"
// wizard interview once it has enough to draft good text — either a
// recurring Pulsar routine prompt, or (see tools.Context.PulsarDailyBlockTitle)
// a Pulsar Daily block's short steering instruction. Only ever offered on
// a PulsarWizard turn (see catalog.go's "pulsar_wizard" Requires case) —
// never appears in a normal chat or pulse turn. Mirrors
// ask_user_question.go's shape: calling this ends the turn immediately,
// same as that tool.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

var finalizePulsarPromptDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "finalize_pulsar_prompt",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/finalize_pulsar_prompt.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type": "string",
					"description": "The finished, ready-to-use text this interview was writing — a Pulsar " +
						"routine's recurring prompt, or a Pulsar Daily block's short steering instruction, " +
						"depending on what this interview is for (see the system prompt). Written the way " +
						"you'd write/use it right now, not a description of what it will do.",
				},
				"name": map[string]interface{}{
					"type": "string",
					"description": "Optional: a short suggested name — only meaningful for a Pulsar routine " +
						"(e.g. \"Daily tech news\"), never for a Daily block's steering instruction. Omit if " +
						"nothing natural suggests itself, or if this interview isn't about a routine at all — " +
						"the user's own routine name, if they already typed one, takes priority over this.",
				},
			},
			"required": []string{"prompt"},
		},
	},
}

func init() { Register("finalize_pulsar_prompt", handleFinalizePulsarPrompt) }

func handleFinalizePulsarPrompt(argsJSON string, ctx *Context) string {
	var args struct {
		Prompt string `json:"prompt"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "finalize_pulsar_prompt", nil, "error: "+err.Error())
	}
	args.Prompt = strings.TrimSpace(args.Prompt)
	args.Name = strings.TrimSpace(args.Name)
	if args.Prompt == "" {
		return emitToolError(ctx, "finalize_pulsar_prompt", map[string]interface{}{"prompt": args.Prompt},
			"error: prompt is required")
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "finalize_pulsar_prompt",
		"args": map[string]interface{}{"prompt": args.Prompt, "name": args.Name},
	})

	ctx.SetWizardFinal(&WizardFinal{Prompt: args.Prompt, Name: args.Name})

	result := "(turn paused — the drafted prompt has been handed back to the routine form)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "finalize_pulsar_prompt", "result": result})
	return result
}
