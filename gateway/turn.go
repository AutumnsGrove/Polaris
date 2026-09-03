package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/prompts"
	"polaris/tools"
)

// requestLocation, when non-nil, asks the connected browser for a live
// GPS fix and blocks (bounded by locationRequestTimeout, or waitCtx being
// cancelled) for its answer — see location_broker.go. Nil on turns with
// no live client to ask, e.g. POST /api/ask (see ask.go).
func (s *Server) handleTurn(ctx context.Context, msg ClientMessage, send func(ServerEvent), requestLocation func(waitCtx context.Context, threadID string) (string, bool)) {
	cfg := s.liveConfig()

	threadID := msg.ThreadID
	isNewThread := threadID == ""
	if isNewThread {
		threadID = uuid.NewString()
	}

	// Covers this entire function, every early return included — see
	// markTurnInFlight's doc comment for what this is actually for
	// (letting handleGetThread tell a pulse that's still genuinely
	// running apart from one that crashed, since a pulse has no live
	// WebSocket connection for the frontend's usual heuristic to key
	// off). Marked here, right after threadID is finalized, rather than
	// down in ws.go/ask.go's callers, so this also covers handleTurn's
	// own synchronous callers (handleAsk) without each needing its own
	// copy of this bookkeeping.
	s.markTurnInFlight(threadID)
	defer s.clearTurnInFlight(threadID)

	// turnID ties together the user message, the assistant message it
	// produces, and every event (thinking/tool call/tool result) logged
	// while this turn runs — the join key loadHistory's sibling on the
	// frontend (openThread) uses to regroup a past turn's timeline after
	// a page reload, instead of that history being stranded in the
	// events table with no way to tell one turn's events from another's
	// in the same thread.
	turnID := uuid.NewString()

	// storageThreadID is where this turn's messages/events actually get
	// persisted — threadID itself for a brand-new thread or a plain
	// continuation, but a freshly forked thread for an edit/retry. Every
	// DB write from here on uses storageThreadID; threadID stays reserved
	// for what the client sees (ServerEvent.ThreadID) and root-level
	// concerns (the title). Keeping the client-facing thread id stable
	// across edits/regenerates is the whole point: the sidebar entry, the
	// URL, ThreadMenu — none of it needs to know a fork ever happened.
	//
	// Retry/edit no longer deletes anything (the old DeleteMessagesFromAnd
	// AddMessage behavior) — the reply about to be replaced gets a
	// permanent home of its own first (ForkThread), so it stays reachable
	// afterward via the variant switcher instead of being gone for good.
	//
	// isFirstMessageEdit is checked against the pre-fork effective thread,
	// since nothing downstream can prove this was the thread's opening
	// question once ForkThread's copy has run. Only regenerating the title
	// in this case (not on every retry) matters because the title
	// describes the thread as a whole: editing turn 5 of an established
	// conversation shouldn't retitle it around just that turn, but editing
	// turn 1 means the question the current title was generated from
	// doesn't exist anymore.
	storageThreadID := threadID
	isFirstMessageEdit := false
	if !isNewThread {
		effectiveID, err := s.db.EffectiveThreadID(threadID)
		if err != nil {
			s.db.LogEvent(threadID, "error", "turn", "resolving active variant failed", map[string]interface{}{"err": err.Error()}, turnID)
			send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
			return
		}
		if msg.EditFromID != 0 {
			isFirstMessageEdit, err = s.db.IsFirstMessage(effectiveID, msg.EditFromID)
			if err != nil {
				s.db.LogEvent(threadID, "error", "turn", "checking edit/retry position failed", map[string]interface{}{"err": err.Error()}, turnID)
				send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
				return
			}
			atIndex, err := s.db.MessageIndex(effectiveID, msg.EditFromID)
			if err != nil {
				s.db.LogEvent(threadID, "error", "turn", "locating edit/retry position failed", map[string]interface{}{"err": err.Error()}, turnID)
				send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
				return
			}
			forkID, err := s.db.ForkThread(threadID, effectiveID, atIndex)
			if err != nil {
				s.db.LogEvent(threadID, "error", "turn", "forking thread for edit/retry failed", map[string]interface{}{"err": err.Error()}, turnID)
				send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
				return
			}
			if err := s.db.SetActiveVariant(threadID, forkID); err != nil {
				s.db.LogEvent(threadID, "error", "turn", "activating forked variant failed", map[string]interface{}{"err": err.Error()}, turnID)
				send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
				return
			}
			storageThreadID = forkID
		} else {
			storageThreadID = effectiveID
		}
	}

	requestedModel := msg.Model
	if requestedModel == "" {
		requestedModel = s.effectiveDefaultModel(cfg)
	}
	modelCfg := cfg.ModelByID(requestedModel)
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		// WithSessionID is a no-op for provider stickiness here: every
		// model in the registry sets Provider above, and OpenRouter
		// ignores session_id sticky routing whenever provider.order is
		// present (see llm.Client's sessionID doc comment). Cache
		// stability across the thread comes from OpenRouter preferring
		// Provider[0] while it's healthy, not from this call.
		WithSessionID(threadID)
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	if isNewThread {
		title := msg.Content
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		source := msg.Source
		if source == "" {
			source = "web"
		}
		if err := s.db.CreateThread(threadID, title, modelCfg.ID, source); err != nil {
			s.db.LogEvent(threadID, "error", "turn", "creating thread failed", map[string]interface{}{"err": err.Error()}, turnID)
			send(ServerEvent{Type: "error", Message: err.Error()})
			return
		}
		if msg.PulsarRoutineID != 0 {
			// A hard failure here, not a log-and-continue: an unlinked
			// pulse thread isn't invisible (it still shows up in the
			// normal sidebar list, since pulsar-sourced threads aren't
			// filtered like Atlas's are), but it's permanently excluded
			// from ListPulsarPulses/UnreadPulseCounts — both filter on
			// pulsar_routine_id — with no repair path once this turn
			// finishes. Aborting before spending an LLM turn on a thread
			// that can't function as a pulse from /pulsar's perspective
			// costs nothing extra either way: firePulse already stamps
			// last_run_at before calling in here (deliberately, so a
			// crash mid-turn can't trigger a same-minute re-fire — see
			// its doc comment), so this routine simply produces no pulse
			// for this scheduled slot and tries again at the next one —
			// the same outcome a turn failure further downstream would
			// already have.
			if err := s.db.SetThreadPulsarRoutine(threadID, msg.PulsarRoutineID); err != nil {
				s.db.LogEvent(threadID, "error", "turn", "linking pulse thread to routine failed", map[string]interface{}{"err": err.Error()}, turnID)
				send(ServerEvent{Type: "error", Message: err.Error()})
				return
			}
		}
	} else if isFirstMessageEdit {
		// Same truncated-placeholder treatment as a brand-new thread above
		// — the old title (placeholder or generated) described the
		// question that just got deleted, so the sidebar shouldn't keep
		// showing it while this turn runs. generateTitle below replaces
		// this with a real title once the new answer lands, same as for
		// a new thread.
		title := msg.Content
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		if err := s.db.SetThreadTitle(threadID, title); err != nil {
			log.Warn("failed to reset placeholder title for first-message edit", "err", err)
			s.db.LogEvent(threadID, "warn", "title", "resetting placeholder title for first-message edit failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	// Write through this turn's config as the thread's new sticky default
	// — always threadID (the root), same target SetThreadTitle uses, so
	// reopening the thread later (handleGetThread reads by root id) shows
	// what this turn actually ran with regardless of whether an edit/retry
	// forked storageThreadID above. Best-effort: a failure here shouldn't
	// abort an otherwise-working turn, same reasoning as TouchUpdatedAt.
	if err := s.db.SetThreadConfig(threadID, modelCfg.ID, msg.FocusMode, msg.DeepResearch); err != nil {
		log.Warn("failed to persist thread turn config", "thread", threadID, "err", err)
		s.db.LogEvent(threadID, "warn", "turn", "persisting thread turn config failed", map[string]interface{}{"err": err.Error()}, turnID)
	}

	history, err := s.loadHistory(storageThreadID, 0)
	if err != nil {
		s.db.LogEvent(storageThreadID, "error", "turn", "loading history failed", map[string]interface{}{"err": err.Error()}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
		return
	}

	// Persist the user message before running the agent, not after — so
	// it (and its ID, needed for retry/edit) survives even if the turn
	// below errors out. Previously a failed turn left no record at all.
	// SttCostUSD folds in push-to-talk transcription cost, if this
	// message originated from a voice memo.
	//
	// Always a plain AddMessage now, even for a retry/edit — ForkThread
	// above already left storageThreadID with exactly the shared prefix
	// and nothing else, so there's nothing left to delete the way
	// DeleteMessagesFromAndAddMessage used to.
	userMsgID, err := s.db.AddMessage(storageThreadID, "user", msg.Content, "[]", "[]", msg.SttCostUSD, turnID)
	if err != nil {
		s.db.LogEvent(storageThreadID, "error", "turn", "persisting user message failed", map[string]interface{}{"err": err.Error()}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
		return
	}
	// See TouchUpdatedAt's doc comment: AddMessage above just bumped
	// storageThreadID's own updated_at, which is invisible to ListThreads
	// once storageThreadID is a forked variant (post edit/retry) rather
	// than threadID itself — without this, the thread silently stops
	// climbing the sidebar's recency order the moment it's ever been
	// edited, even while actively being used.
	if err := s.db.TouchUpdatedAt(threadID); err != nil {
		log.Warn("failed to bump thread recency", "thread", threadID, "err", err)
	}
	send(ServerEvent{Type: "user_message", ThreadID: threadID, UserMessageID: userMsgID})

	if msg.AttachmentID != "" {
		if err := s.db.SetMessageAttachment(userMsgID, msg.AttachmentFilename, msg.AttachmentContentType); err != nil {
			log.Warn("failed to record attachment metadata", "err", err)
			s.db.LogEvent(storageThreadID, "warn", "turn", "recording attachment metadata failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	s.db.LogEvent(storageThreadID, "info", "turn", "turn started", map[string]interface{}{
		"model":         modelCfg.ID,
		"is_new_thread": isNewThread,
		"voice_mode":    msg.VoiceMode,
		"is_retry":      msg.EditFromID != 0,
	}, turnID)

	// reasoningBuf accumulates one "reasoning" burst — a reasoning-capable
	// model's native hidden-thinking stream arrives as dozens-to-hundreds
	// of tiny chunks, so persisting one DB row per chunk (like "token")
	// would be excessive. Instead it's flushed as a single row exactly
	// when something else interrupts it (see emit below) — the same
	// moment the frontend's closeOpenReasoning marks the live timeline
	// item "done" — so a reopened thread's reasoning lands in the same
	// position in the timeline it actually streamed in, not tacked onto
	// the end regardless of when it really happened.
	var reasoningBuf strings.Builder
	flushReasoning := func() {
		if reasoningBuf.Len() == 0 {
			return
		}
		s.db.LogEvent(storageThreadID, "info", "turn", "reasoning", map[string]interface{}{"content": reasoningBuf.String()}, turnID)
		reasoningBuf.Reset()
	}

	// emitMu serializes the whole emit() body below — reasoningBuf's
	// mutation isn't otherwise safe for concurrent use, and neither is
	// interleaving arbitrary tool_call/tool_result events with it. Needed
	// now that agent.Run dispatches a turn's tool calls concurrently (see
	// dispatchToolCallsConcurrently): several handlers can call ctx.Emit
	// at the same instant. send() has its own separate mutex already
	// (ws.go) for the WebSocket write specifically; this one covers
	// everything else emit() does around that write.
	var emitMu sync.Mutex

	// emit both streams the event to the browser (send) and, for the
	// subset worth keeping as durable evidence, persists it to the events
	// table — "token" is deliberately excluded: it arrives as
	// dozens-to-hundreds of small chunks per turn, and the assembled
	// final answer is already persisted in full as the assistant message.
	// "reasoning" chunks are handled separately above/below, batched into
	// one row per burst instead of skipped entirely — unlike "token",
	// they have no other persisted home to fall back to.
	emit := func(eventType string, payload map[string]interface{}) {
		emitMu.Lock()
		defer emitMu.Unlock()

		evt := ServerEvent{Type: eventType, ThreadID: threadID}
		if v, ok := payload["content"].(string); ok {
			evt.Content = v
		}
		if v, ok := payload["tool"].(string); ok {
			evt.Tool = v
		}
		if v, ok := payload["args"].(map[string]interface{}); ok {
			evt.Args = v
		}
		if v, ok := payload["result"].(string); ok {
			evt.Result = v
		}
		if v, ok := payload["provider"].(string); ok {
			evt.Provider = v
		}
		if v, ok := payload["citations"].([]tools.Citation); ok {
			evt.Citations = v
		}
		if v, ok := payload["cards"].([]tools.Card); ok {
			evt.Cards = v
		}
		if eventType == "reasoning" {
			reasoningBuf.WriteString(evt.Content)
		} else {
			flushReasoning()
		}
		send(evt)
		s.logTurnEvent(storageThreadID, turnID, eventType, evt)
	}

	// turnMessage is what the agent actually sees — msg.Content plus the
	// attachment's extracted text, if any (see resolveAttachment). The
	// persisted user message above stays as exactly what the user typed;
	// only the in-flight prompt to the model is augmented, so reopening
	// this thread later shows the original question, not a wall of
	// extracted PDF text glued onto it. Called only now, after emit is
	// defined above — an image attachment's vision-model call streams its
	// own synthetic tool_call/tool_result pair through emit (see
	// resolveAttachment's doc comment), so the frontend has something to
	// show during those few seconds instead of a blank wait before
	// agent.Run even starts.
	turnMessage, attachmentData, attachmentCostUSD, err := resolveAttachment(ctx, cfg, modelCfg, msg, emit)
	if err != nil {
		log.Warn("resolving attachment failed, continuing without it", "err", err)
		s.db.LogEvent(storageThreadID, "warn", "turn", "resolving attachment failed", map[string]interface{}{"err": err.Error()}, turnID)
		turnMessage = msg.Content
		attachmentData = nil
		attachmentCostUSD = 0
	}
	// The file's only ever read once, right above — nothing re-reads it
	// later (see removeAttachmentFile's doc comment), so it can be removed
	// immediately regardless of whether extraction succeeded.
	if msg.AttachmentID != "" {
		removeAttachmentFile(cfg, msg.AttachmentID)
	}

	// Folded into turnMessage (what the model sees), not msg.Content (what
	// gets persisted/shown as this pulse's own question) — same "augment
	// the model-facing text, leave the visible transcript alone" split
	// resolveAttachment's own doc comment describes. Lets a recurring
	// routine actually know what it already told the user instead of
	// restating the same still-true report every single run — the
	// concrete case that motivated this: a weekly "Guild Wars 3 news"
	// routine with no memory of its own last pulse just re-describes
	// whatever's still true from before, forever.
	if msg.PulsarPreviousReport != "" {
		turnMessage += "\n\n---\nFor reference, here is what you reported the last time this routine ran " +
			"(" + msg.PulsarPreviousReportAt + "):\n\n" + msg.PulsarPreviousReport +
			"\n\nDo not repeat anything from that report. Only include something in your new answer if it's " +
			"genuinely new or has meaningfully changed since then. If nothing has changed, say so briefly " +
			"instead of restating the old report."
	}

	// The browser's last-known cached fix (see protocol.go's UserLocation
	// doc comment) takes precedence over the static config.yaml default as
	// the bottom rung of the fallback chain — resolveLiveLocation below,
	// wired in as RequestLocation, sits above both of these and is what a
	// tool call actually gets first crack at (see tools.Context.
	// ResolveLocation): a live fix beats a stale cookie, which beats
	// wherever the operator was when they first set up the potato.
	defaultLocation := cfg.DefaultLocation
	if msg.UserLocation != "" {
		defaultLocation = msg.UserLocation
	}

	// requestLocation asks a specific browser for a fix; resolveLiveLocation
	// is the tool-facing wrapper handed to tools.Context — sync.Once means
	// however many location-hungry tool calls this turn makes (nearby_search
	// and weather both could, concurrently, via dispatchToolCallsConcurrently),
	// the browser only ever gets interrupted for its GPS once. Nil when
	// requestLocation itself is nil (no live client — see ask.go), so
	// ResolveLocation's nil check skips straight to defaultLocation above.
	var resolveLiveLocation func() (string, bool)
	if requestLocation != nil {
		var locationOnce sync.Once
		var liveLocation string
		var liveLocationOK bool
		resolveLiveLocation = func() (string, bool) {
			locationOnce.Do(func() {
				liveLocation, liveLocationOK = requestLocation(ctx, threadID)
			})
			return liveLocation, liveLocationOK
		}
	}

	agentCtx := &tools.Context{
		SearXNG:                s.searxng,
		Blocklist:              s.blocklist,
		Foursquare:             s.foursquare,
		Tavily:                 s.tavily,
		Brave:                  s.brave,
		BraveUsageThisMonth:    func() (int, error) { return s.db.GetAPIUsage("brave") },
		IncrementBraveUsage:    func() error { _, err := s.db.IncrementAPIUsage("brave"); return err },
		Parallel:               s.parallel,
		ParallelUsageThisMonth: func() (int, error) { return s.db.GetAPIUsage("parallel") },
		IncrementParallelUsage: func() error { _, err := s.db.IncrementAPIUsage("parallel"); return err },
		Embed:                  s.embed,
		GitHubToken:            cfg.GitHub.Token,
		LastFMAPIKey:           cfg.LastFM.APIKey,
		HardcoverAPIKey:        cfg.Hardcover.APIKey,
		TMDBAPIKey:             cfg.TMDB.APIKey,
		AttachmentData:         attachmentData,
		AttachmentFilename:     msg.AttachmentFilename,
		DefaultLocation:        defaultLocation,
		RequestLocation:        resolveLiveLocation,
		VoiceMode:              msg.VoiceMode,
		FocusMode:              msg.FocusMode,
		DeepResearch:           msg.DeepResearch,
		NoResearch:             msg.NoResearch,
		QuickMode:              msg.QuickMode,
		DisabledTools:          DisabledToolsFromStore(s.db),
		LLM:                    client,
		Emit:                   emit,
		MaxTurns:               cfg.MaxAgentTurns,
	}
	// Left nil (not wired above) when the operator has turned memory off —
	// see MemoryEnabledFromStore's doc comment for why leaving these nil
	// is what actually makes the memory tool AND the {memories} prompt
	// section disappear, not just a tool call that would fail if attempted.
	if MemoryEnabledFromStore(s.db) {
		agentCtx.ListMemories = s.db.ListMemories
		agentCtx.GetMemory = s.db.GetMemory
		agentCtx.WriteMemory = s.db.CreateMemory
		agentCtx.EditMemory = s.db.UpdateMemory
		agentCtx.ForgetMemory = s.db.DeleteMemory
	}
	// Left nil (not wired above) unless this turn actually has Deep
	// Research on — catalog.go's "deep_research" Requires case already
	// checks ctx.DeepResearch too, so leaving this nil otherwise isn't
	// load-bearing for correctness, just avoids building a worker LLM
	// client (and its provider-routing chain) for the vast majority of
	// turns that will never call it. workerModelCfg (config.
	// ResearchWorkerModel — DeepSeek V4 Flash, see the plan doc's
	// "Sub-agents" section) is deliberately its own client, not a reuse
	// of the orchestrator's own `client` above — same reasoning as
	// generateSuggestions/generateTitle building their own dedicated
	// clients rather than sharing the thread's.
	if msg.DeepResearch {
		if workerModelCfg, ok := cfg.ResearchWorkerModel(); ok {
			workerClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, workerModelCfg.Model, workerModelCfg.Temperature, workerModelCfg.MaxTokens).
				WithProvider(&llm.ProviderRouting{Order: workerModelCfg.Provider, AllowFallbacks: boolPtr(false)}).
				WithSessionID(threadID)
			if rc := workerModelCfg.Reasoning; rc != nil && rc.Enabled {
				workerClient = workerClient.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
			}
			agentCtx.SpawnResearchers = func(subCtx *tools.Context, tasks []tools.SubAgentTask) []tools.SubAgentReport {
				return agent.SpawnResearchers(subCtx.Ctx, subCtx, workerClient, tasks)
			}
		}
	}

	// Timed around agent.Run specifically, not the whole handler — this is
	// "how long it took to get an answer", the number a user watching the
	// tokens stream in actually cares about. Excludes the follow-up
	// suggestions/title-generation calls after it, which run invisibly
	// (the answer's already fully rendered) and would otherwise inflate a
	// short answer's reported time with unrelated background work.
	turnStart := time.Now()
	result, err := agent.Run(ctx, agentCtx, history, turnMessage)
	durationMs := time.Since(turnStart).Milliseconds()
	// Catches a reasoning burst still open when the turn ended — normally
	// the final answer's "token" events already triggered this via emit
	// above, but a turn that errored or was stopped mid-reasoning
	// wouldn't have reached that point.
	flushReasoning()
	if err != nil {
		// agent.Run still returns a non-nil result carrying whatever cost
		// the turn accrued before the failing call — real, billed LLM
		// calls happened even though the turn ultimately errored, so this
		// records that spend in the event log instead of silently losing
		// it (there's no assistant message row to attach it to here, since
		// the turn never produced an answer). Not folded into per-thread
		// cost stats — that's a separate design question about where a
		// failed turn's spend should live in aggregate reporting, not
		// solved by this log line.
		var partialCost float64
		if result != nil {
			partialCost = result.CostUSD
		}
		s.db.LogEvent(storageThreadID, "error", "turn", "turn failed", map[string]interface{}{"err": err.Error(), "model": modelCfg.ID, "cost_usd": partialCost}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, UserMessageID: userMsgID, Message: err.Error()})
		return
	}
	// No error, but no answer either — same reasoning-exhaustion failure
	// mode as generateTitle/generateSuggestions (see generateTitle's doc
	// comment), except here it's the primary answer, not a side call: a
	// reasoning model that spends its whole completion budget on hidden
	// reasoning tokens returns empty visible content with no error at all.
	// Left unchecked, this used to fall straight through to AddMessage
	// below and persist a blank assistant turn — no error shown, no log
	// trace, just a silently empty reply. ctx.Err() == nil excludes a
	// user-initiated stop hit before any token streamed, which is a
	// legitimate empty answer, not this failure mode.
	if ctx.Err() == nil && result.Answer == "" {
		const msg = "The model didn't return an answer — it may have spent its whole response budget on " +
			"internal reasoning. Try again, or switch models."
		log.Warn("turn produced an empty answer", "thread", threadID, "model", modelCfg.ID)
		s.db.LogEvent(storageThreadID, "warn", "turn", "model returned an empty answer", map[string]interface{}{"model": modelCfg.ID}, turnID)
		send(ServerEvent{Type: "error", ThreadID: threadID, UserMessageID: userMsgID, Message: msg})
		return
	}

	// One-time LLM-generated thread title, replacing the truncated
	// placeholder set above — on a brand-new thread's first turn, or on
	// an edit/retry of that first turn (isFirstMessageEdit), since in
	// both cases the title needs to describe a question that's only just
	// been asked. Never regenerated on any other turn — a manual rename,
	// or just leaving the placeholder, both take precedence there. Same
	// completion-gating as suggestions: skip on a stopped generation or
	// an empty answer, where the placeholder is already the more
	// sensible title anyway.
	if (isNewThread || isFirstMessageEdit) && ctx.Err() == nil && result.Answer != "" {
		if msg.PulsarRoutineID != 0 {
			// Deterministic, not an LLM call — see the plan doc's "Pulse
			// execution model": every pulse from the same routine starts
			// from a near-identical prompt, so the normal LLM-generated
			// title (which reads the opening question) would produce a
			// wall of near-duplicate titles down a routine's pulse
			// history. Routine name + the run's own date is exactly
			// enough to differentiate them (the plan doc's own example:
			// "Daily news — Sept 4") without generateTitle's failure
			// modes (an empty completion leaving the placeholder
			// forever) or its cost.
			title := msg.PulsarRoutineName + " — " + time.Now().Format("Jan 2")
			if len(title) > maxThreadTitleLen {
				title = title[:maxThreadTitleLen]
			}
			if err := s.db.SetThreadTitle(threadID, title); err != nil {
				log.Warn("failed to persist pulse title", "thread", threadID, "err", err)
				s.db.LogEvent(storageThreadID, "warn", "title", "persisting pulse title failed", map[string]interface{}{"err": err.Error()}, turnID)
			}
		} else if title, titleCost, err := s.generateTitle(cfg, modelCfg, msg.Content); err != nil {
			log.Warn("thread title generation failed", "thread", threadID, "err", err)
			s.db.LogEvent(storageThreadID, "warn", "title", "thread title generation failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else if title != "" {
			if err := s.db.SetThreadTitle(threadID, title); err != nil {
				log.Warn("failed to persist generated thread title", "err", err)
				s.db.LogEvent(storageThreadID, "warn", "title", "persisting generated title failed", map[string]interface{}{"err": err.Error()}, turnID)
			} else {
				result.CostUSD += titleCost
				s.db.LogEvent(storageThreadID, "info", "title", "thread title generated", map[string]interface{}{"title": title, "cost_usd": titleCost}, turnID)
			}
		} else {
			// No error, but nothing usable came back — this exact gap is
			// what let a real bug hide completely: generateTitle's client
			// used to cap completions at 60 tokens, and a reasoning model
			// (this app's own default, "deepseek") can spend that whole
			// budget on hidden reasoning tokens before emitting any
			// visible content, leaving resp.Content empty. Neither branch
			// above fired, so the thread silently kept its
			// truncated-question placeholder forever with zero trace in
			// the event log explaining why. Logging this case (and
			// raising generateTitle's token budget) closes both the
			// silence and the likely cause.
			s.db.LogEvent(storageThreadID, "warn", "title", "model returned no usable title", nil, turnID)
		}
	}

	citationsJSON, err := json.Marshal(result.Citations)
	if err != nil {
		log.Warn("failed to marshal citations, persisting message without them", "err", err)
		s.db.LogEvent(storageThreadID, "warn", "turn", "marshaling citations failed", map[string]interface{}{"err": err.Error()}, turnID)
		citationsJSON = []byte("[]")
	}
	// Suggestions start empty and are filled in by a post-hoc UPDATE once
	// generateSuggestions returns — see the call after "done" ships below.
	assistantMsgID, err := s.db.AddMessage(storageThreadID, "assistant", result.Answer, string(citationsJSON), "[]", result.CostUSD, turnID)
	if err != nil {
		// The answer was already fully streamed live via "token" events —
		// the browser has it. But "done" (see protocol.go's doc comment)
		// means "persisted, safe to re-enable input assuming this thread
		// can be reopened with it intact" — sending it here would be a lie:
		// reopening this thread would show the question with no reply, and
		// its cost would never be added to the thread's running total.
		// Surfacing an explicit error instead tells the user their answer
		// exists only in this live view and won't survive a reload.
		log.Warn("failed to persist assistant message", "err", err)
		s.db.LogEvent(storageThreadID, "error", "turn", "persisting assistant message failed", map[string]interface{}{"err": err.Error()}, turnID)
		send(ServerEvent{
			Type:          "error",
			ThreadID:      threadID,
			UserMessageID: userMsgID,
			Message:       "Your answer was generated but couldn't be saved — copy it now if you need it, then try again.",
		})
		return
	}
	// See the matching call above (and TouchUpdatedAt's doc comment) —
	// same gap, same fix, for the assistant reply's own write.
	if err := s.db.TouchUpdatedAt(threadID); err != nil {
		log.Warn("failed to bump thread recency", "thread", threadID, "err", err)
	}

	if err := s.db.SetContextTokens(storageThreadID, result.ContextTokens); err != nil {
		log.Warn("failed to record context tokens", "err", err)
		s.db.LogEvent(storageThreadID, "warn", "turn", "recording context tokens failed", map[string]interface{}{"err": err.Error()}, turnID)
	}

	if err := s.db.SetMessageDuration(assistantMsgID, durationMs); err != nil {
		log.Warn("failed to record message duration", "err", err)
		s.db.LogEvent(storageThreadID, "warn", "turn", "recording message duration failed", map[string]interface{}{"err": err.Error()}, turnID)
	}

	if len(result.Cards) > 0 {
		if cardsJSON, err := json.Marshal(result.Cards); err != nil {
			log.Warn("failed to marshal cards, message persisted without them", "err", err)
			s.db.LogEvent(storageThreadID, "warn", "turn", "marshaling cards failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else if err := s.db.SetMessageCards(assistantMsgID, string(cardsJSON)); err != nil {
			log.Warn("failed to record cards", "err", err)
			s.db.LogEvent(storageThreadID, "warn", "turn", "recording cards failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	if result.PendingQuestion != nil {
		if pendingJSON, err := json.Marshal(result.PendingQuestion); err != nil {
			log.Warn("failed to marshal pending question, message persisted without it", "err", err)
			s.db.LogEvent(storageThreadID, "warn", "turn", "marshaling pending question failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else if err := s.db.SetMessagePendingQuestion(assistantMsgID, string(pendingJSON)); err != nil {
			log.Warn("failed to record pending question", "err", err)
			s.db.LogEvent(storageThreadID, "warn", "turn", "recording pending question failed", map[string]interface{}{"err": err.Error()}, turnID)
		}
	}

	// Auto-compact once this thread crosses the configured threshold: the
	// model summarizes everything covered so far, and future turns build
	// history from that summary instead of the full raw text. The
	// messages table itself is untouched — only what gets sent back to
	// the LLM shrinks, the visible transcript stays the true record.
	contextTokens := result.ContextTokens
	if result.ContextTokens >= cfg.ContextWindowTokens {
		if summary, compactCost, err := s.compactThread(client, storageThreadID, assistantMsgID); err != nil {
			log.Warn("auto-compaction failed", "thread", threadID, "err", err)
			s.db.LogEvent(storageThreadID, "warn", "compaction", "auto-compaction failed", map[string]interface{}{"err": err.Error()}, turnID)
		} else {
			contextTokens = estimateTokens(summary)
			send(ServerEvent{Type: "compacted", ThreadID: threadID, Content: summary})
			s.db.LogEvent(storageThreadID, "info", "compaction", "thread auto-compacted", map[string]interface{}{
				"through_message_id": assistantMsgID,
				"cost_usd":           compactCost,
				"summary":            summary,
			}, turnID)
			result.CostUSD += compactCost
		}
	}

	// Total cost added to the thread this turn: the agent's LLM/tool
	// spend plus any STT cost from a voice memo, plus an image
	// attachment's description call, plus compaction's own cost if it
	// just ran — all persisted above, so the frontend's running total
	// should reflect all of them. Follow-up suggestions are deliberately
	// excluded: that call hasn't run yet (see below), and its cost ships
	// separately in the "suggestions" event once it does.
	totalCost := result.CostUSD + msg.SttCostUSD + attachmentCostUSD
	s.db.LogEvent(storageThreadID, "info", "turn", "turn completed", map[string]interface{}{
		"model":          modelCfg.ID,
		"cost_usd":       totalCost,
		"context_tokens": contextTokens,
		"citations":      len(result.Citations),
		"stopped":        ctx.Err() != nil,
	}, turnID)

	send(ServerEvent{
		Type:            "done",
		ThreadID:        threadID,
		UserMessageID:   userMsgID,
		Citations:       result.Citations,
		Cards:           result.Cards,
		CostUSD:         totalCost,
		ContextTokens:   contextTokens,
		DurationMs:      durationMs,
		PendingQuestion: result.PendingQuestion,
	})

	// Follow-up suggestions, Perplexity-style — generated in a detached
	// goroutine, after "done" already shipped, so the turn footer and
	// cost/duration appear the instant the real answer is ready instead
	// of stalling behind a second, invisible LLM call the user never
	// asked for. Detached, not just moved, because the caller's own
	// goroutine (see ws.go) releases this connection's "turn in flight"
	// guard via defer as soon as handleTurn returns — running this
	// inline would keep the connection locked for this call's duration
	// too, silently rejecting a fast follow-up message sent right after
	// "done". Skipped on a stopped generation (ctx.Err() != nil) since
	// suggesting where to go next from an answer the user just cut off
	// isn't useful. assistantMsgID/storageThreadID are already persisted
	// at this point, so this only ever does a post-hoc UPDATE, never
	// blocks the message existing. Also skipped whenever the answer
	// itself already ends in a question — not just the ask_user_question
	// case above, but any ordinary turn where the model asks its own
	// follow-up in plain prose instead of calling the tool. A row of
	// "questions you could ask next" directly under an assistant message
	// that's itself a question reads as more questions piling up, not
	// answer shortcuts — a real, confusing collision caught live.
	if ctx.Err() == nil && result.Answer != "" && result.PendingQuestion == nil && !answerEndsInQuestion(result.Answer) {
		go func() {
			// Same rationale as ws.go's turn goroutine: this runs outside
			// any call stack net/http recovers, so an unrecovered panic
			// here would take down the whole process instead of just
			// this one enrichment step.
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic generating follow-up suggestions", "thread", threadID, "panic", r)
				}
			}()
			sug, sugCost, err := s.generateSuggestions(cfg, modelCfg, msg.Content, result.Answer)
			if err != nil {
				log.Warn("follow-up suggestions failed", "thread", threadID, "err", err)
				s.db.LogEvent(storageThreadID, "warn", "suggestions", "follow-up suggestions failed", map[string]interface{}{"err": err.Error()}, turnID)
				return
			}
			if len(sug) == 0 {
				// No error, but nothing usable came back either — a
				// reasoning model can spend its whole completion budget
				// on hidden reasoning tokens and never reach visible
				// content (see generateTitle's doc comment for the real
				// case that surfaced this). Otherwise this failure mode
				// is completely silent: no error to log, no suggestions
				// to show, no trace in the event log to explain why.
				s.db.LogEvent(storageThreadID, "warn", "suggestions", "model returned no usable suggestions", nil, turnID)
				return
			}
			suggestionsJSON, _ := json.Marshal(sug)
			if err := s.db.SetMessageSuggestions(assistantMsgID, string(suggestionsJSON)); err != nil {
				log.Warn("failed to persist follow-up suggestions", "err", err)
				s.db.LogEvent(storageThreadID, "warn", "suggestions", "persisting follow-up suggestions failed", map[string]interface{}{"err": err.Error()}, turnID)
				return
			}
			if err := s.db.AddThreadCost(storageThreadID, sugCost); err != nil {
				log.Warn("failed to record follow-up suggestions cost", "err", err)
			}
			send(ServerEvent{
				Type:        "suggestions",
				ThreadID:    threadID,
				CostUSD:     sugCost,
				Suggestions: sug,
			})
		}()
	}
}

// logTurnEvent persists the subset of streamed turn events worth keeping
// as durable evidence — thinking steps and tool calls/results, so "what
// happened during this turn" survives even if the process crashed before
// the turn finished normally. Errors surfaced mid-stream (a tool
// dispatch failure, still wrapped as a normal "tool_result" whose result
// string starts with "error:") are logged at warn instead of info so they
// stand out when scanning a thread's event history.
func (s *Server) logTurnEvent(threadID, turnID, eventType string, evt ServerEvent) {
	switch eventType {
	case "thinking":
		s.db.LogEvent(threadID, "info", "turn", "thinking", map[string]interface{}{"content": evt.Content}, turnID)
	case "commentary":
		s.db.LogEvent(threadID, "info", "turn", "commentary", map[string]interface{}{"content": evt.Content}, turnID)
	case "tool_call":
		s.db.LogEvent(threadID, "info", "tool."+evt.Tool, "tool call started", map[string]interface{}{"args": evt.Args}, turnID)
	case "tool_result":
		level := "info"
		if strings.HasPrefix(evt.Result, "error:") {
			level = "warn"
		}
		s.db.LogEvent(threadID, level, "tool."+evt.Tool, "tool call finished", map[string]interface{}{"result": evt.Result, "citations": evt.Citations, "provider": evt.Provider}, turnID)
	case "agent_nudge":
		// Durable record of a research-steering signal firing (see
		// agent.emitNudge) — evt.Args carries kind/call_count/
		// citation_count. store.Store.GetStats reads these back to report
		// how often each signal actually fires against real usage.
		s.db.LogEvent(threadID, "info", "agent.nudge", "research steering nudge fired", evt.Args, turnID)
	}
}

// suggestionListPrefix strips list-style prefixes ("1. ", "- ", "• ") the
// model sometimes adds despite being told not to — deliberately narrow
// (requires the punctuation/space right after digits) so it never eats a
// genuine leading number in a question, e.g. "2024 election results?".
var suggestionListPrefix = regexp.MustCompile(`^(?:[-*•]\s+|\d+[.)]\s+)`)

// answerEndsInQuestion reports whether an assistant answer's last
// non-blank line ends in a question mark — the model asking its own
// follow-up in plain prose rather than via ask_user_question. Checked
// against handleTurn's suggestions gate, not just the tool-call case:
// a "what to ask next" chip row directly under a message that's itself
// a question reads as more questions piling up, not answer shortcuts.
// Trailing markdown/whitespace (a closing bold marker, a stray newline)
// is trimmed first so it doesn't mask the real last character.
func answerEndsInQuestion(answer string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(answer), "*_ \t\n")
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？")
}

// maxSuggestionLen caps how long a parsed line can be and still count as
// a follow-up question — a real one reads like "Which company has built
// the most transformer models so far?" (well under this), not a paragraph.
// Belt-and-suspenders against ever showing a runaway response as a chip
// again: the tight MaxTokens below should already prevent it, but this
// means a formatting slip can't leak a full answer into the UI either.
const maxSuggestionLen = 140

// generateSuggestions asks for up to 3 short follow-up questions based on
// the exchange that just finished — one extra cheap, non-streamed LLM
// call, same pattern as compactThread below. Only the last exchange is
// given as context (not the full thread history): follow-ups are about
// "where could this conversation go next", not a function of everything
// said earlier.
//
// Deliberately builds its own client from modelCfg rather than reusing the
// thread's tool-capable client — a fully separate call with no tools
// offered and a tight token cap, so it can never wander into producing a
// real answer instead of short questions. Still pins the provider the
// same way the main client does: leaving that off routes to whatever
// OpenRouter picks by default, which can land on a degraded/no-reasoning
// endpoint for the same model slug and come back with near-empty content.
func (s *Server) generateSuggestions(cfg *config.Config, modelCfg config.ModelConfig, userMessage, answer string) ([]string, float64, error) {
	// 500, not a tighter cap — a reasoning model (this app's own default,
	// "deepseek") spends part of any completion budget on hidden
	// reasoning tokens before it ever emits visible content. A cap too
	// close to what a normal short answer needs risks the reasoning
	// alone consuming it, leaving resp.Content empty — silently, with no
	// error to catch. See generateTitle's doc comment for the concrete
	// case this exact failure mode caused.
	sugClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 500).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		// Explicitly off, not just omitted — see ReasoningParams.Enabled's
		// doc comment. Leaving the reasoning field off entirely still lets
		// a reasoning-native model reason internally by default, spending
		// part of this 500-token budget on invisible thinking before any
		// suggestion text. This is a 3-question list, not a task that
		// benefits from it.
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	// The task instruction lives in the LAST message, as a fresh "user"
	// turn after the exchange — not just in the system prompt above it.
	// Ending the array on the assistant's full answer (as this used to)
	// invites a weaker model to keep going: it reads as "continue this
	// turn", so the model just extends/qualifies the answer instead of
	// switching to the actual task. A real example hit this exactly —
	// asked about the largest dense LLM, answered PaLM, and the single
	// "suggestion" that came back was "The model was never open-sourced,
	// but the architecture and results were published." — a continuation
	// sentence, not a question. Putting the instruction last, with an
	// explicit anti-continuation line and a worked example, reliably
	// signals a context switch instead.
	p := prompts.Get()
	prompt := []llm.ChatMessage{
		{Role: "system", Content: p.Turn.SuggestionsSystem},
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: answer},
		{Role: "user", Content: p.Turn.SuggestionsTask},
	}

	resp, err := sugClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return nil, 0, err
	}

	var suggestions []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = suggestionListPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		line = strings.Trim(line, "\"")
		// Requiring a trailing question mark is a belt-and-suspenders
		// filter against exactly the continuation-sentence failure mode
		// above: even if a model slips past the prompt's instructions, a
		// non-question line gets dropped silently rather than shown as a
		// "suggestion" that's actually just more of the answer. Showing
		// 0 suggestions is far less confusing than showing a wrong one.
		if line == "" || len(line) > maxSuggestionLen {
			continue
		}
		if !strings.HasSuffix(line, "?") && !strings.HasSuffix(line, "？") {
			continue
		}
		suggestions = append(suggestions, line)
		if len(suggestions) == 3 {
			break
		}
	}
	return suggestions, resp.CostUSD, nil
}

// titleQuotePrefix strips a leading/trailing quote mark the model
// sometimes wraps the title in despite being told not to — trimmed
// separately from strings.Trim below since that would also eat a quote
// that's genuinely part of the title (e.g. a title ending in "quotes").
var titleQuotePrefix = regexp.MustCompile(`^["'“‘]+|["'”’]+$`)

// answerLikeTitle catches a title that's actually an answer to the
// user's question instead of a description of it — a real example:
// asked "Who did Vincent Pastore play in the Sopranos? Was it Paulie?"
// and got back "No, Vincent Pastore played Salvatore..." as the title.
// Unlike the earlier answer-in-context bug (see generateTitle's doc
// comment), this happens with only the question in context: a yes/no or
// "was it X" question is enough to pull a helpful model into answering
// it instead of titling it, no matter how the system prompt words the
// instruction not to. Cheaper to catch the handful of tells this
// produces (a leading "Yes"/"No"/"Unfortunately"/etc.) than to rely on
// prompting alone — matched titles are treated as unusable, same as an
// empty one, and the placeholder stands instead.
var answerLikeTitle = regexp.MustCompile(`(?i)^(yes|no|sure|actually|unfortunately|correct|indeed|according to)\b[\s,.:!—-]`)

// maxTitleLen caps the generated title's length — a good one reads like
// "Capital of France" or "Debugging a Go goroutine leak" (well under
// this), not a restated question. Same belt-and-suspenders reasoning as
// maxSuggestionLen: the tight MaxTokens below should already prevent a
// runaway response, this just means a formatting slip can't leak one
// into the sidebar as a "title" anyway — falls back to the
// truncated-first-message placeholder in that case instead.
const maxTitleLen = 60

// generateTitle asks for a short thread title based on the user's
// question alone — one extra cheap, non-streamed LLM call, same pattern
// as generateSuggestions/compactThread. Only called once, right after a
// brand-new thread's first turn (see handleTurn's isNewThread gate).
//
// Deliberately does NOT include the answer, unlike an earlier version of
// this function — with the answer in context, the message array ends on
// a full assistant turn, and a weaker model reliably reads that as "keep
// going" rather than "switch to a new task", producing a title that's
// actually just a continuation of the answer (a real example: title came
// back as "Unfortunately, it was never open-sourced, so while
// available..." — a sentence fragment from the answer, not a title). A
// search-app question is almost always self-descriptive enough on its
// own ("what is the largest dense llm released?"), so dropping the
// answer sidesteps the whole failure mode instead of working around it.
//
// 300, not a tighter cap — this used to be 60, and a real production
// thread never got a generated title at all: the default model
// ("deepseek") is a reasoning model, and reasoning tokens count against
// the same completion budget as visible content. 60 tokens was
// consumed entirely by hidden reasoning, resp.Content came back empty,
// and — since that's not an error — the code below silently did
// nothing, leaving the truncated-question placeholder forever with no
// trace in the event log explaining why (see handleTurn's title-gating
// block, which now logs this case explicitly too).
//
// Even with the answer dropped from context, a yes/no or "was it X"
// question is enough to pull a helpful model into answering it instead
// of titling it (real examples: "Who did Vincent Pastore play in the
// Sopranos? Was it Paulie?" came back as "No, Vincent Pastore played
// Salvatore..."). The system prompt now says so explicitly with
// matching examples, and answerLikeTitle below catches whatever slips
// through anyway.
func (s *Server) generateTitle(cfg *config.Config, modelCfg config.ModelConfig, userMessage string) (string, float64, error) {
	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 300).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		// Explicitly off, not just omitted — see ReasoningParams.Enabled's
		// doc comment. Raising this call's budget from 60 to 300 tokens
		// (see below) helped but didn't fully fix the silent-empty-title
		// failure this comment used to describe alone: a real thread asking
		// a two-part question ("news in Smyrna GA today? And the weather?")
		// still burned the entire 300-token budget on hidden reasoning with
		// the field left unset, and came back with zero visible content.
		// Explicitly disabling reasoning removes the non-determinism
		// instead of just raising the budget and hoping it's enough.
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	prompt := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Turn.TitleSystem},
		{Role: "user", Content: userMessage},
	}

	resp, err := titleClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	return sanitizeGeneratedTitle(resp.Content), resp.CostUSD, nil
}

