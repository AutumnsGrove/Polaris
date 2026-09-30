package gateway

import (
	"context"
	"strings"
	"sync"

	"polaris/agent"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
)

// buildAgentContext assembles the tools.Context the agent runs with: the
// always-on wiring first, then each optional capability (personalization,
// vision, code execution, memory, Weaver, deep research) only when its gate
// passes — leaving a field nil is what makes the matching tool disappear
// from the model's menu.
func (t *turnRun) buildAgentContext() {
	// The browser's last-known cached fix (see protocol.go's UserLocation
	// doc comment) takes precedence over the static config.yaml default as
	// the bottom rung of the fallback chain — resolveLiveLocation below,
	// wired in as RequestLocation, sits above both of these and is what a
	// tool call actually gets first crack at (see tools.Context.
	// ResolveLocation): a live fix beats a stale cookie, which beats
	// wherever the operator was when they first set up the potato.
	defaultLocation := t.cfg.DefaultLocation
	if t.msg.UserLocation != "" {
		defaultLocation = t.msg.UserLocation
	}

	// requestLocation asks a specific browser for a fix; resolveLiveLocation
	// is the tool-facing wrapper handed to tools.Context — sync.Once means
	// however many location-hungry tool calls this turn makes (nearby_search
	// and weather both could, concurrently, via dispatchToolCallsConcurrently),
	// the browser only ever gets interrupted for its GPS once. Nil when
	// requestLocation itself is nil (no live client — see ask.go), so
	// ResolveLocation's nil check skips straight to defaultLocation above.
	var resolveLiveLocation func() (string, bool)
	if t.requestLocation != nil {
		var locationOnce sync.Once
		var liveLocation string
		var liveLocationOK bool
		resolveLiveLocation = func() (string, bool) {
			locationOnce.Do(func() {
				liveLocation, liveLocationOK = t.requestLocation(t.ctx, t.threadID)
			})
			return liveLocation, liveLocationOK
		}
	}

	t.agentCtx = &tools.Context{
		SearXNG:                t.s.searxng,
		Blocklist:              t.s.blocklist,
		Foursquare:             t.s.foursquare,
		Tavily:                 t.s.tavily,
		TavilyUsageThisMonth:   func() (int, error) { return t.s.db.GetAPIUsage("tavily") },
		IncrementTavilyUsage:   func() error { _, err := t.s.db.IncrementAPIUsage("tavily"); return err },
		Brave:                  t.s.brave,
		BraveUsageThisMonth:    func() (int, error) { return t.s.db.GetAPIUsage("brave") },
		IncrementBraveUsage:    func() error { _, err := t.s.db.IncrementAPIUsage("brave"); return err },
		Parallel:               t.s.parallel,
		ParallelUsageThisMonth: func() (int, error) { return t.s.db.GetAPIUsage("parallel") },
		IncrementParallelUsage: func() error { _, err := t.s.db.IncrementAPIUsage("parallel"); return err },
		Jev:                    t.s.jev,
		JevCostThisMonth:       t.s.db.JevCostThisMonth,
		LogJevCost:             t.s.db.LogJevCost,
		Embed:                  t.s.embed,
		GitHubToken:            t.cfg.GitHub.Token,
		LastFMAPIKey:           t.cfg.LastFM.APIKey,
		HardcoverAPIKey:        t.cfg.Hardcover.APIKey,
		TMDBAPIKey:             t.cfg.TMDB.APIKey,
		DefaultLocation:        defaultLocation,
		RequestLocation:        resolveLiveLocation,
		VoiceMode:              t.msg.VoiceMode,
		FocusMode:              t.msg.FocusMode,
		DeepResearch:           t.msg.DeepResearch,
		NoResearch:             t.msg.NoResearch,
		QuickMode:              t.msg.QuickMode,
		DisabledTools:          DisabledToolsFromStore(t.s.db),
		// CustomInstructions is left unset below while this thread is
		// still ghost (issue #67's "no personalization") rather than
		// wired here — same "leave the field nil/empty" convention
		// applyCustomInstructionsPlaceholder already relies on for the
		// benchmark harness's isolated Context, which collapses
		// {custom_instructions} to nothing rather than needing its own
		// flag.
		LLM: t.client,
		// SearchThreads/ListRecentThreads/ReadThread are left unset below
		// while this thread is still ghost, same as CustomInstructions
		// above — they back the chat_search tool (tools/catalog.go's
		// "chat_search" case keys off SearchThreads != nil, same
		// nil-check convention as memory), which reads real, persisted
		// past conversations. Left wired unconditionally here, a ghost
		// turn could (and, live-tested, did) answer "tell me about
		// myself" by searching prior real threads instead of using
		// memory — a second personalization channel the issue's "no
		// personalization" requirement didn't name explicitly but
		// clearly meant to cover. ReadThread's own ghost check (see
		// store.go) is belt-and-suspenders against this same id also
		// being this turn's own still-ghost thread.
		Emit:       t.emitter.emit,
		MaxTurns:   t.cfg.MaxAgentTurns,
		Multimodal: t.modelCfg.Multimodal,
		// threadID, not storageThreadID: code_exec/fetch_url/view_image
		// address the persistent workspace directory by this id (see
		// tools.Context.ThreadID's doc comment), and that directory is one
		// shared resource for the whole conversation, not per fork/variant
		// — a retry/edit's own fresh storageThreadID would point tool
		// calls at a directory nothing was ever written to, making every
		// previously-uploaded file (or code_exec/fetch_url output)
		// invisible to the model from that turn on. A real bug found live
		// testing issue #71's multi-attachment support: retrying a message
		// that had a file attached left the model unable to find it at all.
		ThreadID: t.threadID,
		FieldID:  t.fieldID,
	}
	t.wirePersonalization()
	t.wireVision()
	t.wireCodeExec()
	t.wireMemory()
	t.wireWeaver()
	t.wireResearchers()
}

