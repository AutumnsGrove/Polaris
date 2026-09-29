package gateway

import (
	"context"
	"regexp"
	"strings"

	"polaris/config"
	"polaris/llm"
	"polaris/prompts"
)

// suggestionListPrefix strips list-style prefixes ("1. ", "- ", "• ") the
// model sometimes adds despite being told not to — deliberately narrow
// (requires the punctuation/space right after digits) so it never eats a
// genuine leading number in a question, e.g. "2024 election results?".
var suggestionListPrefix = regexp.MustCompile(`^(?:[-*•]\s+|\d+[.)]\s+)`)

// answerEndsInQuestion reports whether an assistant answer's last
// non-blank line ends in a question mark — the model asking its own
// follow-up in plain prose rather than via ask_user_question. Checked
// against handleTurn's suggestions gate, not just the tool-call case:
// a "what to ask next" chip row directly under a message that's itself
// a question reads as more questions piling up, not answer shortcuts.
// Trailing markdown/whitespace (a closing bold marker, a stray newline)
// is trimmed first so it doesn't mask the real last character.
func answerEndsInQuestion(answer string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(answer), "*_ \t\n")
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？")
}

// maxSuggestionLen caps how long a parsed line can be and still count as
// a follow-up question — a real one reads like "Which company has built
// the most transformer models so far?" (well under this), not a paragraph.
// Belt-and-suspenders against ever showing a runaway response as a chip
// again: the tight MaxTokens below should already prevent it, but this
// means a formatting slip can't leak a full answer into the UI either.
const maxSuggestionLen = 140

// generateSuggestions asks for up to 3 short follow-up questions based on
// the exchange that just finished — one extra cheap, non-streamed LLM
// call, same pattern as compactThread below. Only the last exchange is
// given as context (not the full thread history): follow-ups are about
// "where could this conversation go next", not a function of everything
// said earlier.
//
// Deliberately builds its own client from modelCfg rather than reusing the
// thread's tool-capable client — a fully separate call with no tools
// offered and a tight token cap, so it can never wander into producing a
// real answer instead of short questions. Still pins the provider the
// same way the main client does: leaving that off routes to whatever
// OpenRouter picks by default, which can land on a degraded/no-reasoning
// endpoint for the same model slug and come back with near-empty content.
func (s *Server) generateSuggestions(cfg *config.Config, modelCfg config.ModelConfig, userMessage, answer string) ([]string, float64, error) {
	// 500, not a tighter cap — a reasoning model (this app's own default,
	// "deepseek") spends part of any completion budget on hidden
	// reasoning tokens before it ever emits visible content. A cap too
	// close to what a normal short answer needs risks the reasoning
	// alone consuming it, leaving resp.Content empty — silently, with no
	// error to catch. See generateTitle's doc comment for the concrete
	// case this exact failure mode caused.
	sugClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 500).
		// AllowFallbacks(true) — see the main turn client's construction
		// above for why: an escape valve for every pinned provider being
		// down at once, not a relaxation of the normal curated order.
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)}).
		// Explicitly off, not just omitted — see ReasoningParams.Enabled's
		// doc comment. Leaving the reasoning field off entirely still lets
		// a reasoning-native model reason internally by default, spending
		// part of this 500-token budget on invisible thinking before any
		// suggestion text. This is a 3-question list, not a task that
		// benefits from it.
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	// The task instruction lives in the LAST message, as a fresh "user"
	// turn after the exchange — not just in the system prompt above it.
	// Ending the array on the assistant's full answer (as this used to)
	// invites a weaker model to keep going: it reads as "continue this
	// turn", so the model just extends/qualifies the answer instead of
	// switching to the actual task. A real example hit this exactly —
	// asked about the largest dense LLM, answered PaLM, and the single
	// "suggestion" that came back was "The model was never open-sourced,
	// but the architecture and results were published." — a continuation
	// sentence, not a question. Putting the instruction last, with an
	// explicit anti-continuation line and a worked example, reliably
	// signals a context switch instead.
	p := prompts.Get()
	prompt := []llm.ChatMessage{
		{Role: "system", Content: p.Turn.SuggestionsSystem},
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: answer},
		{Role: "user", Content: p.Turn.SuggestionsTask},
	}

	resp, err := sugClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return nil, 0, err
	}

	var suggestions []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = suggestionListPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		line = strings.Trim(line, "\"")
		// Requiring a trailing question mark is a belt-and-suspenders
		// filter against exactly the continuation-sentence failure mode
		// above: even if a model slips past the prompt's instructions, a
		// non-question line gets dropped silently rather than shown as a
		// "suggestion" that's actually just more of the answer. Showing
		// 0 suggestions is far less confusing than showing a wrong one.
		if line == "" || len(line) > maxSuggestionLen {
			continue
		}
		if !strings.HasSuffix(line, "?") && !strings.HasSuffix(line, "？") {
			continue
		}
		suggestions = append(suggestions, line)
		if len(suggestions) == 3 {
			break
		}
	}
	return suggestions, resp.CostUSD, nil
}
