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
- [ ] 2. `tools/registry.go`: context, citations, cards, chart, image candidates; registry keeps dispatch only
- [ ] 3. `gateway/turn.go`: decompose the 1,560-line `handleTurn` (needs a `turnState` struct; live-verify)
- [ ] 4. `web/src/lib/state.svelte.ts`: extract domain modules; `AppState` becomes a thin composition root
- [ ] 5. `store/constellation.go`: stars, reviews, stats, digest
- [ ] 6. `prompts/prompts.go`: split `buildDefaults()` per section (drift test must stay green)
- [ ] 7. `Transponder.svelte`: controller `.svelte.ts` + sub-components
- [ ] 8. `ChatTurnView.svelte`: extract reasoning / sources / suggestions components
- [ ] 9. `ChatView.svelte`
- [ ] 10. `routes/search/+page.svelte`: result card, pagination, filters
- [ ] 11. Test files follow their source split (`store_test.go`, etc.)
