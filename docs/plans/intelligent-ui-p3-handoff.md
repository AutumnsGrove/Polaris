# Prism (Intelligent UI): handoff for P3

Written 2026-10-09 to start a fresh session. P0, P1 and P2 are built; **P3 (the remaining blocks) is next.**

## Read first, in this order
1. `CLAUDE.md` (repo root): Go + SvelteKit, live-verify before calling anything done, Edit/Write not sed/python.
2. `docs/plans/intelligent-ui.md`: the spec. Read the Status line, "Block catalog" (every block's fields and
   caps), "Phases" (P3 groups a/b/c), "Sourcing and verification" (needed for group c), and "Spike results".
3. `DEVELOPMENT.md`, section "Answer rendering: segments, `ui` blocks, streaming mermaid".
4. Memory `project_intelligent_ui_prism.md` (state, open items).

## State
- 13 local commits on `main` from `43561ca`, **not pushed**. The operator wants a big code review first, so
  do not push or open a PR. Commit at each stage.
- Issue #159. The user-facing name is **Prism** (setting key / Go identifiers stay `visuals`).
- Built: `ui` fence parser + callout/stat/compare/steps, Prism dial (Off/Low/Normal), streaming mermaid,
  flatteners (TS + Go), Oracle `ui` check (options `none`/`compare`/`steps`), "Rerun as plain text".

## P3 groups (from the plan)
- **(a)** timeline, checklist, procon, choose, facts. **(b)** flow, tabs, disclose. **(c)** claim, quote, plus
  the verification wiring. Do them in groups, one commit per stage, and re-run the spike after each group.

## Adding one block touches all of these (use compare/steps as the template)
1. `web/src/lib/uiBlocks/types.ts` + `parse.ts` (container line in `openContainer`, child lines in
   `addChild`, caps). Add a golden fence to `parse.test.ts`; the every-prefix property test must pass.
2. A component in `web/src/lib/components/ui/` + a branch in `UiBlocks.svelte`. Interactive state (ticks,
   active tab, expanded node) must survive streaming: blocks are keyed by index, so keep it local.
3. Flatteners: `web/src/lib/uiBlocks/flatten.ts` **and** `gateway/uiblocks/uiblocks.go` (it re-implements the
   parser; keep them identical), plus hand-written cases in `testdata/ui_flatten.json` (both suites read it).
4. Base prompt: `prompts.yaml` `ui.base` and `prompts/ui.go` (a drift test enforces they match). Extend
   `TestUIBase_ExamplesAreValidJSONAndCoverEveryBlock`.
5. Oracle: add the option + a nudge with an exemplar line to `prompts.yaml` `oracle.checks.ui` and
   `prompts/defaults_oracle.go`; add rows to `docs/oracle.md` (a test checks it); add the label to
   `web/src/lib/oracleLabels.ts` (`OPTION_LABELS.ui`, `UI_BLOCK_LABELS`).
6. Spike: add corpus lines (positives and negatives for the new shape) to `dev/ui_spike/corpus.json` and run
   `go run ./dev/ui_spike` (about $0.02). **False-positive rate is the number that matters.** Add `-real` with
   first user messages pulled read-only from `polaris.db`; keep those out of the repo. If the pick is
   poorly calibrated at ~11 options, the plan's fallback is a two-question split (`ui` gate + `ui_block`).
7. Docs: `docs/FEATURES.md` if user-visible. The glossary entry is "Prism" and already exists.

## Group (c) caveat: client and server must change together
Today neither side counts `ui` links for verification ticks: `claimsForVerification` in
`gateway/verification.go` uses `uiblocks.Strip`, and `UiText.svelte` passes no verification marks. Turning
tick support on means switching Strip to Flatten, threading the occurrence counter through `UiText`, and
emitting links in schema order on both sides. Doing only one side puts ticks on the wrong chips. `quote`
verification is exact-match first, Jev on a miss (plan decision 18).

## Gotchas learned
- **Who is taught blocks** is `ClientMessage.OffersVisuals` (ws, `/api/ask`, Pulsar pulses yes; voice,
  Atlas Quick Answer, Pulsar Daily no). `gateway/visuals_test.go` pins it.
- **Svelte scoped CSS adds no specificity** for element selectors (`table:where(.svelte-x)`), so
  `.prose :global(table)` beats a bare `table`. Prefix with two real classes (see `UiCompare.svelte`).
- Compare picks cards vs table by a CSS media query, not JS.
- Oracle's `emotional` check holds back the nudge but not the base grammar; the operator hasn't decided.
- `search_chats` indexing still sees raw block JSON (needs an FTS migration; deferred, undecided).

## Live-testing recipe
- `dev/stack.sh restart` runs the bare-metal stack on the real OpenRouter (Oracle is on in the dev DB).
  Frontend changes hot-reload; Go changes need the restart.
- Deterministic streaming: run `go run ./dev/fakeopenrouter -addr 127.0.0.1:18901 -chunk-delay 60ms` with the
  stack's own fake LLM stopped, queue a response with `"match":"Today's date"` (otherwise the title call eats
  it), and sample the DOM with Playwright (`web/node_modules/.pnpm/playwright-core@*/`).
- Real-model behaviour is the only test of prompt quality. Use the UI so Oracle runs; `/api/ask` works too.
- Delete only the test threads you create. Never copy the real `config.yaml` into a test instance.

## Don't
- Push, or `git checkout` a file with uncommitted work (an uncommitted edit was lost that way once).
- Use sed/python/heredocs to edit files.
