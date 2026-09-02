package tools

import "testing"

func TestHandleAskUserQuestion_SetsPendingQuestion(t *testing.T) {
	ctx := newTestContext()
	result := Dispatch("ask_user_question", `{"question":"What's your budget?","options":["Under $50","$50-100"]}`, ctx)

	if result == "" {
		t.Fatal("handler returned an empty result")
	}
	if ctx.PendingQuestion == nil {
		t.Fatal("ctx.PendingQuestion is nil, want it set")
	}
	if ctx.PendingQuestion.Question != "What's your budget?" {
		t.Errorf("Question = %q", ctx.PendingQuestion.Question)
	}
	if len(ctx.PendingQuestion.Options) != 2 {
		t.Errorf("Options = %v, want 2 entries", ctx.PendingQuestion.Options)
	}
	if ctx.PendingQuestion.WantsLocation {
		t.Error("WantsLocation = true, want false (not requested)")
	}
}

func TestHandleAskUserQuestion_RequiresQuestion(t *testing.T) {
	ctx := newTestContext()
	result := Dispatch("ask_user_question", `{"question":"  "}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an error for a blank question", result)
	}
	if ctx.PendingQuestion != nil {
		t.Error("PendingQuestion was set despite the validation failure")
	}
}

func TestHandleAskUserQuestion_CapsOptions(t *testing.T) {
	ctx := newTestContext()
	Dispatch("ask_user_question", `{"question":"Which?","options":["a","b","c","d","e","f","g","h"]}`, ctx)
	if got := len(ctx.PendingQuestion.Options); got != maxAskUserQuestionOptions {
		t.Errorf("Options len = %d, want capped at %d", got, maxAskUserQuestionOptions)
	}
}

func TestHandleAskUserQuestion_WantsLocation(t *testing.T) {
	ctx := newTestContext()
	Dispatch("ask_user_question", `{"question":"Where are you?","wants_location":true}`, ctx)
	if !ctx.PendingQuestion.WantsLocation {
		t.Error("WantsLocation = false, want true")
	}
}

// TestHandleAskUserQuestion_Plan covers Tier 2's plan-confirmation step
// (docs/plans/deep-research-two-tier.md's "Confirm" flow) — an optional
// structured plan attached to a PendingQuestion, purely so the frontend
// can render something better than a wall of text (the plan's content is
// also in the question's own prose, so a client that doesn't render it
// specially still shows the full plan as normal text).
func TestHandleAskUserQuestion_Plan(t *testing.T) {
	ctx := newTestContext()
	Dispatch("ask_user_question", `{"question":"Here's my plan — run it?","options":["Run it","Cancel"],`+
		`"plan":{"sub_agent_objectives":["Research Austin","Research Nashville"],"estimated_search_calls":12}}`, ctx)

	if ctx.PendingQuestion.Plan == nil {
		t.Fatal("PendingQuestion.Plan is nil, want it set")
	}
	want := []string{"Research Austin", "Research Nashville"}
	if len(ctx.PendingQuestion.Plan.SubAgentObjectives) != 2 ||
		ctx.PendingQuestion.Plan.SubAgentObjectives[0] != want[0] ||
		ctx.PendingQuestion.Plan.SubAgentObjectives[1] != want[1] {
		t.Errorf("Plan.SubAgentObjectives = %v, want %v", ctx.PendingQuestion.Plan.SubAgentObjectives, want)
	}
	if ctx.PendingQuestion.Plan.EstimatedSearchCalls != 12 {
		t.Errorf("Plan.EstimatedSearchCalls = %d, want 12", ctx.PendingQuestion.Plan.EstimatedSearchCalls)
	}
}

// TestHandleAskUserQuestion_NoPlanLeavesFieldNil confirms the ordinary
// (non-Deep-Research) case is unaffected — no plan argument at all means
// no Plan on the resulting PendingQuestion, not an empty-but-non-nil one.
func TestHandleAskUserQuestion_NoPlanLeavesFieldNil(t *testing.T) {
	ctx := newTestContext()
	Dispatch("ask_user_question", `{"question":"What's your budget?"}`, ctx)
	if ctx.PendingQuestion.Plan != nil {
		t.Errorf("Plan = %+v, want nil when no plan argument was sent", ctx.PendingQuestion.Plan)
	}
}

// TestHandleAskUserQuestion_EmptyPlanObjectivesLeavesFieldNil covers a
// model sending `"plan":{}` or an empty objectives array — treated the
// same as no plan at all, not a Plan with a zero-length slice.
func TestHandleAskUserQuestion_EmptyPlanObjectivesLeavesFieldNil(t *testing.T) {
	ctx := newTestContext()
	Dispatch("ask_user_question", `{"question":"q","plan":{"sub_agent_objectives":[]}}`, ctx)
	if ctx.PendingQuestion.Plan != nil {
		t.Errorf("Plan = %+v, want nil when sub_agent_objectives is empty", ctx.PendingQuestion.Plan)
	}
}

func TestSetPendingQuestion_FirstWriteWins(t *testing.T) {
	ctx := newTestContext()
	ctx.SetPendingQuestion(&PendingQuestion{Question: "first"})
	ctx.SetPendingQuestion(&PendingQuestion{Question: "second"})
	if ctx.PendingQuestion.Question != "first" {
		t.Errorf("PendingQuestion.Question = %q, want %q (first write should win)", ctx.PendingQuestion.Question, "first")
	}
}

func TestCatalogEntry_Offered_InteractiveChat(t *testing.T) {
	entry := catalogEntry{Name: "ask_user_question", Requires: "interactive_chat"}

	ctx := newTestContext()
	if entry.offered(ctx) {
		t.Error("offered() = true with RequestLocation nil (no live client), want false")
	}

	ctx.RequestLocation = func() (string, bool) { return "", false }
	if !entry.offered(ctx) {
		t.Error("offered() = false with RequestLocation set (live client), want true")
	}
}
