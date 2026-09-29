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
				"multi_select": map[string]interface{}{
					"type": "boolean",
					"description": "Optional: set true to let the user pick more than one of `options` in a " +
						"single reply instead of the default single choice — e.g. when asking which of several " +
						"topics to cover. Only meaningful when `options` is set; ignored otherwise.",
				},
				"wants_location": map[string]interface{}{
					"type": "boolean",
					"description": "Set true only when the question is specifically asking where the user " +
						"is or wants something near — shows a \"share my location\" action alongside the text " +
						"input. Leave false for every other kind of question.",
				},
				"wants_web_search": map[string]interface{}{
					"type": "boolean",
					"description": "Set true only when chat mode is active (see the system prompt) and " +
						"you're specifically asking the user whether to turn research tools back on because " +
						"the question genuinely needs current information you don't have — shows an " +
						"\"enable web search\" action alongside the text input. Leave false for every other " +
						"kind of question.",
				},
				"plan": map[string]interface{}{
					"type": "object",
					"description": "Optional, Deep Research only: when this question is confirming a " +
						"spawn_researchers plan, a structured form of the plan you're already describing in " +
						"your own message — lets the UI render it as a real plan instead of a wall of text. " +
						"Omit entirely for every other kind of question.",
					"properties": map[string]interface{}{
						"sub_agent_objectives": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "One entry per sub-agent you're proposing to spawn — must match what you'll actually pass to spawn_researchers if confirmed.",
						},
						"estimated_search_calls": map[string]interface{}{
							"type":        "integer",
							"description": "Optional rough estimate of this plan's total search calls.",
						},
					},
				},
			},
			"required": []string{"question"},
		},
	},
}

func init() { Register("ask_user_question", handleAskUserQuestion) }

func handleAskUserQuestion(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Question       string   `json:"question"`
		Options        []string `json:"options"`
		MultiSelect    bool     `json:"multi_select"`
		WantsLocation  bool     `json:"wants_location"`
		WantsWebSearch bool     `json:"wants_web_search"`
		Plan           *struct {
			SubAgentObjectives   []string `json:"sub_agent_objectives"`
			EstimatedSearchCalls int      `json:"estimated_search_calls"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "ask_user_question", nil, "error: "+err.Error(), callID)
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return emitToolError(ctx, "ask_user_question", map[string]interface{}{"question": args.Question},
			"error: question is required", callID)
	}
	if len(args.Options) > maxAskUserQuestionOptions {
		args.Options = args.Options[:maxAskUserQuestionOptions]
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "ask_user_question",
		"args": map[string]interface{}{
			"question": args.Question, "options": args.Options, "multi_select": args.MultiSelect,
			"wants_location": args.WantsLocation, "wants_web_search": args.WantsWebSearch,
		},
		"call_id": callID,
	})

	var plan *ResearchPlan
	if args.Plan != nil && len(args.Plan.SubAgentObjectives) > 0 {
		plan = &ResearchPlan{
			SubAgentObjectives:   args.Plan.SubAgentObjectives,
			EstimatedSearchCalls: args.Plan.EstimatedSearchCalls,
		}
	}
	ctx.SetPendingQuestion(&PendingQuestion{
		Question: args.Question, Options: args.Options, MultiSelect: args.MultiSelect,
		WantsLocation: args.WantsLocation, WantsWebSearch: args.WantsWebSearch, Plan: plan,
	})

	// Never seen by the model again — the turn ends right after this
	// dispatch (see agent.Run's PendingQuestion check), and the next
	// turn's history is rebuilt purely from persisted user/assistant
	// messages, not from this call's in-memory tool-result scaffolding.
	result := "(turn paused — waiting for the user's reply to this question)"
	ctx.Emit("tool_result", map[string]interface{}{"tool": "ask_user_question", "result": result, "call_id": callID})
	return result
}

// PendingQuestion is a clarifying question the model asked instead of
// answering — see ask_user_question.go. Persisted as part of the
// assistant message that asked it (store.Message.PendingQuestion) so it
// survives reloads and restarts: answering it is just sending the next
// ordinary chat message in the thread, not a live round trip, so there's
// nothing else to keep alive in memory.
type PendingQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	// MultiSelect, when true, lets the user pick more than one of Options
	// in a single reply (the frontend joins the picks into one
	// comma-separated answer) instead of the default single tap-to-answer
	// choice. Only meaningful alongside Options — see ask_user_question.go's
	// multi_select parameter.
	MultiSelect   bool `json:"multi_select,omitempty"`
	WantsLocation bool `json:"wants_location,omitempty"`
	// WantsWebSearch mirrors WantsLocation's shape for a different missing
	// capability: set when the model wants to ask whether to turn research
	// back on for chat mode (NoResearch above) — shows an "enable web
	// search" action alongside the text input, same as WantsLocation's
	// "share my location". See ask_user_question.go.
	WantsWebSearch bool `json:"wants_web_search,omitempty"`

	// Plan, when set, is a Tier 2 Deep Research plan-confirmation question
	// (docs/plans/deep-research-two-tier.md's "Confirm" step) — the
	// orchestrator's proposed spawn_researchers fan-out, attached purely
	// so the frontend can render a richer plan card instead of parsing it
	// back out of Question's prose. The plan's content is also written
	// into Question itself, so a client that doesn't render Plan
	// specially still shows the full plan as normal text — this is an
	// enhancement, not the source of truth.
	Plan *ResearchPlan `json:"plan,omitempty"`
}

// ResearchPlan is PendingQuestion's structured Deep Research plan — see
// its doc comment above.
type ResearchPlan struct {
	// SubAgentObjectives is one entry per sub-agent the orchestrator is
	// proposing to spawn, matching what it intends to pass to
	// spawn_researchers if confirmed.
	SubAgentObjectives []string `json:"sub_agent_objectives"`
	// EstimatedSearchCalls is an optional rough total-call estimate for
	// the whole plan — 0 means the orchestrator didn't provide one, not a
	// claim of "zero calls needed".
	EstimatedSearchCalls int `json:"estimated_search_calls,omitempty"`
}

// SetPendingQuestion records the turn-ending question, if none has been
// recorded yet this turn. First-write-wins rather than overwriting or
// erroring on a second call — dispatchToolCallsConcurrently could in
// principle run two ask_user_question calls from the same batch in
// parallel (the model was told not to, but nothing enforces that), and
// silently keeping whichever one landed first is a safer failure mode
// than a data race or a nondeterministic "last one wins".
func (c *Context) SetPendingQuestion(q *PendingQuestion) {
	c.pendingQuestionMu.Lock()
	defer c.pendingQuestionMu.Unlock()
	if c.PendingQuestion == nil {
		c.PendingQuestion = q
	}
}
