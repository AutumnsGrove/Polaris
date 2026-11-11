// record_classification is how an Approach run ends — the forced-tool-call
// output shape finalize_daily_items/finalize_wizard_prompt already use,
// applied to Comet's per-thread classifier (see docs/plans/comet.md's
// "Category breakdown"). Calling it ends the turn immediately. Only ever
// offered inside an Approach run (requires: approach_run in
// tools/descriptions/record_classification.yaml).
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

var recordClassificationDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "record_classification",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/record_classification.yaml — see
		// tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"topic": map[string]interface{}{
					"type": "string",
					"description": "What this conversation was actually about, as one short category " +
						"label — reuse the category vocabulary from the system prompt rather than " +
						"inventing a narrow one-off.",
				},
				"mood": map[string]interface{}{
					"type": "string",
					"description": "The emotional register the person brought to this conversation, as one " +
						"short plain-word label (e.g. \"curious\", \"frustrated\", \"excited\"). Reuse a value " +
						"from the system prompt's starting list unless nothing there fits.",
				},
				"intent": map[string]interface{}{
					"type":        "string",
					"description": "One line: what were they trying to do or find out?",
				},
				"resolved": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"yes", "no", "partial"},
					"description": "Did they get what they came for — yes, no, or partial?",
				},
			},
			"required": []string{"topic", "mood", "intent", "resolved"},
		},
	},
}

func init() { Register("record_classification", handleRecordClassification) }

func handleRecordClassification(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Topic    string `json:"topic"`
		Mood     string `json:"mood"`
		Intent   string `json:"intent"`
		Resolved string `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "record_classification", nil, "error: "+err.Error(), callID)
	}
	args.Topic = strings.TrimSpace(args.Topic)
	args.Mood = strings.TrimSpace(args.Mood)
	args.Intent = strings.TrimSpace(args.Intent)
	args.Resolved = strings.TrimSpace(args.Resolved)

	if args.Topic == "" || args.Mood == "" || args.Intent == "" {
		return emitToolError(ctx, "record_classification", map[string]interface{}{"topic": args.Topic, "mood": args.Mood, "intent": args.Intent},
			"error: topic, mood, and intent are all required and must be non-empty", callID)
	}
	if args.Resolved != "yes" && args.Resolved != "no" && args.Resolved != "partial" {
		return emitToolError(ctx, "record_classification", map[string]interface{}{"resolved": args.Resolved},
			`error: resolved must be one of "yes", "no", "partial"`, callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "record_classification",
		"args":    map[string]interface{}{"topic": args.Topic, "mood": args.Mood, "intent": args.Intent, "resolved": args.Resolved},
		"call_id": callID,
	})

	ctx.SetTrailClassification(&TrailClassification{
		Topic:    args.Topic,
		Mood:     args.Mood,
		Intent:   args.Intent,
		Resolved: args.Resolved,
	})

	result := "(turn paused — the classification has been handed back to the Comet pipeline)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "record_classification", "result": result, "call_id": callID})
	return result
}

// TrailClassification is one thread's finished classification, read off
// agent.Run's Result by gateway/comet_approach.go once the run ends.
type TrailClassification struct {
	Topic    string `json:"topic"`
	Mood     string `json:"mood"`
	Intent   string `json:"intent"`
	Resolved string `json:"resolved"`
}

// SetTrailClassification records the turn-ending classification, if none has
// been recorded yet this run — same first-write-wins reasoning as
// SetDailyItemsFinal.
func (c *Context) SetTrailClassification(t *TrailClassification) {
	c.trailClassificationMu.Lock()
	defer c.trailClassificationMu.Unlock()
	if c.TrailClassification == nil {
		c.TrailClassification = t
	}
}
