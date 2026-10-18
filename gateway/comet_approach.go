// comet_approach.go implements Comet's Approach — one agent.Run over one
// past conversation, classifying it as a Trail (see docs/plans/comet.md's
// "Category breakdown"). Same shape as Weaver (gateway/constellation_weaver.go):
// Go assembles a bounded input text before the loop starts, then the model
// reads it, may look at an image or check prior trails, and ends the run by
// calling record_classification. Unlike Weaver, Approaches have no
// cross-thread dependency — one thread produces exactly one Trail — so they
// run concurrently in small batches rather than strictly sequentially.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
	"polaris/uiblocks"
)

// cometApproachMaxTurns is an Approach's turn cap — a backstop, not an
// expected limit (the classifier only ever calls view_image/search_trails
// zero or more times, then record_classification), same idea as
// weaverMaxTurns.
const cometApproachMaxTurns = 10

// cometApproachBatchSize is how many Approaches run concurrently. The plan
// doc's operator call was ~5: real parallelism without firing the whole
// month's thread count at the LLM provider at once. Unlike Weaver, no
// cross-thread ordering matters here, so this is purely a load bound.
const cometApproachBatchSize = 5

// cometApproachStaleAfter bounds how long a trail row can sit mid-flight
// (classified_at IS NULL) before the sweep treats it as abandoned rather
// than still running — same role shootingStarStaleAfter plays for Weaver.
var cometApproachStaleAfter = 30 * time.Minute

// cometApproachAssistantCharCap is how much of each assistant reply the
// classifier sees. User messages are included in full (the operator's own
// estimate: user messages run roughly ten times shorter than assistant
// replies, so the cost is dominated by this cap); assistant replies are
// truncated to this many characters, enough to catch the gist and tone
// without paying for the full response. See the plan doc's "Classifier
// input, bounded per call regardless of thread length".
const cometApproachAssistantCharCap = 500

// CometClient builds the LLM client Comet runs on — modelID empty means
// "use whatever config.DefaultModel currently resolves to", same
// convention WeaverClient uses.
func CometClient(cfg *config.Config, modelID string) llm.ChatClient {
	modelCfg := cfg.ModelByID(modelID)
	return llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)})
}

