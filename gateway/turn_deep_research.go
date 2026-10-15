package gateway

import (
	"encoding/json"

	"polaris/tools"
)

// carryDeepResearch keeps Deep Research on for the reply to its own plan
// confirmation. A Deep Research turn that plans before spawning ends on an
// ask_user_question carrying a structured Plan; the person's answer ("run it",
// or a change to the plan) is the next ordinary message, and it has nothing in it
// that would earn Deep Research again — Oracle's deep_research check says "no"
// to "run it", and a promoted turn deliberately never touches the thread's
// sticky toggle. Without this, that second turn has no spawn_researchers tool
// and the plan can never run.
//
// Keyed off the previous assistant message's persisted Plan rather than any
// Oracle state, so it holds for a manual toggle that was later switched off and
// with Oracle disabled, and it chains by itself: a "change the plan" reply makes
// the model ask again (another Plan, another carried turn), and the chain ends
// the moment a turn finishes without one — i.e. once the researchers have run.
// Not offered under Research off, where spawn_researchers can't exist anyway.
func (t *turnRun) carryDeepResearch() {
	if t.agentCtx.DeepResearch || t.msg.NoResearch {
		return
	}
	pending, oracleJSON, err := t.s.db.LastAssistantPendingQuestionBefore(t.storageThreadID, t.userMsgID)
	if err != nil {
		log.Warn("deep research: reading the previous turn's pending question failed, not carrying", "thread", t.storageThreadID, "err", err)
		return
	}
	if !awaitingResearchPlan(pending, oracleJSON) {
		return
	}
	t.agentCtx.DeepResearch = true
	t.wireResearchers()
	t.s.db.LogEvent(t.threadID, "info", "oracle", "deep research carried over from a pending plan confirmation", nil, t.turnID)
}

// awaitingResearchPlan reports whether the previous assistant turn was a Deep
// Research plan confirmation: it ended on a question carrying a structured Plan,
// or — for a model that described the plan in prose and left the plan argument
// off — it was promoted to Deep Research by Oracle and ended on any question
// (the first thing a promoted turn does is ask). Any other clarifying question
// must not drag the next turn onto the expensive tier.
func awaitingResearchPlan(pendingJSON, oracleJSON string) bool {
	if pendingJSON == "" {
		return false
	}
	var q tools.PendingQuestion
	if err := json.Unmarshal([]byte(pendingJSON), &q); err != nil {
		return false
	}
	if q.Plan != nil {
		return true
	}
	var o OracleResult
	return oracleJSON != "" && json.Unmarshal([]byte(oracleJSON), &o) == nil && o.DeepResearch
}