// wirePersonalization attaches the persisted-store reads (custom
// instructions, chat search, Constellation stars) — all withheld while the
// thread is still ghost.
func (t *turnRun) wirePersonalization() {
	if !t.ghost {
		t.agentCtx.CustomInstructions = joinCustomInstructions(
			CustomInstructionsFromStore(t.s.db),
			fieldPromptBlock(t.field, listFieldFiles(t.cfg.CodeExec.WorkspaceDir, t.fieldID)),
		)
		t.agentCtx.PersonName = PersonNameFromStore(t.s.db)
		t.agentCtx.PersonPronouns = PersonPronounsFromStore(t.s.db)
		t.agentCtx.SearchThreads = t.s.db.SearchMessages
		t.agentCtx.ListRecentThreads = t.s.db.ListThreadsPage
		t.agentCtx.ReadThread = t.s.db.ReadThread
		// stars (issue #56) — the main assistant's own read-only search
		// over Constellation's library. Nil while this thread is still
		// ghost, same "no persisted-store reads leaking into an incognito
		// session" reasoning as SearchThreads/WriteMemory above. StarsRead wraps
		// GetStar (which Weaver's own read_star deliberately lets see
		// rejected/disabled stars) with the same eligibility filter
		// SearchLibraryStars already applies, so a guessed/stale star_id
		// can't surface something the person rejected or hid via the
		// Library UI's Disable action.
		t.agentCtx.StarsSearch = t.s.db.SearchLibraryStars
		t.agentCtx.StarsRead = func(starID int64) (*store.Star, error) {
			star, err := t.s.db.GetStar(starID)
			if err != nil {
				return nil, err
			}
			if star.Disabled || (star.Status != "auto" && star.Status != "confirmed") {
				return nil, store.ErrStarNotFound
			}
			return star, nil
		}
	}
}

// wireVision attaches the image-description helper when any multimodal model
// is available.
func (t *turnRun) wireVision() {
	// visionClient mirrors resolveAttachment's own model-selection logic
	// (this thread's model if multimodal, else cfg.MultimodalModel()'s
	// fallback) — nil only when neither exists, matching every other
	// optional dependency on tools.Context (Brave/Parallel/Tavily) that's
	// left nil rather than wired when unavailable.
	if visionCl, ok := visionClient(t.cfg, t.modelCfg); ok {
		t.agentCtx.DescribeImage = func(imgCtx context.Context, imageBase64, mimeType, instructions string) (string, float64, error) {
			return visionCl.DescribeImage(imgCtx, imageBase64, mimeType, instructions)
		}
	}
}

// wireCodeExec turns on the code_exec sandbox when it is actually configured.
func (t *turnRun) wireCodeExec() {
	// Left false/empty (not wired above) unless the sandbox is actually
	// configured — a pure capability check, not a deployment-mode check.
	// Polaris itself doesn't need to run inside a container for code_exec
	// to work; only the host-side watcher (compose/watcher/codeexec.sh)
	// and a reachable Docker daemon do, which a bare-metal dev instance
	// can have too. See config.Config.CodeExec's doc comment on why
	// HostWorkspaceDir has no safe default. An install that hasn't set
	// host_workspace_dir/signal_dir yet gets CodeExecEnabled=false, which
	// catalog.go's "docker_only" Requires case turns into code_exec
	// simply not being offered.
	if t.cfg.CodeExec.HostWorkspaceDir != "" && t.cfg.CodeExec.SignalDir != "" {
		t.agentCtx.CodeExecEnabled = true
		t.agentCtx.CodeExecWorkspaceDir = t.cfg.CodeExec.WorkspaceDir
		t.agentCtx.CodeExecHostWorkspaceDir = t.cfg.CodeExec.HostWorkspaceDir
		t.agentCtx.CodeExecSignalDir = t.cfg.CodeExec.SignalDir
		t.agentCtx.CodeExecMemoryLimitMB = t.cfg.CodeExec.MemoryLimitMB
		t.agentCtx.CodeExecPidsLimit = t.cfg.CodeExec.PidsLimit
		t.agentCtx.CodeExecTimeoutSeconds = t.cfg.CodeExec.TimeoutSeconds
		t.agentCtx.UITheme = ThemeFromStore(t.s.db)
	}
}

