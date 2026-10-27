package agent

import (
	"context"
	"os"
	"strings"
	"sync/atomic"

	"polaris/llm"
	"polaris/tools"
)

// novaLoop is the process-wide switch between the bespoke loop and the Nova
// one (docs/plans/nova-loop.md §4). It is set once at startup from
// config.Config.NovaLoopEnabled and on every config reload, never mid-turn:
// Run reads it exactly once per call.
var novaLoop atomic.Bool

// SetNovaLoop selects the Nova-backed loop for every subsequent Run.
func SetNovaLoop(on bool) { novaLoop.Store(on) }

// novaLoopEnabled is the config value, with POLARIS_NOVA_LOOP as a full
// override in both directions: "1"/"true" forces the Nova loop on for a
// checkout that has it off, "0"/"false" forces the bespoke loop and is the
// environment's kill switch (docs/plans/nova-loop.md §6 N10).
func novaLoopEnabled() bool {
	if v, ok := os.LookupEnv("POLARIS_NOVA_LOOP"); ok {
		return v == "1" || strings.EqualFold(v, "true")
	}
	return novaLoop.Load()
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
