package eval

import (
	"context"
	"fmt"

	"polaris/jev"
	"polaris/llm"
	"polaris/prompts"
)

// Result is one case's outcome.
type Result struct {
	Case       Case    `json:"case"`
	Pass       bool    `json:"pass"`
	Reason     string  `json:"reason,omitempty"`
	RawOutput  string  `json:"raw_output,omitempty"`
	CostUSD    float64 `json:"cost_usd"`
	JevChoice  string  `json:"jev_choice,omitempty"`
	JevConf    float64 `json:"jev_confidence,omitempty"`
	ErrMessage string  `json:"error,omitempty"`
}

// citationSupportCriteria mirrors gateway/verification.go's verifySource
// exactly — the same four options a real citation-support Jev call uses,
// so a case here is testing the identical judgment Jev makes live, not a
// simplified stand-in for it.
var citationSupportCriteria = map[string]string{
	"supported":           "The source confirms this claim",
	"partially_supported": "The source partially confirms this",
	"contradicted":        "The source contradicts this claim",
	"not_addressed":       "The source does not address this at all",
}

// RunCase runs one case through its Kind's pipeline. llmClient is used for
// KindTitle/KindSuggestions (nil is fine if the case set has none of
// those); jevClient is used for KindCitationSupport (nil is fine if the
// case set has none of those either) — a case whose kind needs a client
// that's nil comes back as an error result, not a panic, so a partial
// case set (e.g. running only --category format) never needs both wired.
func RunCase(ctx context.Context, c Case, llmClient llm.ChatClient, jevClient *jev.Client) Result {
	switch c.Kind {
	case KindTitle:
		return runTitle(ctx, c, llmClient)
	case KindTitleWeaver:
		return runTitleWeaver(ctx, c, llmClient, jevClient)
	case KindSuggestions:
		return runSuggestions(ctx, c, llmClient)
	case KindCitationSupport:
		return runCitationSupport(ctx, c, jevClient)
	default:
		return Result{Case: c, ErrMessage: fmt.Sprintf("unknown case kind %q", c.Kind)}
	}
}

func runTitle(ctx context.Context, c Case, client llm.ChatClient) Result {
	if client == nil {
		return Result{Case: c, ErrMessage: "case needs an LLM client but none was configured"}
	}
	// Exactly gateway/turn.go's generateTitle prompt shape — see its own
	// doc comment on titleSystem/weaverThread; eval cases never exercise
	// the Weaver variant, only the default TitleSystem.
	messages := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Turn.TitleSystem},
		{Role: "user", Content: c.UserMessage},
	}
	resp, err := client.ChatCompletionStreaming(ctx, messages, func(string) {}, nil)
	if err != nil {
		return Result{Case: c, ErrMessage: err.Error()}
	}
	ok, reason := CheckTitleFormat(resp.Content)
	return Result{Case: c, Pass: ok, Reason: reason, RawOutput: resp.Content, CostUSD: resp.CostUSD}
}

// titleWeaverActionCriteria backs runTitleWeaver's semantic half — a
// title generated with weaver_title_system needs Choice, not just code,
// since "does this read as an instruction to Weaver, not a trivia topic"
// is exactly the judgment issue #94 found production's default
// title_system getting wrong (see gateway/turn.go's own doc comment on
// weaverThread): "Framework 13 and ThinkPad Comparison" is a perfectly
// well-formed title by CheckTitleFormat's rules, but wrong content for a
// Weaver session.
var titleWeaverActionCriteria = map[string]string{
	"action": "The title names an action or instruction the person gave Weaver to carry out (e.g. merging, cleaning up, reviewing, looking into something)",
	"topic":  "The title just names a subject or comparison, as if summarizing a trivia question about that topic rather than an instruction to Weaver",
}

