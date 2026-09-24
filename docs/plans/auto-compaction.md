# Auto-compaction: verification, timing, and prompt quality

Working notes for GitHub issue #109.

Status: **steps 2 + 3 implemented and live-verified; the "never fired" mystery is solved
and fixed.** See "ROOT CAUSE FOUND (step 1, live)" below — compaction was firing all along
and failing silently on a prompt-shape bug, now fixed and confirmed working end to end
against a real model through `POST /api/ask`.

**Still open: step 4, summary *quality* over time.** The mechanics work and one real
summary was inspected and looked good, but that's a sample size of one, on a short
conversation, with a deliberately tiny threshold. The question this file opened with —
whether the summary preserves tool-derived facts across a long research-heavy thread —
is still unanswered, and still needs the harness described in "Step 1" (real searches,
a real model, a browser). `compactThread` builds from `loadAnswerHistory` (F6), so it
never sees tool-call structure; that remains the leading suspect if summaries turn out
lossy.

This file is the shared scratchpad; it is not user-facing docs.

## Why this exists

Auto-compaction has been implemented since prompt caching landed but has **never fired
in production**. Tool calls were once trimmed from the LLM feed, so real threads stayed
well under `context_window_tokens`; now that tool calls live in the feed again (to keep
the prompt cache's exact-prefix run), threads are expected to actually cross the
threshold. We have zero live signal on whether it works, whether its summary is good,
or whether its latency is acceptable. This is a "verify an unexercised real path" task,
in the spirit of CLAUDE.md's "Verify on real hardware, not just review or mocked tests".

## Code map (verified, not assumed)

Line numbers below were re-checked after steps 2 + 3 landed.

| Piece | Location |
| --- | --- |
| Threshold decision + detached fire | `gateway/turn.go:1006`, `:1078` |
| Notice surface (top of next turn) | `gateway/turn.go:296` |
| `compactThread` (extra non-streamed LLM call) | `gateway/turn.go:1581` |
| `Store.CompactThread` (persist summary + dual cost ledger + arm notice) | `store/store.go:2242` |
| `Store.TakeCompactionNotice` (read-and-clear, one-shot) | `store/store.go:2276` |
| In-flight guard (F5) | `gateway/server.go:490` |
| `EffectiveHistory` (summary substitution) | `store/store.go:1681` |
| `loadHistory` (normal turn prompt) | `gateway/history_replay.go:56` |
| `loadAnswerHistory` (used by `compactThread`) | `gateway/history_replay.go:129` |
| `compacted` live event emit + notice log | `gateway/turn.go:296-305` |
| `thread auto-compacted` audit row (stats only, untagged) | `gateway/turn.go:1105` |
| `compacted` frontend live handler | `web/src/lib/state.svelte.ts:1491` |
| `compacted` frontend replay (`buildTimelineFromEvents`) | `web/src/lib/state.svelte.ts:99` |
| `compacted` display component | `web/src/lib/components/ToolEvent.svelte:216` |
| Auto-compactions stat (counts the untagged audit row) | `store/stats.go:540` |
| Prompt text (`compaction_system` + trailing `compaction_task`) | `prompts.yaml:339`, fallback `prompts/prompts.go:460` |

## Lifecycle, as it ran BEFORE steps 2 + 3

Kept because F1–F7 below refer to it — the triggering turn used to do all of this
synchronously, inside the `"done"` critical path. See "Step 2 + 3: what was built" for the
current shape.

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

## ROOT CAUSE FOUND (step 1, live): the summary prompt ended on an assistant turn

The mystery at the top of this file — "has **never fired in production**" — is solved, and it
was never about the threshold. Compaction was firing. It was **failing silently, every
time**.

`compactThread` builds `[system] + loadAnswerHistory(...)`. History always ends on an
**assistant** message: a turn's own answer is the last thing persisted before that turn's
threshold check triggers compaction. A model handed a prompt that ends there reads it as
its own turn to continue rather than as something to summarize, and returns essentially
nothing. Measured live against `deepseek-v4.1-flash` (streaming, `effort: medium`):

| Prompt ends on | reasoning tokens | **content** |
| --- | --- | --- |
| assistant (what compaction sent) | 0 | **1 char** |
| user | 894 | **169 chars** |

One character of content then trips `compactThread`'s empty-summary guard, so the
compaction is skipped, the thread never compacts, and the only trace is a
`warn compaction auto-compaction failed` row — which reads as "it didn't fire" rather than
"it fired and failed". This predates steps 2 + 3 entirely: the old synchronous path had the
same prompt shape and the same guard.

`generateTitle` documents this exact hazard and already fixes it by appending a trailing
task turn (`title_regenerate_task`). Compaction simply never got the same treatment.

**Fix:** a `compaction_task` prompt (a trailing **user** turn) appended in `compactThread`,
mirroring `title_regenerate_task`. Added to `prompts.yaml`, the `prompts.Set` struct, the
compiled-in defaults, and the blank-field fallback merge. Regression test:
`TestCompactThread_EndsOnAUserTurn`, which asserts both that the task turn is last *and*
that the message before it is the assistant answer (so the test fails loudly if the
premise ever changes).

Verified live, end to end, through the real server (`POST /api/ask`, real model): a real
752-char summary landed, `compacted_pending_notice` armed, the next turn emitted
`compaction notice shown` under its own `turn_id` and cleared the flag, and the
`thread auto-compacted` audit row was written untagged with the stats count intact.

## Step 2 + 3: what was built (Design A)

The original six-step plan is below the decisions, kept for the record. What actually
shipped, and the places it diverged from that plan:

1. **Detached.** `compactThread` now fires in a goroutine after `"done"` ships, with the
   mandatory `recover()`, same shape as the suggestions goroutine. `result.CostUSD +=
   compactCost` is gone; `totalCost` no longer includes compaction spend (F2).
2. **Per-thread in-flight guard.** `Server.compactingMu`/`compactingThreads` +
   `tryBeginCompaction`/`endCompaction` (`gateway/server.go`), keyed by **storage**
   thread id (that's the row `CompactThread` writes, and it differs from the
   client-facing root on an edit/retry turn). In-memory on purpose — a persisted claim
   would wedge a thread permanently after a crash mid-compaction (F5).
3. **Pending notice persisted.** Two new appended `threads` columns:
   `compacted_pending_notice` (the flag) and `compacted_pending_cost` (accumulated with
   `+=`, not overwritten). `CompactThread` arms both in the same transaction that writes
   the summary.
   - *Correction to the plan:* the plan implied one column. It's two, and the summary is
     **not** duplicated into the notice — it's read from the existing `compacted_summary`,
     which is the current effective one. Only the cost needs its own home (both
     `threads.cost_usd` and the through-message's `cost_usd` have already absorbed it by
     the time anyone reads the notice). Accumulating rather than overwriting is what makes
     a second compaction that lands before any turn collects the first a *lag* rather
     than money that silently vanishes.
4. **Surfaced on the next turn.** `store.TakeCompactionNotice` — read-and-clear in one
   transaction, so two turns can't announce or charge the same compaction twice. Called in
   `handleTurn` right after `loadHistory`; on `ok`, emits the `"compacted"` event and logs
   a `"compaction notice shown"` row tagged to **this** turn's `turnID`.
5. **Frontend.** `compacted` handler adds `e.cost_usd` to `totalCost`, like `suggestions`.
   Replay reads the note off `"compaction notice shown"`. Added the missing
   `cost_usd?: number` to the `ServerEvent` `compacted` variant in `web/src/lib/types.ts`
   — `svelte-check` caught this; vitest alone did not, since the field is runtime-only.
6. **Forks don't inherit** (F4): confirmed and now documented at `ForkThread`'s inset —
   no copy of `compacted_summary`/`compacted_through_id`/`context_tokens` or the pending
   pair. A copied notice would describe the *root's* prefix (with a through_id meaningless
   in the fork's new id space) and charge its cost to a session that never triggered it.

### The thing the plan missed: a third consumer of the event name

`store/stats.go:540` counts `source='compaction' AND message='thread auto-compacted'` rows
for the user-facing **Auto-compactions** figure (`SettingsPanel.svelte`, `polaris stats`).
That row was the same one the frontend rendered as the timeline note, so moving the note
to the *next* turn's `turnID` would have made it double-render on replay.

Resolution: the audit row stays exactly as it was — one per compaction, now written with
an **empty `turnID`** from the detached goroutine, which makes it invisible to
`buildTimelineFromEvents` (that function is fed only a turn's own event slice) while
keeping the stat's count identical. The *rendered* note is a separate
`"compaction notice shown"` row written by the announcing turn. `thread auto-compacted`'s
`turn_id` therefore changed from "the triggering turn" to ""; safe because compaction has
never actually fired in production, so no existing row needed to keep rendering.

### Tests added

- `store`: `TakeCompactionNotice_ConsumesExactlyOnce`, `_NothingPending`,
  `CompactThread_PendingCostAccumulates`, `ForkThread_DoesNotInheritCompactionState`.
- `gateway`: `TryBeginCompaction_SerializesPerThread`; and
  `TestWebSocket_SurfacesPendingCompactionNotice`, which drives a real second turn over
  the WS and asserts the live frame (content, `cost_usd`), that it arrives *before* the
  first token, that the notice is consumed, and that the replay row carries a non-empty
  `turn_id`.
- `web`: the notice's cost landing in `totalCost` (+ the missing-`cost_usd` NaN guard),
  and a replay test pinning the note to the *announcing* turn and asserting
  `totalCost` is **not** incremented from the stored row (it's already inside the thread's
  `cost_usd`, so adding it would double-count on every reload).

Each was verified by reverting its fix and confirming the test fails.

### Still open (steps 1 + 4)

The live pass. Nothing above has run against a real model, real searches, or a real
browser. The specific things only that pass can answer: whether the note renders sanely
actually-arriving at turn start (unit tests assert the data, not the look), the real
latency the next turn pays when it collects a notice, and — step 4 — whether the summary
preserves tool-derived facts. `compactThread` still builds from `loadAnswerHistory` (F6),
so it never sees tool-call structure; that remains the leading suspect if summaries turn
out lossy.

Note the baseline-first instruction in Step 1 is now **moot for latency**: steps 2 + 3
already landed, so there is no pre-change build to measure against. Whoever runs the live
pass should either (a) measure the *new* triggering-turn latency against a normal turn
(the detached call should now contribute ~0), or (b) `git stash` this work if a true
before/after is wanted.

## Decisions pending

- **Live verification owner:** which end-to-end harness runs step 1 + step 4.
- **Where the notice's cost lands on reload vs. live** (F2, third bullet) — unchanged and
  accepted: live it arrives on the next turn, on reload it's already inside the triggering
  turn's `cost_usd`. The *session* total is correct in both; only the per-turn attribution
  differs, and it self-corrects on reload.