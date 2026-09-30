# File splits: breaking up monolithic files

**Status:** in progress (started 2026-09-29). Living checklist; one commit per stage.

Goal: dedicated files per concern so code can be reused and read in isolation. Go splits stay
within their package (no API or import changes). Pure moves first, behavior changes never.
Pulsar Daily (`gateway/pulsar_daily.go`) is deliberately out of scope.

Rules for every stage:
- Pure code motion. No renames, no signature changes, no logic edits.
- `go build ./... && go vet ./... && go test ./...` (or `pnpm run check && pnpm test` for web) green before commit.
- `store.go`'s `migrations` slice is append-only and positional; move it as one untouched block.

## Checklist

- [x] 1. `store/store.go` (3,197 lines → 188): schema, migrations, threads, variants, history, thread_pages, message_search, search_history, compaction, settings, messages, message_setters, usage. Schema/migrations verified byte-identical to HEAD.
- [x] 2. `tools/registry.go` (1,339 lines → 107): context, evidence, citations, cards, image_candidates, chart, http; `WizardFinal`/`DailyItemsFinal`/`PendingQuestion`/show-state moved next to their owning tool files
- [x] 3. `gateway/turn.go` (1,979 lines → 181): `handleTurn` is now a ~40-line orchestrator over a `turnRun` struct; phases live in turn_thread / turn_message / turn_context / turn_oracle / turn_agent / turn_persist / turn_followups, the emit closure became `turnEmitter` (turn_emit), and the LLM helpers moved to turn_title / turn_suggestions / turn_compaction / turn_history. Verified: build/vet/`-race` tests, plus a differential run of old vs new code through the real server + `dev/fakeopenrouter` (messages, threads and event log byte-identical). Not exercised live: the detached auto-compaction trigger (the fake reports no token usage).
- [x] 4. `web/src/lib/state.svelte.ts` (1,900 → 1,441): stateHelpers, threadTurns, ThreadSearchState, ToastState, VersionState extracted behind delegating getters (no component/test changes), plus `turnEvents.ts` (pure `applyStreamingEvent`/`closeOpenReasoning`, with its own tests). What remains in the class (`openThread`, `dispatch`/`send`, the `done`/`error`/`compacted` cases) all read and write the same pending-turn fields (`pendingTurn`, `pendingThreadId`, `pendingAbandoned`, ...), so splitting further would mean widening those private fields, not just moving code; left as the one coherent store. Note: live `tool_result` matching (turnEvents.ts) and persisted matching (stateHelpers.ts `buildTimelineFromEvents`) are two near-identical copies; unify if a third appears.
- [x] 5. `store/constellation.go` (1,673 lines → 134): config, stars, star_versions, star_search, star_links, shooting_star_runs, shooting_star_records, constellation_stats, constellation_feed, constellation_eligibility, weaver_threads
- [x] 6. `prompts/prompts.go` (1,401 lines → 46): `buildDefaults()` is now an orchestrator over per-section builders (defaults_agent / turn / tools / weaver / wizard / pulsar / oracle); `Set` types in set.go, cache + `Get`/`fillDefaults` in load.go. `TestDefaults_MatchRealPromptsYAML` (drift test) green.
- [x] 7. `Transponder.svelte` (1,610 → ~1,250): `TransponderOrb` (the three copies of the mic-button gesture wiring + all orb CSS), `TranscriptBubble`, `ThinkingChips`, and pure `transponderHelpers.ts` (with tests). Verified in a real browser: Playwright drove Idle→Listening→Thinking→Speaking against the isolated server (fake mic device, real backend + fake LLM, only `/api/transcribe` and `/api/speak/stream` mocked) and diffed bounding boxes + computed styles + text per phase against the pre-refactor build: identical apart from live mic-level noise. That diff caught a real regression (`.stage > *` no longer matched child-component roots, so `flex-shrink:0` was lost and the orb would squash; fixed with `:global(*)`). The recording/playback state machine (startRecording, chunk queue, speakGeneration guards) was deliberately left in place: it is gesture/timing-sensitive and iOS/real-hardware behavior cannot be verified here.
- [x] 8. `ChatTurnView.svelte` (1,509 → 1,085): AttachmentChips, NetworkErrorBanner, SourcesList, VariantSwitcher, OfferLines (markup + scoped CSS move together); shared `sourceHostname` moved to citations.ts. Verified in a real browser: per-state computed-style/geometry snapshots (mocked persisted thread with sources/variants/offers/attachments + a live network-error banner) identical before vs after, plus interaction checks (attachment link, sources toggle, variant swap request, offers). Note: `.chevron`/`.spin` global rules stay in ChatTurnView because ToolEvent silently depends on them.
- [ ] 9. `ChatView.svelte`
- [ ] 10. `routes/search/+page.svelte`: result card, pagination, filters
- [ ] 11. Test files follow their source split (`store_test.go`, etc.)
