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
	subCtx := newSubAgentContext(baseCtx, llmClient, task)
	userMessage := fmt.Sprintf(prompts.Get().Agent.SubAgentTask, task.Objective, task.Guidance)

	result, err := Run(reqCtx, subCtx, nil, userMessage)
	if err != nil {
		return tools.SubAgentReport{}, err
	}
	// Fold this sub-agent's own web_read/reference_lookup/
	// youtube_transcript evidence back into baseCtx before it's
	// discarded — subCtx.Citations below is the only other thing that
	// survives past this function returning, and citation-only was
	// exactly the gap that left every Deep Research citation unverifiable
	// (see tools.Context.EvidenceSnapshot's doc comment). AddEvidence is
	// safe to call concurrently, so this is fine even with several
	// sub-agents in the same fan-out wave finishing around the same time.
	for url, text := range subCtx.EvidenceSnapshot() {
		baseCtx.AddEvidence(url, text)
	}
	report := tools.ParseSubAgentReport(task.Objective, result.Answer, subCtx.Citations)
	// result.CostUSD already includes subCtx.ExtraCostUSD (web_read
	// extraction passes) — see Run's Result construction. Report it and fold
	// it into the turn's total here, since baseCtx outlives this call.
	report.CostUSD = result.CostUSD
	baseCtx.AddCost(result.CostUSD)
	return report, nil
}

// subAgentEmit wraps the turn's Emit for one sub-agent: every event it
// forwards is tagged with agentID so the UI can keep that researcher's
// reasoning and tool calls together instead of pouring them into the main
// timeline, and two event types are dropped outright because they address
// the *turn*, not the agent:
//
//   - "token": a sub-agent's streamed final answer (its JSON report) would
//     be appended to the main turn's visible answer. The report reaches the
//     UI through subagent_end instead.
//   - "cost_update": carries one agent.Run's running total, which the
//     frontend assigns (not adds) to the turn's cost, so a sub-agent's
//     small total would overwrite the orchestrator's. Sub-agent spend is
//     folded into the turn total via SubAgentReport.CostUSD instead.
//
// "commentary" is forwarded but tagged: untagged, the frontend treats it as
// "what just streamed in was commentary, not the answer" and clears the
// turn's visible text.
func subAgentEmit(base func(string, map[string]interface{}), agentID string) func(string, map[string]interface{}) {
	if base == nil {
		return nil
	}
	return func(eventType string, payload map[string]interface{}) {
		switch eventType {
		case "token", "cost_update":
			return
		}
		tagged := make(map[string]interface{}, len(payload)+1)
		for k, v := range payload {
			tagged[k] = v
		}
		tagged["agent_id"] = agentID
		base(eventType, tagged)
	}
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
func newSubAgentContext(baseCtx *tools.Context, llmClient llm.ChatClient, task tools.SubAgentTask) *tools.Context {
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
		TavilyUsageThisMonth:   baseCtx.TavilyUsageThisMonth,
		IncrementTavilyUsage:   baseCtx.IncrementTavilyUsage,
		PinnedProvider:         baseCtx.PinnedProvider,
		Emit:                   subAgentEmit(baseCtx.Emit, task.AgentID()),
		MaxTurns:               baseCtx.MaxTurns,
		DeepResearch:           true,
		DisabledTools:          baseCtx.DisabledTools,
		SubAgentRole:           "researcher",
		ResearchBudget:         baseCtx.ResearchBudget,
		SearchDedup:            baseCtx.SearchDedup,
	}
}
