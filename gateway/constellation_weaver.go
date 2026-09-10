// constellation_weaver.go implements Weaver — Constellation's own agent
// (see docs/plans/constellation.md): one agent.Run per shooting star, with
// its own narrow tool set (tools/search_stars.go, read_star.go,
// create_star.go, update_star.go, link_stars.go), never Polaris's main
// chat agent gaining a tool. Go code does exactly one thing before the
// loop starts — assemble the task text (the raw thread on a first pass, or
// a filtered delta on a revisit) — and everything after that is Weaver's
// own reasoning and tool calls.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"polaris/agent"
	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
	"polaris/tools"
)

// weaverMaxTurns is Weaver's own turn cap — a real conversation thread is
// very unlikely to need Weaver's loop to run longer than this, so it's a
// backstop, not an expected limit, same idea as config.MaxAgentTurns for
// the main chat agent. Deliberately much lower than the main agent's
// default: Weaver never researches, so there's no reason its loop should
// run anywhere near as long.
const weaverMaxTurns = 25

// RunShootingStar runs one shooting star — Weaver reading a single thread
// and deciding what belongs in Constellation — and persists every part of
// the observability trail described in the plan doc's "Database schema":
// the run itself, candidates/events logged as a side effect of Weaver's
// own tool calls, and the final needs_retry/error state on failure.
func RunShootingStar(reqCtx context.Context, db *store.Store, client llm.ChatClient, threadID string) error {
	lastRun, err := db.LastShootingStarRun(threadID)
	if err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}

	msgs, err := db.GetMessages(threadID)
	if err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}
	var lastMessageID int64
	for _, m := range msgs {
		if m.ID > lastMessageID {
			lastMessageID = m.ID
		}
	}

	runID, err := db.StartShootingStarRun(threadID, lastMessageID)
	if err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}

	task, err := weaverTaskText(reqCtx, db, client, threadID, lastRun, msgs, runID)
	if err != nil {
		_ = db.FinishShootingStarRun(runID, "", err.Error(), true)
		return fmt.Errorf("shooting star: %w", err)
	}
	if task == "" {
		// Nothing new since the last run — the caller's eligibility gate
		// (see gateway/constellation_scheduler.go) should already exclude
		// this, but stay a harmless no-op rather than starting an empty
		// run against Weaver.
		_ = db.FinishShootingStarRun(runID, "nothing new since the last pass", "", false)
		return nil
	}

	agentCtx := newWeaverToolContext(reqCtx, db, client, runID, threadID)
	result, err := agent.Run(reqCtx, agentCtx, nil, task)
	if err != nil {
		_ = db.FinishShootingStarRun(runID, "", err.Error(), true)
		return fmt.Errorf("shooting star: %w", err)
	}
	if result.TurnCount > weaverMaxTurns {
		// agent.Run forces a wrap-up answer rather than erroring when it
		// runs out of turns (see agent/driver.go's "Ran out of turns"
		// branch) — TurnCount == maxTurns+1 uniquely identifies that
		// forced path, since every normal completion returns with
		// TurnCount in [1, maxTurns]. Weaver treats hitting the cap as a
		// real failure needing retry, unlike the main chat agent: a
		// forced wrap-up here means Weaver didn't get to finish its own
		// extraction/linking judgment, not that it produced a good-enough
		// answer under time pressure.
		_ = db.FinishShootingStarRun(runID, "", "max_turns_exceeded", true)
		return fmt.Errorf("shooting star: hit turn cap (%d turns)", weaverMaxTurns)
	}

	if err := db.FinishShootingStarRun(runID, strings.TrimSpace(result.Answer), "", false); err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}
	return nil
}

