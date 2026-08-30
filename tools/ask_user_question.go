// ask_user_question lets the model ask a single clarifying question
// instead of guessing at a detail it genuinely needs. Unlike every other
// tool here, calling it ends the turn: see PendingQuestion's doc comment
// for why answering is just the user's next ordinary chat message rather
// than a live round trip this handler waits on.
package tools

import (
	"encoding/json"
	"strings"

	"polaris/llm"
)

// maxAskUserQuestionOptions caps how many suggested answers the frontend
// renders as tappable rows — enough room for a real finite set (yes/no/
// maybe, a handful of neighborhoods) without turning into an unreadable
// list a human has to scroll through on a phone.
const maxAskUserQuestionOptions = 6

var askUserQuestionDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "ask_user_question",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/ask_user_question.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"question": map[string]interface{}{
					"type":        "string",
					"description": "The single, focused question to ask — no more than one question per call.",
				},
				"options": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
					"description": "Optional: up to 6 short suggested answers shown as tappable rows. The " +
						"user can always type a different answer instead — options are a convenience, never a " +
						"restriction on what they can say.",
				},
				"wants_location": map[string]interface{}{
					"type": "boolean",
					"description": "Set true only when the question is specifically asking where the user " +
						"is or wants something near — shows a \"share my location\" action alongside the text " +
						"input. Leave false for every other kind of question.",
				},
			},
			"required": []string{"question"},
		},
	},
}

func init() { Register("ask_user_question", handleAskUserQuestion) }

func handleAskUserQuestion(argsJSON string, ctx *Context) string {
	var args struct {
		Question      string   `json:"question"`
		Options       []string `json:"options"`
		WantsLocation bool     `json:"wants_location"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "ask_user_question", nil, "error: "+err.Error())
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return emitToolError(ctx, "ask_user_question", map[string]interface{}{"question": args.Question},
			"error: question is required")
	}
	if len(args.Options) > maxAskUserQuestionOptions {
		args.Options = args.Options[:maxAskUserQuestionOptions]
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "ask_user_question",
		"args": map[string]interface{}{
			"question": args.Question, "options": args.Options, "wants_location": args.WantsLocation,
		},
	})

	ctx.SetPendingQuestion(&PendingQuestion{
		Question: args.Question, Options: args.Options, WantsLocation: args.WantsLocation,
	})

	// Never seen by the model again — the turn ends right after this
	// dispatch (see agent.Run's PendingQuestion check), and the next
	// turn's history is rebuilt purely from persisted user/assistant
	// messages, not from this call's in-memory tool-result scaffolding.
	result := "(turn paused — waiting for the user's reply to this question)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "ask_user_question", "result": result})
	return result
}
