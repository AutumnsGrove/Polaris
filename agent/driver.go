// Package agent implements the tool-use loop: alternate between the
// model calling think/web_search/web_read and the model answering
// directly, until it produces a plain-text final answer (no reply
// signal tool — see tools.Defs for why).
package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"polaris/llm"
	"polaris/tools"
)

// maxTurns bounds runaway tool-use loops (a model stuck re-searching).
// Hitting it forces a wrap-up answer instead of erroring out.
const maxTurns = 6

// promptPath is read fresh on every turn — no recompiling to change how
// Polaris behaves. Matches her-go's convention of hot-reloaded prompt
// files living as plain text in the working directory.
const promptPath = "prompt.md"

// fallbackSystemPrompt is used only if prompt.md is missing, so a fresh
// clone still works before the user copies prompt.md.example into place.
const fallbackSystemPrompt = `You are Polaris, a private, self-hosted research assistant. You have four tools:

- think: reason privately about strategy before acting.
- web_search: search the web via a private SearXNG instance.
- web_read: fetch a URL and extract its content (optionally filtered to just what's needed).
- nearby_search: find real-world places (restaurants, pharmacies, etc.) near a location.

There is no separate "reply" tool. Once you have enough information (or the question needs none),
just answer directly in plain text — that ends the research phase and streams straight to the user.

Be concise. Cite sources inline as [Title](URL) when you used web_search or web_read to support a claim.
Don't call tools for questions you can already answer confidently (general knowledge, math, writing help).`

// voiceModeInstruction is appended when the turn will be read aloud —
// long markdown-formatted answers with citation lists read terribly via
// TTS, so voice mode gets a stronger brevity/plain-text nudge than the
// base prompt asks for.
const voiceModeInstruction = "\n\nVoice mode is active: this answer will be read aloud, not just displayed. " +
	"Keep it brief and conversational (1-3 sentences when possible), and avoid markdown formatting, " +
	"bullet lists, or reciting citations inline — sources will still be shown in the UI regardless."

// loadSystemPrompt reads prompt.md fresh every call — edit the file,
// see the change on your very next message, no rebuild or restart.
func loadSystemPrompt(voiceMode bool) string {
	data, err := os.ReadFile(promptPath)
	prompt := fallbackSystemPrompt
	if err == nil {
		prompt = string(data)
	}
	if voiceMode {
		prompt += voiceModeInstruction
	}
	return prompt
}

// currentContextPreamble grounds the model in real wall-clock time, computed
// fresh on every turn — without this, a model has no way to know "today"
// beyond its training cutoff, and will confidently answer with a stale
// date or search for news anchored to the wrong week. Prepended ahead of
// the rest of the system prompt so it's the first thing the model reads.
func currentContextPreamble() string {
	now := time.Now()
	return fmt.Sprintf(
		"Current date and time: %s (timezone: %s). Treat this as ground truth for anything "+
			"relative — \"today\", \"this week\", \"latest\", \"currently\", how old something is "+
			"— rather than any date you might otherwise assume from training. If it conflicts with "+
			"a date implied by the user or a search result, trust this line.\n\n",
		now.Format("Monday, January 2, 2006, 15:04"), now.Location(),
	)
}

// Result is what one turn produces, once the model settles on a
// plain-text final answer.
type Result struct {
	Answer    string
	Citations []tools.Citation
	CostUSD   float64
	// ContextTokens is the prompt+completion token count of the LAST LLM
	// call this turn made — the best available estimate of how much
	// context this thread now occupies, since it reflects every message,
	// tool result, and reasoning pass accumulated through the whole loop.
	ContextTokens int
}

// Run executes one turn of the agent loop: given prior conversation
// history plus a new user message, it streams progress (thinking/
// tool_call/tool_result/token events) via ctx.Emit and returns once the
// model has produced its final answer. ctx must have LLM and Emit set;
// SearXNG/Foursquare/DefaultLocation are optional per-tool dependencies.
//
// reqCtx cancels the whole turn (the "stop" button) — a cancellation
// isn't treated as an error, since the LLM client already turns it into a
// graceful early finish with whatever content streamed so far.
func Run(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
	client := ctx.LLM
	ctx.Ctx = reqCtx

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.ChatMessage{Role: "system", Content: currentContextPreamble() + loadSystemPrompt(ctx.VoiceMode)})
	messages = append(messages, history...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: userMessage})

	toolDefs := tools.Defs()
	var totalCost float64
	var answer strings.Builder

	for turn := 0; turn < maxTurns; turn++ {
		answer.Reset()
		resp, err := client.ChatCompletionWithTools(reqCtx, messages, toolDefs, func(chunk string) {
			answer.WriteString(chunk)
			ctx.Emit("token", map[string]interface{}{"content": chunk})
		}, func(chunk string) {
			ctx.Emit("reasoning", map[string]interface{}{"content": chunk})
		})
		if err != nil {
			return nil, err
		}
		totalCost += resp.CostUSD

		if len(resp.ToolCalls) == 0 {
			// Plain content = the final answer. It was already streamed
			// token-by-token via the onChunk callback above.
			return &Result{
				Answer:        resp.Content,
				Citations:     ctx.Citations,
				CostUSD:       totalCost,
				ContextTokens: resp.PromptTokens + resp.CompletionTokens,
			}, nil
		}

		call := resp.ToolCalls[0]
		messages = append(messages, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		result := tools.Dispatch(call.Function.Name, call.Function.Arguments, ctx)
		messages = append(messages, llm.ChatMessage{Role: "tool", Content: result, ToolCallID: call.ID})
	}

	// Ran out of turns — force a wrap-up instead of failing outright.
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: "Wrap up now — give your best answer with what you've gathered so far.",
	})
	resp, err := client.ChatCompletionStreaming(reqCtx, messages, func(chunk string) {
		ctx.Emit("token", map[string]interface{}{"content": chunk})
	}, func(chunk string) {
		ctx.Emit("reasoning", map[string]interface{}{"content": chunk})
	})
	if err != nil {
		return nil, err
	}
	totalCost += resp.CostUSD

	return &Result{
		Answer:        resp.Content,
		Citations:     ctx.Citations,
		CostUSD:       totalCost,
		ContextTokens: resp.PromptTokens + resp.CompletionTokens,
	}, nil
}
