// alert_me lets a Pulsar routine's pulse flag itself as genuinely important
// — the "big news here" signal. Only offered on a scheduled pulse of a
// user-defined routine (see catalog.go's "pulsar_alert" Requires case),
// never in a normal chat, a Pulsar Daily block, or any other turn. Unlike
// finalize_daily_items/finalize_wizard_prompt it does NOT end the turn: the
// pulse still writes its normal report, and this just marks it.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

// maxPulseAlertReasonLen caps the stored reason so a rambling model can't
// turn the one-line subtitle in /pulsar into a paragraph. Measured in runes,
// not bytes, so truncating never splits a multi-byte character.
const maxPulseAlertReasonLen = 200

var alertMeDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "alert_me",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/alert_me.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "One short sentence saying what the big news is, e.g. \"Fed cut rates 50bp, unexpected.\" Shown next to this pulse in the user's list.",
				},
			},
			"required": []string{"reason"},
		},
	},
}

func init() { Register("alert_me", handleAlertMe) }

func handleAlertMe(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "alert_me", nil, "error: "+err.Error(), callID)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		return emitToolError(ctx, "alert_me", map[string]interface{}{"reason": args.Reason},
			"error: a short reason is required", callID)
	}
	if r := []rune(reason); len(r) > maxPulseAlertReasonLen {
		reason = strings.TrimSpace(string(r[:maxPulseAlertReasonLen-1])) + "…"
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "alert_me", "args": map[string]interface{}{"reason": reason}, "call_id": callID,
	})

	result := "Pulse flagged as important. Now write your report as usual."
	if ctx.PulseAlert == nil {
		// Unreachable via Defs() (offered() requires PulseAlert), but a
		// hand-built Context in a test or a stale tool call shouldn't panic.
		result = "error: alerts are not available on this turn"
	} else if err := ctx.PulseAlert(reason); err != nil {
		result = "error: " + err.Error()
	}
	ctx.Emit("tool_result", map[string]interface{}{"tool": "alert_me", "result": result, "call_id": callID})
	return result
}
