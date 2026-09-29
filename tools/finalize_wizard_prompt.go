// finalize_wizard_prompt lets the model end a "help me write this"
// wizard interview once it has enough to draft good text — whatever that
// text is for (see WizardTarget: a Pulsar routine prompt, a Pulsar Daily
// block's instructions, a Field's custom instructions, ...). Only ever
// offered on a Wizard turn (see catalog.go's "wizard" Requires case) —
// never appears in a normal chat or pulse turn. Mirrors
// ask_user_question.go's shape: calling this ends the turn immediately,
// same as that tool.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

// WizardTarget says what a wizard interview is writing. Kind picks the
// per-target prompt fragments (prompts.yaml's wizard.targets, keyed by
// these same strings) and Label is the one piece of per-instance text those
// fragments can interpolate — e.g. the Daily block's title or the Field's
// name. Held for the whole session (gateway/wizard.go's wizardSession),
// since only the start request carries it.
type WizardTarget struct {
	Kind  string
	Label string
}

// Wizard target kinds. The strings are the wire values the frontend sends
// (POST /api/wizard/start's "target"), the keys of prompts.yaml's
// wizard.targets, and the suffix of the aux-cost ledger kind — one
// identifier for all three, so they can't drift apart.
const (
	WizardPulsarRoutine          = "pulsar_routine"
	WizardPulsarDailyBlock       = "pulsar_daily_block"
	WizardPulsarDailyCustomBlock = "pulsar_daily_custom_block"
	WizardFieldInstructions      = "field_instructions"
	WizardGlobalInstructions     = "global_instructions"
)

var finalizeWizardPromptDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "finalize_wizard_prompt",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/finalize_wizard_prompt.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{
					"type": "string",
					"description": "The finished, ready-to-use text this interview was writing — what " +
						"exactly that is depends on what this interview is for (see the system prompt). " +
						"Written the way you'd write/use it right now, not a description of what it will do.",
				},
				"name": map[string]interface{}{
					"type": "string",
					"description": "Optional: a short suggested name — only meaningful when the system " +
						"prompt says this interview is for a routine (e.g. \"Daily tech news\"). Omit if " +
						"nothing natural suggests itself, or if this interview isn't about a routine at all — " +
						"the user's own routine name, if they already typed one, takes priority over this.",
				},
			},
			"required": []string{"prompt"},
		},
	},
}

func init() { Register("finalize_wizard_prompt", handleFinalizeWizardPrompt) }

func handleFinalizeWizardPrompt(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Prompt string `json:"prompt"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "finalize_wizard_prompt", nil, "error: "+err.Error(), callID)
	}
	args.Prompt = strings.TrimSpace(args.Prompt)
	args.Name = strings.TrimSpace(args.Name)
	if args.Prompt == "" {
		return emitToolError(ctx, "finalize_wizard_prompt", map[string]interface{}{"prompt": args.Prompt},
			"error: prompt is required", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "finalize_wizard_prompt",
		"args":    map[string]interface{}{"prompt": args.Prompt, "name": args.Name},
		"call_id": callID,
	})

	ctx.SetWizardFinal(&WizardFinal{Prompt: args.Prompt, Name: args.Name})

	result := "(turn paused — the drafted text has been handed back to the form)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "finalize_wizard_prompt", "result": result, "call_id": callID})
	return result
}

// WizardFinal is the tuned text the model drafted once it decided the
// wizard interview had enough to go on — see
// finalize_wizard_prompt.go. Unlike PendingQuestion this is never
// persisted anywhere: the whole wizard session is ephemeral, held only in
// gateway/wizard.go's in-memory session map.
type WizardFinal struct {
	Prompt string `json:"prompt"`
	// Name is an optional suggested routine name — left blank if the
	// model didn't propose one, in which case the frontend leaves
	// whatever the user already typed (if anything) alone.
	Name string `json:"name,omitempty"`
}

// SetWizardFinal records the turn-ending drafted prompt, if none has been
// recorded yet this turn — same first-write-wins reasoning as
// SetPendingQuestion.
func (c *Context) SetWizardFinal(f *WizardFinal) {
	c.wizardFinalMu.Lock()
	defer c.wizardFinalMu.Unlock()
	if c.WizardFinal == nil {
		c.WizardFinal = f
	}
}
