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

	// threadID is always a root id (EligibleConstellationThreads' own doc
	// comment on why) but the actual current content of an edited/
	// regenerated thread lives in whichever hidden variant
	// active_variant_id points at, same EffectiveThreadID resolution every
	// other read path (loadHistory, GetThreadEvents, handleRegenerateTitle)
	// already does. Everything else in this function — run tracking,
	// star_sources — deliberately keeps using threadID (the root), not
	// this: a variant's own id isn't independently addressable and its
	// title is always "", so linking a star to it is exactly the bug this
	// whole resolution exists to avoid.
	effectiveID, err := db.EffectiveThreadID(threadID)
	if err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}

	msgs, err := db.GetMessages(effectiveID)
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

	task, err := weaverTaskText(reqCtx, db, client, threadID, effectiveID, lastRun, msgs, runID)
	if err != nil {
		warnOnErr("finishing failed shooting star run", db.FinishShootingStarRun(runID, "", err.Error(), true))
		return fmt.Errorf("shooting star: %w", err)
	}
	if task == "" {
		// Nothing new since the last run — the caller's eligibility gate
		// (see gateway/constellation_scheduler.go) should already exclude
		// this, but stay a harmless no-op rather than starting an empty
		// run against Weaver.
		warnOnErr("finishing no-op shooting star run", db.FinishShootingStarRun(runID, "nothing new since the last pass", "", false))
		return nil
	}

	agentCtx := newWeaverToolContext(reqCtx, db, client, runID, threadID)
	result, err := agent.Run(reqCtx, agentCtx, nil, task)
	if err != nil {
		warnOnErr("finishing failed shooting star run", db.FinishShootingStarRun(runID, "", err.Error(), true))
		return fmt.Errorf("shooting star: %w", err)
	}

	// result.CostUSD is agent.Run's own running total (agent/driver.go
	// accumulates it once per completion call) — the real cost of this
	// shooting star, previously never recorded anywhere. Every other event
	// logs a real per-tool-call trace at cost 0 (tool dispatch itself isn't
	// billed, see newWeaverToolContext's callbacks); this one row is what
	// actually carries the dollar figure into FinishShootingStarRun's
	// SUM(shooting_star_events.cost_usd) rollup. Logged before either exit
	// branch below — hitting the turn cap still means real, billed
	// completion calls happened on the way there, not a $0 no-op.
	warnOnErr("recording final_answer event", db.RecordShootingStarEvent(runID, "final_answer", "", strings.TrimSpace(result.Answer), result.CostUSD))

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
		warnOnErr("finishing turn-capped shooting star run", db.FinishShootingStarRun(runID, "", "max_turns_exceeded", true))
		return fmt.Errorf("shooting star: hit turn cap (%d turns)", weaverMaxTurns)
	}

	if err := db.FinishShootingStarRun(runID, strings.TrimSpace(result.Answer), "", false); err != nil {
		return fmt.Errorf("shooting star: %w", err)
	}
	return nil
}

// RunShootingStarRecovered wraps RunShootingStar with a panic recovery —
// same reasoning as gateway/pulsar_scheduler.go's firePulseRecovered: both
// the scheduler's tick and BackfillConstellation call this from their own
// top-level goroutine/call stack, outside any net/http recover, so an
// unrecovered panic anywhere in Weaver's loop (agent.Run, any of the five
// tool handlers) would otherwise take down the whole Polaris process
// instead of just failing that one shooting star.
func RunShootingStarRecovered(reqCtx context.Context, db *store.Store, client llm.ChatClient, threadID string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic running shooting star", "thread_id", threadID, "panic", rec)
			err = fmt.Errorf("shooting star: panic: %v", rec)
		}
	}()
	return RunShootingStar(reqCtx, db, client, threadID)
}

// turnGate lets a shooting star register with the server's shutdown-drain
// tracking (see server.go's TryStartTurn/FinishTurn) so an in-flight
// Weaver run isn't silently killed mid-write by an ordinary `polaris
// restart`/`polaris update` — the exact same registration firePulse
// (pulsar_scheduler.go) already does for a Pulsar pulse, and for the same
// reason: this runs as a background turn with no live WebSocket/HTTP
// client ever attached, so without it a restart's drain window
// (WaitForActiveTurns) has no way to know a shooting star is in flight at
// all and the process just exits out from under it mid-DB-write.
//
// A plain struct of two funcs, not *Server itself — BackfillConstellation
// must also work from the bare-metal CLI's own `polaris constellation
// backfill` invocation (cmd/constellation_backfill.go), a separate,
// one-shot OS process with no *Server/drain concept to register against
// at all. That path uses NoopTurnGate instead.
type turnGate struct {
	tryStart func() bool
	finish   func()
}

