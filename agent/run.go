package agent

import (
	"context"

	"polaris/llm"
	"polaris/tools"
)

// Run executes one turn of the agent loop on Nova (github.com/AutumnsGrove/nova;
// docs/plans/nova-loop.md): given prior conversation history plus a new user
// message, it streams progress (thinking/tool_call/tool_result/token events)
// via ctx.Emit and returns once the model has produced its final answer. ctx
// must have LLM and Emit set; SearXNG/Foursquare/DefaultLocation are optional
// per-tool dependencies.
//
// reqCtx cancels the whole turn (the "stop" button) — a cancellation isn't
// treated as an error, but as a graceful early finish with whatever content
// streamed so far.
func Run(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
	return runWithNova(reqCtx, ctx, history, userMessage)
}
