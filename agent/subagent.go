package agent

import (
	"context"
	"fmt"

	"polaris/llm"
	"polaris/prompts"
	"polaris/tools"
)

// RunSubAgent runs one Tier 2 Deep Research sub-agent to completion — a
// normal agent.Run tool-use loop against a Context scoped down to
// SubAgentRole (web_search/web_read/think/reference_lookup only — see
// tools/catalog.go's subAgentToolNames), then parses the final
// answer into a SubAgentReport (see ParseSubAgentReport). baseCtx
// supplies every shared session-scoped dependency (SearXNG/Brave/
// Parallel/Tavily, ResearchBudget, SearchDedup, ...); RunSubAgent builds
// its own *tools.Context from it via newSubAgentContext rather than
// reusing baseCtx directly, since every sub-agent in a fan-out wave runs
// concurrently against the same baseCtx otherwise (see that function's
// doc comment for why a plain `*baseCtx` copy isn't safe here).
//
// task's type (tools.SubAgentTask, not a local one) is defined in
// package tools rather than here so tools.Context's SpawnResearchers
// closure field (the spawn_researchers tool's bridge into this
// function's caller) can reference it too — package tools can't import
// agent, since agent already imports tools.
func RunSubAgent(reqCtx context.Context, baseCtx *tools.Context, llmClient llm.ChatClient, task tools.SubAgentTask) (tools.SubAgentReport, error) {
	subCtx := newSubAgentContext(baseCtx, llmClient)
	userMessage := fmt.Sprintf(prompts.Get().Agent.SubAgentTask, task.Objective, task.Guidance)

	result, err := Run(reqCtx, subCtx, nil, userMessage)
	if err != nil {
		return tools.SubAgentReport{}, err
	}
	return tools.ParseSubAgentReport(task.Objective, result.Answer, subCtx.Citations), nil
}

// newSubAgentContext builds a fresh *tools.Context for one sub-agent,
// sharing baseCtx's session-scoped dependencies (research clients,
// ResearchBudget, SearchDedup, DisabledTools) while giving it its own
// zero-value accumulators (Citations, Cards, PendingQuestion) and
// mutexes. Deliberately never `*baseCtx` (a whole-struct dereference
// copy) — tools.Context embeds unexported sync.Mutex fields, and copying
// those is unsafe once any sub-agent has actually used them concurrently
// (go vet's copylocks check would catch a plain copy; hand-copying the
// fields we actually want avoids the mutexes entirely). A field added to
// tools.Context that a sub-agent should also see needs to be added here
// by hand — nothing enforces that automatically, same as this codebase's
// other "keep in sync by hand" mirrors (e.g. FocusMode's Go/TS pair).
func newSubAgentContext(baseCtx *tools.Context, llmClient llm.ChatClient) *tools.Context {
	return &tools.Context{
		Ctx:                    baseCtx.Ctx,
		SearXNG:                baseCtx.SearXNG,
		Foursquare:             baseCtx.Foursquare,
		Tavily:                 baseCtx.Tavily,
		Brave:                  baseCtx.Brave,
		Parallel:               baseCtx.Parallel,
		LLM:                    llmClient,
		Embed:                  baseCtx.Embed,
		BraveUsageThisMonth:    baseCtx.BraveUsageThisMonth,
		IncrementBraveUsage:    baseCtx.IncrementBraveUsage,
		ParallelUsageThisMonth: baseCtx.ParallelUsageThisMonth,
		IncrementParallelUsage: baseCtx.IncrementParallelUsage,
		PinnedProvider:         baseCtx.PinnedProvider,
		Emit:                   baseCtx.Emit,
		MaxTurns:               baseCtx.MaxTurns,
		DeepResearch:           true,
		DisabledTools:          baseCtx.DisabledTools,
		SubAgentRole:           "researcher",
		ResearchBudget:         baseCtx.ResearchBudget,
		SearchDedup:            baseCtx.SearchDedup,
	}
}
