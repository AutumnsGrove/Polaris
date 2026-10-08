package gateway

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"polaris/config"
	"polaris/llm"
	"polaris/prompts"
)

// titleQuotePrefix strips a leading/trailing quote mark the model
// sometimes wraps the title in despite being told not to — trimmed
// separately from strings.Trim below since that would also eat a quote
// that's genuinely part of the title (e.g. a title ending in "quotes").
var titleQuotePrefix = regexp.MustCompile(`^["'“‘]+|["'”’]+$`)

// titleLabelPrefix strips a leading "Title:" / "Thread title:" label. Found
// live 2026-10-08 on claude-haiku-5.5, which answers the title prompt as a
// chat reply — "Thread title: Go Release, Security...\n\nI" — where the
// DeepSeek models the prompt was tuned on return the bare title.
var titleLabelPrefix = regexp.MustCompile(`(?i)^(?:\*\*)?(?:thread\s+)?title(?:\*\*)?\s*:\s*(?:\*\*)?`)

// answerLikeTitle catches a title that's actually an answer to the
// user's question instead of a description of it — a real example:
// asked "Who did Vincent Pastore play in the Sopranos? Was it Paulie?"
// and got back "No, Vincent Pastore played Salvatore..." as the title.
// Unlike the earlier answer-in-context bug (see generateTitle's doc
// comment), this happens with only the question in context: a yes/no or
// "was it X" question is enough to pull a helpful model into answering
// it instead of titling it, no matter how the system prompt words the
// instruction not to. Cheaper to catch the handful of tells this
// produces (a leading "Yes"/"No"/"Unfortunately"/etc.) than to rely on
// prompting alone — matched titles are treated as unusable, same as an
// empty one, and the placeholder stands instead.
var answerLikeTitle = regexp.MustCompile(`(?i)^(yes|no|sure|actually|unfortunately|correct|indeed|according to)\b[\s,.:!—-]`)

// maxTitleLen caps the generated title's length — a good one reads like
// "Capital of France" or "Debugging a Go goroutine leak" (well under
// this), not a restated question. Same belt-and-suspenders reasoning as
// maxSuggestionLen: the tight MaxTokens below should already prevent a
// runaway response, this just means a formatting slip can't leak one
// into the sidebar as a "title" anyway — falls back to the
// truncated-first-message placeholder in that case instead.
const maxTitleLen = 60

// generateTitle asks for a short thread title based on the user's
// question alone — one extra cheap, non-streamed LLM call, same pattern
// as generateSuggestions/compactThread. Only called once, right after a
// brand-new thread's first turn (see handleTurn's isNewThread gate).
//
// Deliberately does NOT include the answer, unlike an earlier version of
// this function — with the answer in context, the message array ends on
// a full assistant turn, and a weaker model reliably reads that as "keep
// going" rather than "switch to a new task", producing a title that's
// actually just a continuation of the answer (a real example: title came
// back as "Unfortunately, it was never open-sourced, so while
// available..." — a sentence fragment from the answer, not a title). A
// search-app question is almost always self-descriptive enough on its
// own ("what is the largest dense llm released?"), so dropping the
// answer sidesteps the whole failure mode instead of working around it.
//
// 300, not a tighter cap — this used to be 60, and a real production
// thread never got a generated title at all: the default model
// ("deepseek") is a reasoning model, and reasoning tokens count against
// the same completion budget as visible content. 60 tokens was
// consumed entirely by hidden reasoning, resp.Content came back empty,
// and — since that's not an error — the code below silently did
// nothing, leaving the truncated-question placeholder forever with no
// trace in the event log explaining why (see handleTurn's title-gating
// block, which now logs this case explicitly too).
//
// Even with the answer dropped from context, a yes/no or "was it X"
// question is enough to pull a helpful model into answering it instead
// of titling it (real examples: "Who did Vincent Pastore play in the
// Sopranos? Was it Paulie?" came back as "No, Vincent Pastore played
// Salvatore..."). The system prompt now says so explicitly with
// matching examples, and answerLikeTitle below catches whatever slips
// through anyway.
func (s *Server) generateTitle(cfg *config.Config, modelCfg config.ModelConfig, userMessage string, weaverThread bool) (string, float64, error) {
	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 300).
		// AllowFallbacks(true) — see the main turn client's construction
		// above for why: an escape valve for every pinned provider being
		// down at once, not a relaxation of the normal curated order.
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)}).
		// Explicitly off, not just omitted — see ReasoningParams.Enabled's
		// doc comment. Raising this call's budget from 60 to 300 tokens
		// (see below) helped but didn't fully fix the silent-empty-title
		// failure this comment used to describe alone: a real thread asking
		// a two-part question ("news in Smyrna GA today? And the weather?")
		// still burned the entire 300-token budget on hidden reasoning with
		// the field left unset, and came back with zero visible content.
		// Explicitly disabling reasoning removes the non-determinism
		// instead of just raising the budget and hoping it's enough.
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	// weaverThread (issue #94) picks WeaverTitleSystem instead — see its
	// own doc comment for the real bug this avoids: TitleSystem's Q&A/
	// trivia-tuned heuristics badly misread a Weaver instruction like
	// "merge the Framework 13 and ThinkPad stars" as a topic to name
	// rather than an action being asked of Weaver.
	titleSystem := prompts.Get().Turn.TitleSystem
	if weaverThread {
		titleSystem = prompts.Get().Turn.WeaverTitleSystem
	}
	prompt := []llm.ChatMessage{
		{Role: "system", Content: titleSystem},
		{Role: "user", Content: userMessage},
	}

	resp, err := titleClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	return sanitizeGeneratedTitle(resp.Content), resp.CostUSD, nil
}