func runTitleWeaver(ctx context.Context, c Case, llmClient llm.ChatClient, jevClient *jev.Client) Result {
	if llmClient == nil {
		return Result{Case: c, ErrMessage: "case needs an LLM client but none was configured"}
	}
	messages := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Turn.WeaverTitleSystem},
		{Role: "user", Content: c.UserMessage},
	}
	resp, err := llmClient.ChatCompletionStreaming(ctx, messages, func(string) {}, nil)
	if err != nil {
		return Result{Case: c, ErrMessage: err.Error()}
	}
	result := Result{Case: c, RawOutput: resp.Content, CostUSD: resp.CostUSD}

	if ok, reason := CheckTitleFormat(resp.Content); !ok {
		result.Reason = reason
		return result
	}
	if jevClient == nil {
		result.ErrMessage = "case needs a Jev client for its semantic check but none was configured"
		return result
	}

	jevResp, err := jevClient.AskChoice(ctx, resp.Content, map[string]jev.ChoiceQuestion{
		"framing": {
			Instructions: fmt.Sprintf("The person said to Weaver: %q. Read the title above — does it name an action/instruction, or just a topic?", c.UserMessage),
			Criteria:     titleWeaverActionCriteria,
		},
	})
	if err != nil {
		result.ErrMessage = err.Error()
		return result
	}
	result.CostUSD += jevResp.Usage.CostUSD
	answer, ok := jevResp.Answers["framing"]
	if !ok {
		result.ErrMessage = "jev response missing the \"framing\" answer"
		return result
	}
	result.JevChoice, result.JevConf = answer.Choice, answer.Confidence
	result.Pass = answer.Choice == "action"
	if !result.Pass {
		result.Reason = fmt.Sprintf("title %q reads as a %s, not an action (confidence %.2f)", resp.Content, answer.Choice, answer.Confidence)
	}
	return result
}

func runSuggestions(ctx context.Context, c Case, client llm.ChatClient) Result {
	if client == nil {
		return Result{Case: c, ErrMessage: "case needs an LLM client but none was configured"}
	}
	// Exactly gateway/turn.go's generateSuggestions prompt shape — system,
	// the real Q&A exchange, then the task instruction last (see its own
	// doc comment on why the task can't just live in the system prompt).
	p := prompts.Get()
	messages := []llm.ChatMessage{
		{Role: "system", Content: p.Turn.SuggestionsSystem},
		{Role: "user", Content: c.UserMessage},
		{Role: "assistant", Content: c.Answer},
		{Role: "user", Content: p.Turn.SuggestionsTask},
	}
	resp, err := client.ChatCompletionStreaming(ctx, messages, func(string) {}, nil)
	if err != nil {
		return Result{Case: c, ErrMessage: err.Error()}
	}
	ok, reason := CheckSuggestionsFormat(resp.Content)
	return Result{Case: c, Pass: ok, Reason: reason, RawOutput: resp.Content, CostUSD: resp.CostUSD}
}

func runCitationSupport(ctx context.Context, c Case, client *jev.Client) Result {
	if client == nil {
		return Result{Case: c, ErrMessage: "case needs a Jev client but none was configured"}
	}
	questions := map[string]jev.ChoiceQuestion{
		"claim": {
			Instructions: fmt.Sprintf("Does the source support the claim: %s", c.ClaimText),
			Criteria:     citationSupportCriteria,
		},
	}
	resp, err := client.AskChoice(ctx, c.SourceText, questions)
	if err != nil {
		return Result{Case: c, ErrMessage: err.Error()}
	}
	answer, ok := resp.Answers["claim"]
	if !ok {
		return Result{Case: c, ErrMessage: "jev response missing the \"claim\" answer", CostUSD: resp.Usage.CostUSD}
	}

	pass := answer.Choice == c.WantChoice
	reason := ""
	if !pass {
		reason = fmt.Sprintf("jev said %q (confidence %.2f), want %q", answer.Choice, answer.Confidence, c.WantChoice)
	} else if c.MinConfidence > 0 && answer.Confidence < c.MinConfidence {
		pass = false
		reason = fmt.Sprintf("jev said %q but confidence %.2f is below the %.2f floor", answer.Choice, answer.Confidence, c.MinConfidence)
	}
	return Result{
		Case: c, Pass: pass, Reason: reason,
		CostUSD: resp.Usage.CostUSD, JevChoice: answer.Choice, JevConf: answer.Confidence,
	}
}
