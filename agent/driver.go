// Package agent implements the tool-use loop: alternate between the
// model calling think/web_search/web_read and the model answering
// directly, until it produces a plain-text final answer (no reply
// signal tool — see tools.Defs for why).
package agent

import (
	"strings"

	"localassistant/llm"
	"localassistant/search"
	"localassistant/tools"
)

// maxTurns bounds runaway tool-use loops (a model stuck re-searching).
// Hitting it forces a wrap-up answer instead of erroring out.
const maxTurns = 6

const systemPrompt = `You are LocalAssistant, a private, self-hosted research assistant. You have three tools:

- think: reason privately about strategy before acting.
- web_search: search the web via a private SearXNG instance.
- web_read: fetch a URL and extract its content (optionally filtered to just what's needed).

There is no separate "reply" tool. Once you have enough information (or the question needs none),
just answer directly in plain text — that ends the research phase and streams straight to the user.

Be concise. Cite sources inline as [Title](URL) when you used web_search or web_read to support a claim.
Don't call tools for questions you can already answer confidently (general knowledge, math, writing help).`

// Result is what one turn produces, once the model settles on a
// plain-text final answer.
type Result struct {
	Answer    string
	Citations []tools.Citation
	CostUSD   float64
}

// Run executes one turn of the agent loop: given prior conversation
// history plus a new user message, it streams progress (thinking/
// tool_call/tool_result/token events) via emit and returns once the
// model has produced its final answer.
func Run(client *llm.Client, searxng *search.SearXNGClient, history []llm.ChatMessage, userMessage string, emit func(eventType string, payload map[string]interface{})) (*Result, error) {
	ctx := &tools.Context{SearXNG: searxng, LLM: client, Emit: emit}

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.ChatMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, history...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: userMessage})

	toolDefs := tools.Defs()
	var totalCost float64
	var answer strings.Builder

	for turn := 0; turn < maxTurns; turn++ {
		answer.Reset()
		resp, err := client.ChatCompletionWithTools(messages, toolDefs, func(chunk string) {
			answer.WriteString(chunk)
			emit("token", map[string]interface{}{"content": chunk})
		})
		if err != nil {
			return nil, err
		}
		totalCost += resp.CostUSD

		if len(resp.ToolCalls) == 0 {
			// Plain content = the final answer. It was already streamed
			// token-by-token via the onChunk callback above.
			return &Result{Answer: resp.Content, Citations: ctx.Citations, CostUSD: totalCost}, nil
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
	resp, err := client.ChatCompletionStreaming(messages, func(chunk string) {
		emit("token", map[string]interface{}{"content": chunk})
	})
	if err != nil {
		return nil, err
	}
	totalCost += resp.CostUSD

	return &Result{Answer: resp.Content, Citations: ctx.Citations, CostUSD: totalCost}, nil
}
