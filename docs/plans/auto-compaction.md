# Auto-compaction: verification, timing, and prompt quality

Working notes for GitHub issue #109. Status: **investigation in progress** — nothing
implemented yet. This file is the shared scratchpad; it is not user-facing docs.

## Why this exists

Auto-compaction has been implemented since prompt caching landed but has **never fired
in production**. Tool calls were once trimmed from the LLM feed, so real threads stayed
well under `context_window_tokens`; now that tool calls live in the feed again (to keep
the prompt cache's exact-prefix run), threads are expected to actually cross the
threshold. We have zero live signal on whether it works, whether its summary is good,
or whether its latency is acceptable. This is a "verify an unexercised real path" task,
in the spirit of CLAUDE.md's "Verify on real hardware, not just review or mocked tests".

## Code map (verified, not assumed)

| Piece | Location |
| --- | --- |
| Threshold check, post-turn | `gateway/turn.go:973-988` |
| `compactThread` (extra non-streamed LLM call) | `gateway/turn.go:1495` |
| `Store.CompactThread` (persist summary + dual cost ledger) | `store/store.go:2187` |
| `EffectiveHistory` (summary substitution) | `store/store.go:1633` |
| `loadHistory` (normal turn prompt) | `gateway/history_replay.go:56` |
| `loadAnswerHistory` (used by `compactThread`) | `gateway/history_replay.go:129` |
| `compacted` live event emit/log | `gateway/turn.go:980-985` |
| `compacted` frontend live handler | `web/src/lib/state.svelte.ts:1479` |
| `compacted` frontend replay (`buildTimelineFromEvents`) | `web/src/lib/state.svelte.ts:99` |
| `compacted` display component | `web/src/lib/components/ToolEvent.svelte:216` |
| Prompt text | `prompts.yaml:339` (`compaction_system`) + fallback `prompts/prompts.go:456` |

## Lifecycle, as it runs today

1. A turn runs; `agent.Result.ContextTokens` is the **last** LLM call's
   `prompt_tokens + completion_tokens` (not a sum — `agent/driver.go:446,667`).
2. `gateway/turn.go:974`: if `ContextTokens >= cfg.ContextWindowTokens` (config
   default 200 000; **both real configs set 100 000** — `config.yaml:79`,
   `compose/polaris/config.yaml:81`), `compactThread` runs **synchronously**, after the
   answer is persisted but **before `"done"` ships**.
3. `compactThread` builds `loadAnswerHistory(threadID)` — flat role/content pairs, no
   tool-call wire shape — prepends `CompactionSystem`, makes one non-streamed call.
4. Empty summary is treated as an error (`compactThread`'s check) and skips
   persistence entirely — an empty summary would otherwise permanently erase history.
5. `store.CompactThread` writes `compacted_summary`/`compacted_through_id`, adds the
   call's cost to **both** the thread row and the `throughID` message, and reseeds
   `context_tokens` with an estimate.
6. `gateway/turn.go:980` sends `"compacted"` and logs a `compaction` event, both keyed
   to the **triggering** turn's `turnID`.
7. On every later turn, `loadHistory` → `EffectiveHistory` emits one leading assistant
   entry containing the summary in place of every message with `id <=
   compacted_through_id`. The visible transcript is untouched.

## Findings beyond the issue

### F1. Compaction is synchronous and in the `done` critical path

The triggering turn pays a full extra non-streamed LLM round-trip before the user gets
`"done"`. This is issue step 2.

### F2. Cost accounting has three surfaces, not one

- **DB correctness** (already fine): `CompactThread` updates thread total + message
  `cost_usd` atomically. `AddTurnCost`'s doc comment documents the "both ledgers" rule.
- **Live `totalCost`** (breaks when backgrounded): today the compaction cost is folded
  into `result.CostUSD` (`turn.go:986`) → `totalCost` → the `"done"` event's `cost_usd`,
  and the frontend does `this.totalCost += e.cost_usd`. Once compaction is detached,
  the triggering `"done"` no longer includes it, so the session's running total would
  silently lag.
- **Per-turn displayed cost**: `CompactThread` adds the cost to the `throughID`
  message, so on reload the triggering turn shows the higher cost; live it would not
  (unless we forward the delta somehow).

### F3. `cost_update` is NOT the right carrier

`cost_update` is produced mid-turn by the agent loop (`agent/driver.go:615,698`) and is
**deliberately excluded from `totalCost`** (`web/src/lib/state.svelte.ts:1470-1476`):
it only sets `turn.costUsd` because `"done"`/`"suggestions"` add the final cost
themselves. Repurposing it for post-`done` spend would either be a no-op for the
running total or would require breaking the contract that prevents double-counting.

The correct precedent is **`suggestions`/`verification`**: detached post-`done` cost
carriers that the frontend adds to `totalCost`
(`state.svelte.ts:1324,1558`), handled *before* the in-flight-turn gate
(`state.svelte.ts:1318`). Compaction is the same shape.

**DECIDED — Design A:** carry the compaction cost on the `compacted` event itself and
handle it like `suggestions` (`totalCost += e.cost_usd`). Emit that event at the top of
the **next** turn (issue step 3). Consequences:
- One event, one concept; matches step 3's "notify on the next turn".
- Bounded lag: the compaction cost does not hit the live session total until the next
  turn (or a reload, which reads the correct DB total). Acceptable and self-correcting,
  but must be a conscious choice.
- Live/reload discrepancy on the triggering turn's own displayed cost (F2, third
  bullet). Minor; could be revisited later.

**Alternative (Design B):** emit an immediate cost-bearing event at background
completion, and a separate notice event on the next turn. Real-time `totalCost`, but
two events for one concept and a frontend path for a cost event that attaches to no
live turn.

### F4. Forks do not inherit compaction state

`ForkThread` (`store/store.go:1388`) inserts a fresh thread row copying only
`model`/`source`, then copies messages and events. It does **not** copy
`compacted_summary`, `compacted_through_id`, or `context_tokens`. So an edit/retry fork
of a compacted thread rebuilds history from raw messages (larger prompt, no summary).
This is probably defensible (a fork is a fresh variant), but it means:
- A `compacted_pending_notice` column on `threads` won't exist on the fork, so a notice
  set on the root and then edited away could be lost or duplicated depending on which
  variant the next turn resolves to. Needs a deliberate decision in step 3.
- The issue's step-1 checklist item "nothing breaks across a fork/retry/edit boundary
  that lands right at `compacted_through_id`" is exactly this.

### F5. Backgrounding introduces a double-fire / race risk

Two fast turns could both cross the threshold before the first compaction finishes,
producing interleaved `CompactThread` writes and a summary computed against already
shrinking history. The issue's "accept the one-turn lag" covers benign ordering, not
double-firing. A per-thread in-flight guard (map of threadID, or a `compacting` flag)
is likely warranted. Also: `compactThread` captures the triggering turn's
`llm.ChatClient`; a detached goroutine holding it must be explicit, with the same
`recover()` the suggestions/verification goroutines use (the WS path has no
`net/http` panic net).

### F6. `compactThread` uses `loadAnswerHistory`, not `loadHistory`

The summary is built from flat role/content pairs — it never sees tool-call structure,
raw tool results, or the cited-sources note injected by `EffectiveHistory`. This is
deliberate (the comment says these side calls run under their own system prompt and
never shared the turn's cached prefix), but it directly bears on issue step 4's
question of whether tool-derived facts survive compaction: the summarizer sees only the
assistant's *prose answer*, and that prose is what carries the citations forward.

### F7. Existing tests cover persistence and substitution, not behavior

`gateway/turn_test.go:18,59,87` and `store/store_test.go:526,1098` cover the empty-
summary guard, the DB write, and `EffectiveHistory`/`ReadThread` substitution. Nothing
exercises the threshold trigger, the live event, or prompt quality. There is also no
`compacted` frontend test. Worth adding targeted unit tests for the new
pending-notice flow in step 3.

## Step 1: force it and watch it (deferred to an end-to-end harness)

**Verification for steps 1 and 4 is deferred.** The current harness has no web-search
capability, so it cannot drive the research-heavy, real-tool-call turns this step
needs (and judging summary quality needs a real model, not `dev/fakeopenrouter`). A
harness that can complete the whole loop end-to-end — real searches, real citations,
real forced compaction, a browser to watch the note render — will do the live pass.
Nothing in this file is a substitute for that; it is only the code map and the plan.

Environment when it runs: `dev/stack.sh` (real OpenRouter by default; `config.yaml:8-10`
has a key), backend on :8899, vite on :45173, SearXNG up. `config.yaml` is gitignored,
so edits are safe.

Test plan (for whoever runs it):
1. Set `context_window_tokens` in `config.yaml` to a small value (e.g. 2 000–4 000),
   `dev/stack.sh restart`, open the vite URL.
2. Run several back-to-back research-heavy turns (real `web_search`/`web_read`, real
   citations) until `context_tokens` crosses the threshold.
3. Confirm:
   - [ ] The `compacted` event renders as the collapsible timeline note
     (`ToolEvent.svelte`), live and after reload.
   - [ ] `EffectiveHistory` actually substitutes the summary on the next turn — inspect
     the real request (temporary log line, or the provider request if visible).
   - [ ] Cost lands on both ledgers (`threads.cost_usd` and the `throughID` message) —
     `sqlite3` the dev DB.
   - [ ] Note the wall-clock latency the triggering turn pays vs. a normal turn
     (baseline for step 2's improvement).
   - [ ] Fork/retry/edit at/around `compacted_through_id` (F4).
4. Capture the raw summary output for every run — this is the corpus for step 4's
   prompt iteration.

Baseline-first: grab one or two forced runs **before** changing any code, so the
latency and summary-quality comparison is real. (This ordering is now moot if steps
2+3 land first; note it explicitly in whatever harness does the live pass.)

## Step 2 + 3: implementation plan (Design A)

1. **Detach compaction from the `done` critical path.** Fire `compactThread` in a
   goroutine after `"done"` ships, same shape as the follow-up-suggestions and
   verification goroutines (mandatory `recover()`; the WS turn goroutine has no
   `net/http` panic net). Do **not** fold `compactCost` into the triggering turn's
   `totalCost`/`"done"` event any more (F2).
2. **Guard against double-fire** (F5): a per-thread in-flight marker so two fast turns
   can't produce interleaved `CompactThread` writes.
3. **Persist a pending notice.** `Store.CompactThread` sets a `compacted_pending_notice`
   flag (new `threads` column via the idempotent `ALTER TABLE` list at
   `store/store.go:886`), cleared once shown.
4. **Surface on the next turn.** In `handleTurn`, right after `loadHistory` succeeds
   (`gateway/turn.go:272`): if the flag is set, emit + `logEvent` `"compacted"` with the
   summary and `cost_usd`, tagged to **this** turn's `turnID` (so live and replay agree),
   then clear the flag.
5. **Frontend.** `compacted` handler adds `e.cost_usd` to `totalCost`, like the
   `suggestions` case (`web/src/lib/state.svelte.ts:1479`).
6. **Fork behavior** (F4): decide whether the pending flag is copied by `ForkThread`.
   Leaning no — a fresh variant should build its own history; a notice belonging to the
   root's compacted prefix shouldn't surface under an unrelated fork. Must be called out
   in a test either way.

## Decisions pending

- **Notice state for step 3:** exact column name/semantics, and confirm the no-copy
  fork decision (F4).
- **In-flight guard shape for step 2** (F5).
- **Live verification owner:** which end-to-end harness runs step 1 + step 4.