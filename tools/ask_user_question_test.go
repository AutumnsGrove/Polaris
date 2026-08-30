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
