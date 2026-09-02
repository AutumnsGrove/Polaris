package agent

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"

	"polaris/llm"
	"polaris/tools"
)

// maxConcurrentSubAgents caps how many Tier 2 Deep Research sub-agents
// SpawnResearchers runs at once, regardless of how many tasks the
// orchestrator requests — a bounded semaphore, not unbounded goroutines
// (docs/plans/deep-research-two-tier.md's "Budget (session-level)"
// section), so even a legitimately-confirmed wide fan-out doesn't
// thunder-herd the LLM/search APIs all at once. Set in the middle of the
// plan's own complexity bands (2-4 for comparisons, 5-10+ for genuinely
// deep research, capped below Anthropic's "10+" given a personal
// deployment's quota reality) rather than at the top of them — extra
// tasks beyond this queue behind whichever sub-agent finishes first,
// they don't get dropped.
const maxConcurrentSubAgents = 8

// SpawnResearchers runs every task as its own Tier 2 Deep Research
// sub-agent (see RunSubAgent), concurrently up to maxConcurrentSubAgents
// at a time, and returns one SubAgentReport per task in the same order
// tasks were given — positionally, not by completion order, so callers
// can always match a report back to the task that produced it.
//
// baseCtx supplies every sub-agent's shared session-scoped dependencies.
// If baseCtx.ResearchBudget or baseCtx.SearchDedup is nil, it's lazily
// created here and written back onto baseCtx, so a caller that reuses
// the same baseCtx across multiple spawn_researchers tool calls within
// one Deep Research session gets one shared budget/dedup group across
// all of them, not a fresh one per call.
//
// A sub-agent that fails (RunSubAgent returning an error — a network
// failure, a cancelled context, ...) doesn't abort the wave or vanish
// from the results: it's reported back as a placeholder SubAgentReport
// whose one finding states the failure, so the orchestrator's synthesis
// step sees "this angle couldn't be completed" instead of silently
// having fewer sources than it expected.
func SpawnResearchers(reqCtx context.Context, baseCtx *tools.Context, llmClient llm.ChatClient, tasks []tools.SubAgentTask) []tools.SubAgentReport {
	if baseCtx.ResearchBudget == nil {
		baseCtx.ResearchBudget = tools.NewResearchBudget()
	}
	if baseCtx.SearchDedup == nil {
		baseCtx.SearchDedup = &singleflight.Group{}
	}

	sem := make(chan struct{}, min(len(tasks), maxConcurrentSubAgents))
	reports := make([]tools.SubAgentReport, len(tasks))

	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task tools.SubAgentTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			report, err := RunSubAgent(reqCtx, baseCtx, llmClient, task)
			if err != nil {
				report = tools.SubAgentReport{
					Objective: task.Objective,
					Findings: []tools.SubAgentFinding{
						{Claim: fmt.Sprintf("This sub-agent failed to complete: %v", err)},
					},
				}
			}
			reports[i] = report
		}(i, task)
	}
	wg.Wait()

	return reports
}
