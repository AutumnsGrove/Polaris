package tools

import (
	"encoding/json"
	"strings"
)

// SubAgentTask is one unit of work the orchestrator hands to a Tier 2
// Deep Research sub-agent (agent.RunSubAgent) — see
// docs/plans/deep-research-two-tier.md's "Sub-agents" section's "Task
// spec" bullet (explicit objective, expected output format, tool/source
// guidance, task boundaries). The output-format instruction itself lives
// in prompts.yaml's agent.subagent_task template, not here, so tuning
// how sub-agents are told to structure their JSON answer doesn't require
// a rebuild.
type SubAgentTask struct {
	Objective string
	Guidance  string
}

// SubAgentFinding is one claim with its supporting sources — the unit
// Tier 2 Deep Research sub-agents report back in, instead of free prose,
// specifically to avoid the "aggregator invents a hallucinated middle
// ground between disagreeing sub-agents" failure mode (see
// docs/plans/deep-research-two-tier.md's "Synthesis" section). This is
// what makes the final answer come from claims backed by sources, not
// just a paragraph that happens to mention some URLs — the whole point
// of Polaris's sourcing-first design, one level up from a single
// citation list.
type SubAgentFinding struct {
	Claim   string   `json:"claim"`
	Sources []string `json:"sources"`
}

// SubAgentReport is one sub-agent's complete output, handed back to the
// orchestrator for synthesis.
type SubAgentReport struct {
	// Objective echoes the task the orchestrator assigned this sub-agent
	// — always set from the caller-supplied fallbackObjective when the
	// model's own JSON omits or the model can't be trusted to state it
	// consistently, so the orchestrator never has to guess which
	// sub-agent a report came from.
	Objective string            `json:"objective"`
	Findings  []SubAgentFinding `json:"findings"`

	// Citations is the sub-agent's own full gathered citation list (real
	// titles, not just the bare URL strings a model might echo in a
	// finding's Sources) — from its actual web_search/web_read/
	// reference_lookup calls, unrelated to whether the model's JSON
	// itself parsed. json:"-" since this is never part of the model's
	// own output; ParseSubAgentReport fills it in from the caller-
	// supplied citations regardless of which branch produced Findings.
	Citations []Citation `json:"-"`
}

// ParseSubAgentReport turns a sub-agent's raw final-answer text into a
// SubAgentReport. Sub-agents are prompted to answer in the JSON shape
// above (DSV4 Flash's confirmed JSON-mode support makes this practical —
// see the plan doc's "Synthesis" section), but a model can always ignore
// that instruction or wrap it in prose/markdown fencing, so this never
// hard-fails: any answer that isn't parseable as a findings array with at
// least one entry degrades to a single finding whose claim is the whole
// raw answer, backed by whatever citations the sub-agent's own tool
// calls actually gathered (citations, formatted down to bare URLs for
// the fallback finding's Sources) — still real sources, just not
// claim-by-claim attributed. fallbackObjective always wins over whatever
// the JSON itself claims its objective was, since that field is
// bookkeeping for the orchestrator, not something worth trusting model
// output for. citations — the sub-agent's full, real (title+URL) list —
// is always attached to the returned report's Citations field regardless
// of which branch produced Findings, so a successfully-parsed report
// never loses them just because the model didn't echo them.
func ParseSubAgentReport(fallbackObjective, rawAnswer string, citations []Citation) SubAgentReport {
	if block := extractJSONBlock(rawAnswer); block != "" {
		var report SubAgentReport
		if err := json.Unmarshal([]byte(block), &report); err == nil && len(report.Findings) > 0 {
			report.Objective = fallbackObjective
			report.Citations = citations
			return report
		}
	}

	sources := make([]string, len(citations))
	for i, c := range citations {
		sources[i] = c.URL
	}
	return SubAgentReport{
		Objective: fallbackObjective,
		Findings: []SubAgentFinding{
			{Claim: rawAnswer, Sources: sources},
		},
		Citations: citations,
	}
}

// extractJSONBlock pulls a candidate JSON object out of raw, tolerating
// a fenced ```json ... ``` (or plain ```...```) code block — models
// routinely wrap structured output in one even when asked for bare JSON
// — or a bare object with surrounding prose stripped by taking the first
// '{' through the last '}'. Returns "" if nothing that looks like a JSON
// object is present; the caller treats that the same as a parse failure.
func extractJSONBlock(raw string) string {
	trimmed := strings.TrimSpace(raw)

	if i := strings.Index(trimmed, "```"); i != -1 {
		rest := trimmed[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if end := strings.Index(rest, "```"); end != -1 {
			if candidate := strings.TrimSpace(rest[:end]); strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return trimmed[start : end+1]
}