// weaverTaskText assembles Weaver's starting task text — the one piece of
// Go-orchestrated logic before the loop starts (see the plan doc's
// "Revisiting a thread"). Returns "" if this is a revisit with no actual
// new content in the delta (shouldn't happen given the scheduler's own
// delta gate, but stays defensive rather than assuming the caller always
// gates correctly).
func weaverTaskText(reqCtx context.Context, db *store.Store, client llm.ChatClient, threadID string, lastRun *store.ShootingStarRun, msgs []store.Message, runID int64) (string, error) {
	if lastRun == nil {
		// First-ever pass on this thread: no prior notes exist, so
		// there's nothing to filter for — Weaver gets the thread's raw
		// content directly, no filter pass.
		read, err := db.ReadThread(threadID)
		if err != nil {
			return "", err
		}
		return read.Content, nil
	}

	var delta strings.Builder
	for _, m := range msgs {
		if m.ID <= lastRun.LastMessageIDSeen {
			continue
		}
		label := "User"
		if m.Role == "assistant" {
			label = "Assistant"
		}
		if delta.Len() > 0 {
			delta.WriteString("\n\n")
		}
		delta.WriteString(label + ": " + m.Content)
	}
	deltaText := delta.String()
	if deltaText == "" {
		return "", nil
	}

	priorStars, err := db.StarsByThread(threadID)
	if err != nil {
		return "", err
	}
	var notes strings.Builder
	for i, star := range priorStars {
		if i > 0 {
			notes.WriteString("; ")
		}
		notes.WriteString(star.Title + " — " + star.Summary)
	}
	instruction := fmt.Sprintf(prompts.Get().Weaver.RevisitInstruction, notes.String())

	filtered, filterCost, ferr := tools.FilterExtractedText(reqCtx, client, prompts.Get().Tools.ThreadReadFilterSystem, deltaText, instruction)
	if ferr != nil {
		// Degrade to the raw delta rather than failing the whole run —
		// the same choice web_read/search_chats' own FilterExtractedText
		// callers make when the filter pass itself errors.
		return deltaText, nil
	}
	_ = db.RecordShootingStarEvent(runID, "filter_pass", instruction, filtered, filterCost)
	return filtered, nil
}

// newWeaverToolContext builds the tools.Context for one shooting star —
// deliberately narrow: no SearXNG/Brave/Parallel/Tavily/Embed, since
// Weaver's whole job is reading and inferring from already-written chat
// content, never researching or computing anything new (see the plan
// doc's "Weaver" design principle). The five WeaverX closures log to
// shooting_star_events as a side effect of each call, giving the
// per-tool-call trace the plan doc's "Full observability from day one"
// principle asks for.
func newWeaverToolContext(reqCtx context.Context, db *store.Store, client llm.ChatClient, runID int64, threadID string) *tools.Context {
	return &tools.Context{
		Ctx:       reqCtx,
		LLM:       client,
		Emit:      func(string, map[string]interface{}) {},
		WeaverRun: true,
		MaxTurns:  weaverMaxTurns,

		WeaverSearchStars: func(query string) ([]store.StarSearchResult, error) {
			results, err := db.SearchStars(query, 10)
			args, _ := json.Marshal(map[string]string{"query": query})
			_ = db.RecordShootingStarEvent(runID, "search_stars", string(args), fmt.Sprintf("%d results", len(results)), 0)
			return results, err
		},
		WeaverReadStar: func(starID int64) (*store.Star, error) {
			star, err := db.GetStar(starID)
			args, _ := json.Marshal(map[string]int64{"star_id": starID})
			resultText := "not found"
			if err == nil {
				resultText = star.Title
			}
			_ = db.RecordShootingStarEvent(runID, "read_star", string(args), resultText, 0)
			return star, err
		},
		WeaverCreateStar: func(title, category, summary, body string, tags []string, confidenceClass string, isPersonal bool) (int64, error) {
			id, err := db.CreateStar(store.Star{
				Title: title, Category: category, Summary: summary, Body: body,
				Tags: tags, Confidence: confidenceClass, IsPersonal: isPersonal, Status: "auto",
			})
			if err != nil {
				return 0, err
			}
			_ = db.LinkStarSource(id, threadID)
			_ = db.RecordShootingStarCandidate(runID, title, confidenceClass, "new_star", "", &id)
			args, _ := json.Marshal(map[string]interface{}{"title": title, "category": category, "is_personal": isPersonal})
			_ = db.RecordShootingStarEvent(runID, "create_star", string(args), fmt.Sprintf("star_id=%d", id), 0)
			return id, nil
		},
		WeaverUpdateStar: func(starID int64, summary, body string, tags []string, confidenceClass string, isPersonal bool) error {
			err := db.UpdateStar(starID, summary, body, tags, confidenceClass, isPersonal)
			if err != nil {
				return err
			}
			_ = db.LinkStarSource(starID, threadID)
			_ = db.RecordShootingStarCandidate(runID, "", confidenceClass, "merged", "", &starID)
			args, _ := json.Marshal(map[string]interface{}{"star_id": starID, "is_personal": isPersonal})
			_ = db.RecordShootingStarEvent(runID, "update_star", string(args), "updated", 0)
			return nil
		},
		WeaverLinkStars: func(starIDA, starIDB int64, reasoning string) error {
			err := db.LinkStars(starIDA, starIDB, reasoning)
			args, _ := json.Marshal(map[string]interface{}{"star_id_a": starIDA, "star_id_b": starIDB, "reasoning": reasoning})
			_ = db.RecordShootingStarEvent(runID, "link_stars", string(args), "linked", 0)
			return err
		},
	}
}