// sanitizeGeneratedTitle cleans up a raw title completion into something
// safe to store — strips a wrapping quote and trailing punctuation, caps
// the length, and rejects anything that reads like an answer rather than
// a title (see answerLikeTitle). Shared by generateTitle and
// regenerateTitle so both apply the exact same rules to whatever the
// model sends back.
func sanitizeGeneratedTitle(raw string) string {
	title := strings.TrimSpace(raw)
	title = strings.TrimSpace(titleQuotePrefix.ReplaceAllString(title, ""))
	title = strings.TrimRight(title, ".!。")
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen]
	}
	if answerLikeTitle.MatchString(title) {
		return ""
	}
	return title
}

// regenerateTitle is generateTitle's whole-thread counterpart: instead of
// titling just the opening question, it reads the full conversation
// (history, exactly as loadHistory reconstructs it — a compacted summary
// included, same as a normal turn would see) and titles that as a whole.
// Used by the "Regenerate title" menu action, not by the automatic
// once-per-thread path in handleTurn.
//
// The task instruction goes in a trailing user message after history,
// exactly like generateSuggestions — see that function's doc comment for
// why: ending the prompt array on the thread's last assistant reply
// (which the raw history always does) invites a helpful model to keep
// answering instead of switching to the actual task, so the instruction
// needs to be the very last thing it sees.
func (s *Server) regenerateTitle(cfg *config.Config, modelCfg config.ModelConfig, history []llm.ChatMessage) (string, float64, error) {
	if len(history) == 0 {
		return "", 0, fmt.Errorf("thread has no messages to title")
	}

	titleClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, 300).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	p := prompts.Get()
	prompt := make([]llm.ChatMessage, 0, len(history)+2)
	prompt = append(prompt, llm.ChatMessage{Role: "system", Content: p.Turn.TitleRegenerateSystem})
	prompt = append(prompt, history...)
	prompt = append(prompt, llm.ChatMessage{Role: "user", Content: p.Turn.TitleRegenerateTask})

	resp, err := titleClient.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	return sanitizeGeneratedTitle(resp.Content), resp.CostUSD, nil
}

