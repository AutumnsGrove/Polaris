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
- [ ] 5. `store/constellation.go`: stars, reviews, stats, digest
- [ ] 6. `prompts/prompts.go`: split `buildDefaults()` per section (drift test must stay green)
- [ ] 7. `Transponder.svelte`: controller `.svelte.ts` + sub-components
- [ ] 8. `ChatTurnView.svelte`: extract reasoning / sources / suggestions components
- [ ] 9. `ChatView.svelte`
- [ ] 10. `routes/search/+page.svelte`: result card, pagination, filters
- [ ] 11. Test files follow their source split (`store_test.go`, etc.)