// sanitizeGeneratedTitle cleans up a raw title completion into something
// safe to store — strips a wrapping quote and trailing punctuation, caps
// the length, and rejects anything that reads like an answer rather than
// a title (see answerLikeTitle). Shared by generateTitle and
// regenerateTitle so both apply the exact same rules to whatever the
// model sends back.
func sanitizeGeneratedTitle(raw string) string {
	title := strings.TrimSpace(raw)
	// A title is one line; anything after the first newline is the model
	// continuing to chat (an explanation, a second candidate) — and left in,
	// it would be truncated mid-word into the stored title.
	title, _, _ = strings.Cut(title, "\n")
	title = strings.TrimSpace(titleLabelPrefix.ReplaceAllString(strings.TrimSpace(title), ""))
	title = strings.TrimSpace(titleQuotePrefix.ReplaceAllString(title, ""))
	title = strings.TrimRight(title, ".!。")
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen]
	}
	if answerLikeTitle.MatchString(title) {
		return ""
	}
	return title
}

// regenerateTitle is generateTitle's whole-thread counterpart: instead of
// titling just the opening question, it reads the full conversation
// (history as loadAnswerHistory builds it — a compacted summary
// included, same as a normal turn would see) and titles that as a whole.
// Used by the "Regenerate title" menu action, not by the automatic
// once-per-thread path in handleTurn.
//
// The task instruction goes in a trailing user message after history,
// exactly like generateSuggestions — see that function's doc comment for
// why: ending the prompt array on the thread's last assistant reply
// (which the raw history always does) invites a helpful model to keep
// answering instead of switching to the actual task, so the instruction
// needs to be the very last thing it sees.
func (s *Server) regenerateTitle(cfg *config.Config, modelCfg config.ModelConfig, history []llm.ChatMessage) (string, float64, error) {
	if len(history) == 0 {
		return "", 0, fmt.Errorf("thread has no messages to title")
	}

	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 300).
		// AllowFallbacks(true) — see the main turn client's construction
		// above for why: an escape valve for every pinned provider being
		// down at once, not a relaxation of the normal curated order.
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)}).
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	p := prompts.Get()
	prompt := make([]llm.ChatMessage, 0, len(history)+2)
	prompt = append(prompt, llm.ChatMessage{Role: "system", Content: p.Turn.TitleRegenerateSystem})
	prompt = append(prompt, history...)
	prompt = append(prompt, llm.ChatMessage{Role: "user", Content: p.Turn.TitleRegenerateTask})

	resp, err := titleClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	return sanitizeGeneratedTitle(resp.Content), resp.CostUSD, nil
}