// RunCometClassify runs an Approach over every eligible thread for period,
// in concurrent batches, up to limit threads (0 = all). A single thread's
// failure is logged and its trail marked needs_retry rather than aborting
// the batch — the next run sweeps it up.
//
// The classify_started_at CAS (same reasoning as SetConstellationBackfillStarted)
// stops a manual classify and the scheduler's own generation flow from
// independently computing the eligible list and double-processing threads.
func (s *Server) RunCometClassify(period string, limit int) (processed int, err error) {
	if n, werr := s.db.MarkStaleTrailsFailed(cometApproachStaleAfter); werr != nil {
		log.Warn("comet: sweeping stale trail runs failed", "err", werr)
	} else if n > 0 {
		log.Warn("comet: marked stale trail run(s) as needing retry", "count", n)
	}

	if err := s.db.SetCometClassifyStarted(cometClassifyStaleAfter); err != nil {
		return 0, err
	}
	defer func() {
		if clearErr := s.db.ClearCometClassifyStarted(); clearErr != nil {
			log.Warn("comet: clearing classify in-progress flag failed", "err", clearErr)
		}
	}()

	startStr, endStr, ok := cometPeriodDB(period)
	if !ok {
		return 0, fmt.Errorf("invalid comet period %q", period)
	}
	ids, err := s.db.EligibleCometThreads(startStr, endStr, period)
	if err != nil {
		return 0, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	log.Info("comet: classifying threads", "period", period, "eligible", len(ids), "limit", limit)

	for i := 0; i < len(ids); i += cometApproachBatchSize {
		end := i + cometApproachBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		var wg sync.WaitGroup
		stopped := false
		for _, threadID := range batch {
			// Register each concurrent Approach with the server's
			// shutdown-drain tracking — same reasoning as Weaver's turnGate:
			// this runs in its own goroutine outside any net/http recover,
			// and a restart landing mid-batch must not kill it mid-write.
			if !s.TryStartTurn() {
				log.Warn("comet: server is restarting, stopping classify early", "period", period, "processed", processed)
				stopped = true
				break
			}
			wg.Add(1)
			go func(threadID string) {
				defer wg.Done()
				defer s.FinishTurn()
				defer func() {
					if rec := recover(); rec != nil {
						log.Error("panic running comet approach", "thread_id", threadID, "panic", rec)
					}
				}()
				if aerr := s.runApproach(threadID, period); aerr != nil {
					log.Warn("comet: approach failed", "thread_id", threadID, "err", aerr)
				}
			}(threadID)
		}
		wg.Wait()
		processed += len(batch)
		if stopped {
			break
		}
	}
	return processed, nil
}

// runApproach is one thread's classification run. It opens the trail row
// first (so a crash mid-run leaves a sweepable marker), assembles the
// bounded input, gives the model a real hidden thread to log into, and
// writes the finished classification.
func (s *Server) runApproach(threadID, period string) error {
	cfg := s.liveConfig()
	cfgRow, err := s.db.GetCometConfig()
	if err != nil {
		return fmt.Errorf("comet approach: %w", err)
	}
	client := CometClient(cfg, cfgRow.Model)
	modelCfg := cfg.ModelByID(cfgRow.Model)

	trailID, err := s.db.StartTrail(threadID, period)
	if err != nil {
		return fmt.Errorf("comet approach: %w", err)
	}

	fail := func(errMsg string) error {
		if ferr := s.db.FailTrail(trailID, errMsg, 0); ferr != nil {
			log.Warn("comet approach: marking trail failed", "trail_id", trailID, "err", ferr)
		}
		return fmt.Errorf("comet approach: %s", errMsg)
	}

	effectiveID, err := s.db.EffectiveThreadID(threadID)
	if err != nil {
		return fail("resolving effective thread: " + err.Error())
	}
	msgs, err := s.db.GetMessages(effectiveID)
	if err != nil {
		return fail("loading messages: " + err.Error())
	}
	if len(msgs) == 0 {
		// Nothing to classify — mark it done with empty fields rather than
		// retrying forever on a thread whose content vanished (an edited
		// root whose variant is empty, say).
		if ferr := s.db.FinishTrail(trailID, "", "", "", "", 0); ferr != nil {
			return ferr
		}
		return nil
	}

	task, imageFiles := s.cometApproachInput(threadID, period, msgs)

	// A real, ordinary (but hidden) threads/messages/events transcript for
	// this run — same primitives gateway/turn.go uses for real chat, in
	// parallel with the trails row. store.ListThreads/ListThreadsPage/
	// SearchMessages exclude source = 'comet_approach' the same way they
	// exclude 'weaver', so it never appears in the sidebar; GetThreadRaw has
	// no such filter, so it's still fully viewable at its own /t/<uuid> for
	// debugging a classification.
	approachThreadID := uuid.NewString()
	turnID := uuid.NewString()
	title := "Comet approach"
	if th, terr := s.db.GetThreadRaw(threadID); terr == nil && th.Title != "" {
		title = "Comet approach — " + th.Title
	}
	if cerr := s.db.CreateThread(approachThreadID, title, modelCfg.ID, "comet_approach"); cerr != nil {
		log.Warn("comet approach: creating hidden thread failed", "err", cerr)
	}
	if _, aerr := s.db.AddMessage(approachThreadID, "user", task, "[]", "[]", 0, turnID); aerr != nil {
		log.Warn("comet approach: persisting task message failed", "err", aerr)
	}

	agentCtx := s.newApproachToolContext(cfg, cfgRow, client, approachThreadID, turnID, threadID, imageFiles)
	result, err := agent.Run(context.Background(), agentCtx, nil, task)
	if err != nil {
		return fail(err.Error())
	}

	// The hidden thread's assistant message carries the run's own cost, so
	// GetStats' existing messages-based rollup counts it (folded into the
	// Polaris bucket, the same place a Weaver hidden thread's cost lands).
	// trails.cost_usd is a per-trail audit mirror, deliberately not summed
	// into stats anywhere, so nothing double-counts.
	if _, aerr := s.db.AddMessage(approachThreadID, "assistant", strings.TrimSpace(result.Answer), "[]", "[]", result.CostUSD, turnID); aerr != nil {
		log.Warn("comet approach: persisting answer message failed", "err", aerr)
	}

	if result.TrailClassification == nil {
		return fail(fmt.Sprintf("model never called record_classification (hit turn %d)", result.TurnCount))
	}
	tc := result.TrailClassification
	if err := s.db.FinishTrail(trailID, tc.Topic, tc.Mood, tc.Intent, tc.Resolved, result.CostUSD); err != nil {
		return fmt.Errorf("comet approach: %w", err)
	}
	log.Info("comet: classified thread", "thread_id", threadID, "period", period, "topic", tc.Topic, "mood", tc.Mood, "resolved", tc.Resolved)
	return nil
}

// newApproachToolContext builds the tools.Context for one Approach run.
// Deliberately narrow — no SearXNG/web_search/web_read, since an Approach
// reads one already-written conversation and never researches. The three
// tools it does get are view_image (look at an image the thread referenced),
// search_trails (cross-period consistency), and record_classification (the
// mandated ending). Its Emit callback logs every tool call/result into the
// hidden thread's ordinary events table, the same way
// newWeaverToolContext does.
func (s *Server) newApproachToolContext(cfg *config.Config, cfgRow *store.CometConfig, client llm.ChatClient, approachThreadID, turnID, analyzedThreadID string, imageFiles []string) *tools.Context {
	categories, err := s.db.DistinctTrailTopics()
	if err != nil {
		categories = nil
	}
	moods, err := s.db.DistinctTrailMoods()
	if err != nil {
		moods = nil
	}

	ctx := &tools.Context{
		Ctx:                     context.Background(),
		LLM:                     client,
		ApproachRun:             true,
		MaxTurns:                cometApproachMaxTurns,
		ApproachCategoriesInUse: strings.Join(categories, ", "),
		ApproachMoodsInUse:      strings.Join(moods, ", "),
		Blocklist:               s.blocklist,
		ApproachSearchTrails:    func(query string) ([]store.Trail, error) { return s.db.SearchTrails(query, 8) },
		Emit: func(event string, data map[string]interface{}) {
			tool, _ := data["tool"].(string)
			switch event {
			case "tool_call":
				s.db.LogEvent(approachThreadID, "info", "tool."+tool, "tool call started", data, turnID)
			case "tool_result":
				resultText, _ := data["result"].(string)
				level := "info"
				if strings.HasPrefix(resultText, "error:") {
					level = "warn"
				}
				s.db.LogEvent(approachThreadID, level, "tool."+tool, "tool call finished", data, turnID)
			}
		},
	}

	// Image access: the analyzed thread's own attachments already live in
	// its workspace directory, so pointing CodeExecWorkspaceDir/ThreadID at
	// that thread lets view_image's existing "path" source read them
	// unchanged (see cometApproachInput, which lists their filenames in the
	// task text). Multimodal/DescribeImage mirror the classifier's own
	// model: "see" only when it's genuinely vision-capable, "describe"
	// otherwise — exactly view_image's existing gate.
	ctx.CodeExecWorkspaceDir = cfg.CodeExec.WorkspaceDir
	ctx.ThreadID = analyzedThreadID
	ctx.Multimodal = cfg.ModelByID(cfgRow.Model).Multimodal
	if visionCl, ok := visionClient(cfg, cfg.ModelByID(cfgRow.Model)); ok {
		ctx.DescribeImage = func(imgCtx context.Context, imageBase64, mimeType, instructions string) (string, float64, error) {
			return visionCl.DescribeImage(imgCtx, imageBase64, mimeType, instructions)
		}
	}
	return ctx
}

// cometApproachInput assembles the classifier's bounded input text: the
// chronological conversation with user messages in full and assistant
// replies capped, plus a header listing any image files the model may look
// at, plus any earlier trails this thread already has (the cross-month
// continuation context). Tool calls and reasoning are never stored in
// messages in the first place, so there is nothing to strip.
func (s *Server) cometApproachInput(threadID, period string, msgs []store.Message) (task string, imageFiles []string) {
	var b strings.Builder
	b.WriteString("Classify this conversation.\n")

	// Image attachments, listed 1-based by workspace filename so the model
	// can call view_image path=<filename> on any it needs.
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "user" || m.Attachments == "" {
			continue
		}
		var atts []store.Attachment
		if err := json.Unmarshal([]byte(m.Attachments), &atts); err != nil {
			continue
		}
		for _, a := range atts {
			if !strings.HasPrefix(a.ContentType, "image/") || a.WorkspaceFileID == "" || seen[a.WorkspaceFileID] {
				continue
			}
			seen[a.WorkspaceFileID] = true
			imageFiles = append(imageFiles, a.WorkspaceFileID)
		}
	}
	if len(imageFiles) > 0 {
		b.WriteString("\nThis thread included image files you may look at with view_image (path=<filename>): ")
		b.WriteString(strings.Join(imageFiles, ", "))
		b.WriteString("\n")
	}

	// Prior trails for this same thread — the "picked back up" context.
	if prior, err := s.db.TrailsByThread(threadID); err == nil && len(prior) > 0 {
		b.WriteString("\nEarlier trails already recorded for this same thread (continuation context):\n")
		for _, t := range prior {
			fmt.Fprintf(&b, "- %s: topic=%q mood=%q intent=%q resolved=%s\n", t.Period, t.Topic, t.Mood, t.Intent, t.Resolved)
		}
	}

	b.WriteString("\nConversation:\n")
	for _, m := range msgs {
		content := strings.TrimSpace(uiblocks.Flatten(m.Content))
		if content == "" {
			continue
		}
		switch m.Role {
		case "user":
			b.WriteString("\nUser: ")
			b.WriteString(content)
		case "assistant":
			runes := []rune(content)
			truncated := len(runes) > cometApproachAssistantCharCap
			if truncated {
				runes = runes[:cometApproachAssistantCharCap]
			}
			b.WriteString("\nAssistant: ")
			b.WriteString(string(runes))
			if truncated {
				b.WriteString(" ...")
			}
		}
	}
	return b.String(), imageFiles
}
