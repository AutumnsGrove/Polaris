package agent

import (
	"context"
	"os"
	"sync/atomic"

	"polaris/llm"
	"polaris/tools"
)

// novaLoop is the process-wide switch between the bespoke loop and the Nova
// one (docs/plans/nova-loop.md §4). It is set once at startup from
// config.Config.NovaLoop, never mid-turn: Run reads it exactly once per call.
var novaLoop atomic.Bool

// SetNovaLoop selects the Nova-backed loop for every subsequent Run.
func SetNovaLoop(on bool) { novaLoop.Store(on) }

// novaLoopEnabled is the config flag OR the POLARIS_NOVA_LOOP=1 dev override,
// so a developer can flip a running checkout without editing config.yaml.
func novaLoopEnabled() bool {
	return novaLoop.Load() || os.Getenv("POLARIS_NOVA_LOOP") == "1"
}

// Run executes one turn of the agent loop: given prior conversation history
// plus a new user message, it streams progress (thinking/tool_call/
// tool_result/token events) via ctx.Emit and returns once the model has
// produced its final answer. ctx must have LLM and Emit set;
// SearXNG/Foursquare/DefaultLocation are optional per-tool dependencies.
//
// reqCtx cancels the whole turn (the "stop" button) — a cancellation isn't
// treated as an error, but as a graceful early finish with whatever content
// streamed so far.
//
// Which implementation runs is a runtime switch (SetNovaLoop); both return the
// same *Result, so no caller learns which one ran.
func Run(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
	if novaLoopEnabled() {
		return runWithNova(reqCtx, ctx, history, userMessage)
	}
	return runBespoke(reqCtx, ctx, history, userMessage)
}