// NoopTurnGate always allows a shooting star to proceed and never gates it
// on shutdown — for callers with no *Server to register against (the
// bare-metal CLI's own `polaris constellation backfill` process, which
// isn't part of the long-running `polaris run` server at all).
func NoopTurnGate() turnGate {
	return turnGate{tryStart: func() bool { return true }, finish: func() {}}
}

// shootingStarTurnGate registers a shooting star with this server's own
// shutdown-drain tracking — for the two callers that actually run inside
// the long-lived `polaris run` process: the scheduler's own tick
// (RunConstellationScheduler) and the Docker-mode HTTP backfill handler
// (handleConstellationBackfill).
func (s *Server) shootingStarTurnGate() turnGate {
	return turnGate{tryStart: s.TryStartTurn, finish: s.FinishTurn}
}

// weaverTaskText assembles Weaver's starting task text — the one piece of
// Go-orchestrated logic before the loop starts (see the plan doc's
// "Revisiting a thread"). Returns "" if this is a revisit with no actual
// new content in the delta (shouldn't happen given the scheduler's own
// delta gate, but stays defensive rather than assuming the caller always
// gates correctly). threadID (root, for StarsByThread — star_sources is
// always keyed by root) and effectiveID (root's currently-active variant,
// for reading actual message content — see RunShootingStar's own doc
// comment on why these two can differ) are deliberately separate params,
// not one id used for both.
func weaverTaskText(reqCtx context.Context, db *store.Store, client llm.ChatClient, threadID, effectiveID string, lastRun *store.ShootingStarRun, msgs []store.Message, runID int64) (string, error) {
	if lastRun == nil {
		// First-ever pass on this thread: no prior notes exist, so
		// there's nothing to filter for — Weaver gets the thread's raw
		// content directly, no filter pass.
		read, err := db.ReadThread(effectiveID)
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
	warnOnErr("recording filter_pass event", db.RecordShootingStarEvent(runID, "filter_pass", instruction, filtered, filterCost))
	return filtered, nil
}

// warnOnErr logs a failed observability/side-effect write rather than
// silently discarding it (the previous `_ = db.RecordX(...)` shape) — a
// dropped shooting_star_candidates/shooting_star_events/star_sources row
// leaves the run looking clean in every trace table even though something
// didn't actually get recorded, directly undercutting the plan doc's "Full
// observability from day one" principle.
func warnOnErr(op string, err error) {
	if err != nil {
		log.Warn("constellation weaver: "+op+" failed", "err", err)
	}
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
	categories, err := db.DistinctCategories()
	if err != nil {
		// Not fatal — the escape hatch just falls back to guessing blind,
		// same as before this existed.
		categories = nil
	}

	// seenStars is a defense-in-depth backstop against prompt injection:
	// thread content (which can include text originally fetched from the
	// open web by web_search/web_read during a normal chat turn) is the
	// only input to Weaver's reasoning, and its only defense against
	// steering a destructive write is the prose framing in weaver.system.
	// The plan doc's own "Weaver's tools" section already states read_star
	// is "Mandatory before update_star or link_stars, never optional" —
	// this just makes that an enforced invariant instead of a prompt
	// instruction a sufficiently-adversarial thread could talk Weaver out
	// of: update_star/link_stars may only target a star_id this exact run
	// has already surfaced via search_stars/read_star (or just created
	// itself), never one that appears in a tool call with no prior lookup
	// in this run at all.
	seenStars := map[int64]bool{}
	markSeen := func(id int64) { seenStars[id] = true }
	requireSeen := func(id int64) error {
		if !seenStars[id] {
			return fmt.Errorf("star_id %d hasn't been looked up in this run yet — call search_stars/read_star on it first", id)
		}
		return nil
	}

	return &tools.Context{
		Ctx:       reqCtx,
		LLM:       client,
		WeaverRun: true,
		MaxTurns:  weaverMaxTurns,
		// Emit only ever sees a "tool_result" whose result starts with
		// "error:" here — every successful call is already logged by its
		// own WeaverX closure below. Before this, a tool call that failed
		// validation before ever reaching a WeaverX closure (a bad star_id,
		// star_id_a == star_id_b, a missing required field) left zero trace
		// anywhere: emitToolError (tools/registry.go) only calls ctx.Emit,
		// which was a no-op for Weaver.
		Emit: func(event string, data map[string]interface{}) {
			if event != "tool_result" {
				return
			}
			resultText, _ := data["result"].(string)
			if !strings.HasPrefix(resultText, "error:") {
				return
			}
			tool, _ := data["tool"].(string)
			warnOnErr("recording tool validation failure", db.RecordShootingStarEvent(runID, tool, "", resultText, 0))
		},
		WeaverCategoriesInUse: strings.Join(categories, ", "),

		WeaverSearchStars: func(query string) ([]store.StarSearchResult, error) {
			results, err := db.SearchStars(query, 10)
			for _, r := range results {
				markSeen(r.ID)
			}
			args, _ := json.Marshal(map[string]string{"query": query})
			warnOnErr("recording search_stars event", db.RecordShootingStarEvent(runID, "search_stars", string(args), fmt.Sprintf("%d results", len(results)), 0))
			return results, err
		},
		WeaverReadStar: func(starID int64) (*store.Star, error) {
			star, err := db.GetStar(starID)
			args, _ := json.Marshal(map[string]int64{"star_id": starID})
			resultText := "not found"
			if err == nil {
				markSeen(starID)
				resultText = star.Title
			}
			warnOnErr("recording read_star event", db.RecordShootingStarEvent(runID, "read_star", string(args), resultText, 0))
			return star, err
		},
		WeaverCreateStar: func(title, category, summary, body string, tags []string, confidenceClass string, isPersonal bool, reasoning string) (int64, error) {
			id, err := db.CreateStar(store.Star{
				Title: title, Category: category, Summary: summary, Body: body,
				Tags: tags, Confidence: confidenceClass, IsPersonal: isPersonal, Status: "auto",
			})
			if err != nil {
				return 0, err
			}
			markSeen(id)
			warnOnErr("linking star source", db.LinkStarSource(id, threadID))
			warnOnErr("recording shooting star candidate", db.RecordShootingStarCandidate(runID, title, confidenceClass, "new_star", reasoning, &id))
			args, _ := json.Marshal(map[string]interface{}{"title": title, "category": category, "is_personal": isPersonal})
			warnOnErr("recording create_star event", db.RecordShootingStarEvent(runID, "create_star", string(args), fmt.Sprintf("star_id=%d", id), 0))
			return id, nil
		},
		WeaverUpdateStar: func(starID int64, summary, body string, tags []string, confidenceClass string, isPersonal *bool, reasoning string) error {
			if err := requireSeen(starID); err != nil {
				return err
			}
			// "" for title: update_star's own tool schema has no title field
			// (Weaver never retitles an existing star this way) — see
			// store.UpdateStar's doc comment on the "" == "leave as-is" contract.
			if err := db.UpdateStar(starID, "", summary, body, tags, confidenceClass, isPersonal); err != nil {
				return err
			}
			warnOnErr("linking star source", db.LinkStarSource(starID, threadID))
			warnOnErr("recording shooting star candidate", db.RecordShootingStarCandidate(runID, "", confidenceClass, "merged", reasoning, &starID))
			args, _ := json.Marshal(map[string]interface{}{"star_id": starID, "is_personal": isPersonal})
			warnOnErr("recording update_star event", db.RecordShootingStarEvent(runID, "update_star", string(args), "updated", 0))
			return nil
		},
		WeaverLinkStars: func(starIDA, starIDB int64, reasoning string) error {
			if err := requireSeen(starIDA); err != nil {
				return err
			}
			if err := requireSeen(starIDB); err != nil {
				return err
			}
			err := db.LinkStars(starIDA, starIDB, reasoning)
			args, _ := json.Marshal(map[string]interface{}{"star_id_a": starIDA, "star_id_b": starIDB, "reasoning": reasoning})
			// Logged after checking err, with the real outcome — previously
			// this unconditionally logged "linked" even when db.LinkStars
			// failed (e.g. a bad id tripping the FK constraint), leaving a
			// false-positive row in shooting_star_events.
			result := "linked"
			if err != nil {
				result = "error: " + err.Error()
			}
			warnOnErr("recording link_stars event", db.RecordShootingStarEvent(runID, "link_stars", string(args), result, 0))
			return err
		},
	}
}
