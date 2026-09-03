// finalize_pulsar_prompt lets the model end the Pulsar prompt wizard
// interview once it has enough to draft a good recurring-routine prompt.
// Only ever offered on a PulsarWizard turn (see catalog.go's
// "pulsar_wizard" Requires case) — never appears in a normal chat or
// pulse turn. Mirrors ask_user_question.go's shape: calling this ends the
// turn immediately, same as that tool.
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
					"description": "The finished, ready-to-schedule prompt for this Pulsar routine — " +
						"written the way you'd write it if you were about to run it yourself right now, " +
						"not a description of what the routine will do.",
				},
				"name": map[string]interface{}{
					"type": "string",
					"description": "Optional: a short suggested name for this routine (e.g. \"Daily tech " +
						"news\"). Omit if nothing natural suggests itself — the user's own routine name, " +
						"if they already typed one, takes priority over this.",
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