// wireMemory attaches the memory tool's closures unless memory is off, the
// thread is ghost, or the Field opted out.
func (t *turnRun) wireMemory() {
	// Left nil (not wired below) when the operator has turned memory off, OR
	// while this thread is still tagged ghost (issue #67's "no memory
	// tool") — see MemoryEnabledFromStore's doc comment for why leaving
	// these nil is what actually makes the memory tool AND the {memories}
	// prompt section disappear, not just a tool call that would fail if
	// attempted.
	// A field's memory_mode picks which store(s) the closures bind to, with
	// "none" (and ghost) leaving them unwired — the same mechanism ghost
	// threads already use for "no memory tool", so there's no new plumbing in
	// agent/ or tools/. resolveMemoryAccess also applies the global Memory
	// switch (field_memory.go, issue #133).
	if t.ghost {
		return
	}
	access := resolveMemoryAccess(t.field, MemoryEnabledFromStore(t.s.db))
	if access == memoryNone {
		return
	}
	c := newMemoryClosures(t.s.db, t.field, access)
	t.agentCtx.ListMemories = c.list
	t.agentCtx.GetMemory = c.get
	t.agentCtx.WriteMemory = c.write
	t.agentCtx.EditMemory = c.edit
	t.agentCtx.ForgetMemory = c.forget
}

// wireWeaver switches a Weaver thread onto Weaver's own agent loop and tools.
func (t *turnRun) wireWeaver() {
	// Issue #94: a Weaver thread runs Weaver's own agent loop instead of
	// the main assistant's — WeaverRun is the exact same flag
	// newWeaverToolContext sets for a scheduled shooting star
	// (gateway/constellation_weaver.go), which is what makes
	// tools/catalog.go collapse the menu down to Weaver's five tools +
	// search_chats and makes agent.Run's loadSystemPrompt swap in
	// weaver.system instead of prompt.md. weaverToolClosures is the same
	// shared implementation the shooting-star path uses, just without that
	// path's runID-keyed shooting_star_events/candidates admin trail —
	// this turn already gets full observability the ordinary way (every
	// tool_call/tool_result logged to the events table by handleTurn's own
	// emit/logTurnEvent below, source-agnostic). SearchThreads/
	// ListRecentThreads/ReadThread are already wired above for a
	// non-ghost turn, which is what keeps search_chats available here
	// exactly like it is for a shooting star.
	if t.isWeaverThread {
		categories, err := t.s.db.DistinctCategories()
		if err != nil {
			categories = nil
		}
		search, read, create, update, link := weaverToolClosures(t.s.db, t.threadID)
		t.agentCtx.WeaverRun = true
		t.agentCtx.WeaverInteractive = true
		t.agentCtx.MaxTurns = weaverMaxTurns
		t.agentCtx.WeaverCategoriesInUse = strings.Join(categories, ", ")
		t.agentCtx.WeaverPersonName = PersonNameFromStore(t.s.db)
		t.agentCtx.WeaverPersonPronouns = PersonPronounsFromStore(t.s.db)
		t.agentCtx.WeaverSearchStars = search
		t.agentCtx.WeaverReadStar = read
		t.agentCtx.WeaverCreateStar = create
		t.agentCtx.WeaverUpdateStar = update
		t.agentCtx.WeaverLinkStars = link
	}
}

// wireResearchers gives a Deep Research turn its sub-agent worker client.
func (t *turnRun) wireResearchers() {
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
	if t.msg.DeepResearch {
		if workerModelCfg, ok := t.cfg.ResearchWorkerModel(); ok {
			workerClient := llm.NewClient(t.cfg.OpenRouter.BaseURL, t.cfg.OpenRouter.APIKey, workerModelCfg.Model, workerModelCfg.Temperature, workerModelCfg.MaxTokens).
				// AllowFallbacks(true) — see the main client's construction
				// above for why: an escape valve for every pinned provider
				// being down at once, not a relaxation of the normal curated
				// preference order.
				WithProvider(&llm.ProviderRouting{Order: workerModelCfg.Provider, AllowFallbacks: boolPtr(true)}).
				WithSessionID(t.threadID)
			if rc := workerModelCfg.Reasoning; rc != nil && rc.Enabled {
				workerClient = workerClient.WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(true), Effort: rc.Effort, MaxTokens: rc.MaxTokens})
			}
			t.agentCtx.SpawnResearchers = func(subCtx *tools.Context, tasks []tools.SubAgentTask) []tools.SubAgentReport {
				return agent.SpawnResearchers(subCtx.Ctx, subCtx, workerClient, tasks)
			}
		}
	}
}