// compactThread summarizes every message up to and including throughID,
// via one extra (non-streamed, not shown as a normal answer) LLM call,
// and records that summary so loadHistory substitutes it for the raw
// messages it covers on every subsequent turn.
func (s *Server) compactThread(client llm.ChatClient, threadID string, throughID int64) (summary string, cost float64, err error) {
	history, err := s.loadHistory(threadID, 0)
	if err != nil {
		return "", 0, err
	}
	prompt := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Turn.CompactionSystem},
	}
	prompt = append(prompt, history...)

	resp, err := client.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(resp.Content) == "" {
		// No error, but nothing usable came back — same reasoning-exhaustion
		// failure mode as generateTitle/generateSuggestions (see
		// generateTitle's doc comment), except unchecked here it would be
		// far worse: CompactThread below replaces the thread's ENTIRE prior
		// history with this string and marks it as covering everything
		// through throughID. An empty summary would silently and
		// permanently erase that history instead of just showing a blank
		// title/suggestion list. Treating it as an error routes through the
		// caller's existing "auto-compaction failed" log + skip path, which
		// already correctly leaves the thread's real messages untouched.
		return "", resp.CostUSD, fmt.Errorf("compaction returned no usable summary")
	}

	if err := s.db.CompactThread(threadID, resp.Content, throughID, resp.CostUSD, estimateTokens(resp.Content)); err != nil {
		return "", 0, err
	}
	return resp.Content, resp.CostUSD, nil
}

// estimateTokens is a rough tokens-per-character heuristic (English text
// averages ~4 chars/token) used only to seed context_tokens right after a
// compaction, before the next real LLM call reports an actual count.
func estimateTokens(s string) int {
	return len(s) / 4
}

// loadHistory reconstructs prior turns as ChatMessage pairs so a
// resumed/continued thread has full context. If the thread has been
// auto-compacted, everything at or below compacted_through_id is replaced
// by a single summary message instead of being sent in full.
//
// excludeFromID, if nonzero, additionally skips every message with id >=
// excludeFromID — the retry/edit path's way of getting a "post-edit" view
// of history without the old messages having actually been deleted yet
// (see handleTurn: the physical delete is deferred to a single atomic
// transaction with the new message's insert, but the LLM must still see
// history as if the edit had already happened).
func (s *Server) loadHistory(threadID string, excludeFromID int64) ([]llm.ChatMessage, error) {
	// GetThreadRaw, not the public GetThread — threadID here is always
	// storageThreadID, which is legitimately a hidden fork's own id for
	// an edit/retry turn, and the public GetThread now deliberately
	// rejects those (see its doc comment).
	thread, err := s.db.GetThreadRaw(threadID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.db.GetMessages(threadID)
	if err != nil {
		return nil, err
	}

	history := make([]llm.ChatMessage, 0, len(msgs)+1)
	if thread.CompactedSummary != "" {
		history = append(history, llm.ChatMessage{
			Role: "assistant",
			Content: "(Summary of earlier conversation, compacted to save context — the full history " +
				"is no longer available, only this summary)\n\n" + thread.CompactedSummary,
		})
	}
	for _, m := range msgs {
		if m.ID <= thread.CompactedThroughID {
			continue // covered by the summary above
		}
		if excludeFromID != 0 && m.ID >= excludeFromID {
			continue
		}
		history = append(history, llm.ChatMessage{Role: m.Role, Content: m.Content})
	}
	return history, nil
}
