# Nova loop — scoping plan

**Added 2026-10-26 on branch `nova-loop`.**

**Status: scoping. Nothing in this document is built, and this branch does not touch Polaris
`main`.**

This is the Polaris half of Nova's M8 adoption. The upstream contract is
[AutumnsGrove/nova#22](https://github.com/AutumnsGrove/nova/issues/22) (`docs/INTEGRATION.md`,
`docs/IMAGE.md`). Nova's image workstream and the two library prerequisites — #31 (the embedding
proof and its import guard) and #33 (`agent/sub`) — are built, tested, and closed. What is left is
the adoption itself, and the point of this document is to turn "port the loop" into sequenced,
testable workstreams with every seam named.

## 1. Goal

Replace Polaris's bespoke `agent.Run` (`agent/driver.go`, 1057 lines) with Nova's loop, behind a
flag, prove parity with tests, then delete the old loop.

Everything else stays Polaris: the gateway, the store, the SSE/websocket layer, the frontend, the
tools, the prompts, the scheduler, and the thread model. The loop is the thing being replaced, not
the application around it.

Success is one entry point with two implementations, selected at runtime, producing the same
transcript, the same events, the same usage, and the same `Result` on a shared script — and then
the flag flips.

## 2. Why

The loop is the one piece every consumer rewrote: scoped tools, isolated state, event tagging,
usage folding, depth and concurrency limits, cancellation, pseudo-tool-call sniffing, research
nudges, early exits, and a wrap-up turn. Nova now ships all of those as primitives, and the image
contract was designed around Polaris's pipeline specifically (Nova D115). Keeping the bespoke loop
means maintaining a second copy of the hard parts, and every fix to one has to be ported to the
other.

## 3. Boundary

| Stays Polaris | Moves to Nova |
|---|---|
| `gateway/` (turn handler, SSE, threads, attachments) | the turn loop: request, tools, nudges, wrap-up |
| `store/`, `prompts.yaml`, `prompts/`, `config.yaml` | the transcript types and their wire encoding |
| `tools/` implementations and their `Context` bundle | tool dispatch, parallelism, panic isolation |
| `search/`, `brave/`, `parallel/`, `tavily/`, `places/` | usage/cost accounting across calls |
| `web/`, `uiblocks/`, `thumbs/` | provider streaming and retry |
| `agent/pseudocall.go` and the nudge logic (behaviour stays) | the hooks that invoke them |

Two things change owner without changing behaviour:

- **Tool events.** Today each Polaris tool emits its own `tool_call`/`tool_result` with its
  `call_id` (38 of them). Nova's loop also emits `tool_call`/`tool_start`/`tool_end`. For parity
  the adapter **drops Nova's tool events** and lets the Polaris tools keep emitting theirs. A tool
  that is later ported to return a `tool.Result` can hand its events to the wrapper at that point,
  one tool at a time.
- **Cost.** Polaris tools call `ctx.AddCost` for their own model calls (web_read's filter pass,
  view_image's describe). Nova's `Context.AddUsage` counts nested `Complete` calls. Wiring both for
  the same call would double-count, so the rule is: `AddCost` stays the only path until a tool
  moves its model call onto `Context.Complete`, and then its `AddCost` goes in the same commit.

## 4. Entry point and flag

`agent.Run` keeps its signature, because seven call sites use it and none of them should change:

```go
func Run(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
    if novaLoopEnabled() {
        return runWithNova(reqCtx, ctx, history, userMessage)
    }
    return runBespoke(reqCtx, ctx, history, userMessage)
}
```

- `runBespoke` is today's body, unchanged, so the branch is never a half-migrated loop.
- `novaLoopEnabled` reads `cfg.NovaLoop` (new, default false) and `POLARIS_NOVA_LOOP=1` for a dev
  override.
- `runWithNova` still returns Polaris's `*agent.Result`; Nova's `agent.Result` is mapped into it,
  so `gateway/` never learns which loop ran.
- The flag is read once per call, never mid-turn. Both paths share the same test helper.

## 5. Seam map

### 5.1 Provider

Polaris builds `llm.NewClient(baseURL, apiKey, model, temperature, maxTokens)` per call site, then
`WithProvider(&ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: true})` and sometimes
`WithReasoning`. The adapter builds a Nova provider once per run:

| Polaris | Nova |
|---|---|
| `cfg.OpenRouter.BaseURL`, `APIKey` | `openrouter.New(openrouter.Config{...})` |
| `modelCfg.Model`, `Temperature`, `MaxTokens` | `agent.Config.Model`, `Temperature: &v`, `MaxTokens` |
| `ProviderRouting{Order, AllowFallbacks}` | `ProviderOptions: openrouter.Routing{Order, AllowFallbacks: &true}.Options()` |
| `ReasoningParams{Enabled, Effort, MaxTokens}` | `ProviderOptions["reasoning"]`; `ReasoningEffort` left empty |
| `SessionID` (a no-op while `provider` is set, per its own comment) | dropped in v1; preserve via `ProviderOptions` only if a caller needs it |
| `llm.ChatClient` on `tools.Context.LLM` | unchanged; tools keep their own client for side calls |

Nova's `ReasoningEffort` emits OpenAI's `reasoning_effort`, not OpenRouter's `reasoning` object, so
the adapter must use `ProviderOptions` and assert the body (5.3).

Three request differences are expected and must be recorded, not accidentally introduced:

- **`stream_options`.** Nova sends `{"include_usage": true}` so it can read usage from the stream;
  Polaris sends no `stream_options` field. OpenRouter ignores the extra field. The parity goldens
  ignore this one key (and nothing else).
- **`session_id`.** Polaris sends it but documents it as a no-op while `provider` is set, which it
  always is. The adapter drops it in v1.
- **`cache_control`.** Both send the top-level `{"type":"ephemeral"}` marker, but Polaris decides
  from the model slug (`anthropic/*`) while Nova decides from its catalog's `ImplicitCaching`. The
  adapter sets `PromptCache` when the catalog and Polaris disagree, so the marker stays on exactly
  the same models.

`MaxTurns` is passed explicitly: Polaris's `max_agent_turns` (default 50) with the existing
Deep Research multipliers. Nova has no default cap by design (D110), so forgetting this changes
behaviour.

Retry: Nova retries a retryable provider error up to 3 times by default; Polaris retries 429s twice
(three attempts, 2s/4s). Set `RetryPolicy` to match, or record the difference as accepted — a
decision, not an accident.

### 5.2 Messages and history

| Polaris | Nova |
|---|---|
| `llm.ChatMessage{Role, Content}` | `message.Message{Role, Content}` |
| `ToolCalls []llm.ToolCall` | `message.Message.ToolCalls` (`Function.Arguments` is already a string) |
| `ToolCallID` | `ToolCallID` |
| `ImageURLs []string` | `Parts []message.ContentPart` (`data:<mime>;base64,` → `Data`) |
| `history []ChatMessage` | `[]message.Message`, same order |
| `Result.Transcript` | `Result.New` (Nova records the final assistant message; Polaris appends it) |

`history` is the caller's, exactly as today. The adapter does not persist anything; `gateway/`
still owns the thread and replays `Transcript` verbatim, which is what keeps the provider prefix
cache warm (`docs/plans/verbatim-turn-transcripts.md`).

### 5.3 Tools

| Polaris | Nova |
|---|---|
| `tools.Defs(ctx)` | definition list; register one `tool.Tool` per name, in `catalogOrder` |
| `tools.Dispatch(name, argsJSON, ctx, callID)` | one generic wrapper's `Run` |
| `tools.HandlerFunc` registry | the wrapper; no per-tool code |
| tool gating by configured keys | `Registry.View(defNames...)` per run |
| `ctx.PendingQuestion`, `WizardFinal`, ... | read in the `TurnEnd` hook (5.5) |
| `ctx.AddPendingImageMessage` | flushed in `TurnEnd` into `TurnDecision.Inject` |
| `ctx.ExtraCostUSD` | added to the Nova total once when building `Result` |

The wrapper:

```go
type novaTool struct {
    info     tool.Info
    dispatch tools.HandlerFunc
    name     string
}

func (t novaTool) Info() tool.Info { return t.info }

func (t novaTool) Run(ctx context.Context, args json.RawMessage, tctx *tool.Context) (tool.Result, error) {
    pc, _ := tctx.Data.(*tools.Context)
    return tool.Result{Content: tools.Dispatch(t.name, string(args), pc, tctx.CallID)}, nil
}
```

Registration order is `catalogOrder`, because tool definitions are part of the request prefix and
a reordered list would miss the prompt cache. Descriptions are the catalog overlay, so the registry
is rebuilt per `Run` (about 40 values; cheap) rather than cached across a catalog edit.

The wrapper depends on `tool.Context.CallID`, which Nova does not have today — that is N0.

### 5.4 Events

| Nova event | Polaris action |
|---|---|
| `run_start`, `turn_start`, `message_start`, `message_end`, `turn_end`, `run_end` | ignored by the adapter; gateway already emits its own lifecycle |
| `text_delta` | `ctx.Emit("token", {"content": ...})` |
| `thinking_start`/`thinking_delta` | `ctx.Emit("reasoning", {"content": ...})`; `thinking_start`/`end` ignored |
| `usage` | accumulate; emit `cost_update` with the running total |
| `tool_call`, `tool_start`, `tool_update`, `tool_end` | dropped (the tools emit their own; see 3) |
| `permission` | dropped; no policy is wired in v1 (Polaris gates by config before the run) |
| `retry` | log; the client already surfaces the final failure |
| `compaction`, `queue_update`, `state_changed`, ... | ignored; not part of this loop |
| `error`, `extension_error` | surfaced as today's error path |

`cost_update` keeps its meaning: the running total for the turn, including `ctx.ExtraCostUSD` from
tool-side calls, emitted after each model call and after each tool batch, exactly where the current
loop emits it.

### 5.5 Hooks

| Current loop code | Nova seam |
|---|---|
| `trackResearchCall` (check-in + stale streak) | `Hooks.TurnEnd` → `Inject` |
| `trackSearchQuery` | `Hooks.TurnEnd` → `Inject` |
| empty-answer retry | `Hooks.TurnEnd` → `Continue` + `Inject` |
| `parsePseudoToolCalls` dispatch | `Hooks.TurnEnd` → `Continue` + `Inject` (one user message per call) |
| `streamSniffer` | `Config.TextFilter` (`Delta` holds, `Flush` returns `""` to drop) |
| `emitCommentary` on a pseudo call | the hook calls the same `ctx.Emit("commentary", ...)` |
| `PendingQuestion`/`WizardFinal`/`DailyItemsFinal`/`TrailClassification` | `Hooks.TurnEnd` → `End: true`, answer set by the adapter |
| wrap-up at `maxTurns` | `agent.Config.WrapUp{Instruction, Fallback}` |
| `ask_user_question` answer text | emitted by the hook (`ctx.Emit("token", ...)`), as today |

Per-run nudge state (`researchCalls`, `lastCitationCount`, `staleStreak`, `queryTracker`) lives in a
struct captured by the `TurnEnd` closure, reset on each `runWithNova` call — the same lifetime as
today's locals.

The hook sees the turn's assistant message and the tool results; the tool names and arguments are
on `Turn.Message.ToolCalls`, in the same order as `Turn.Results`.

### 5.6 Result and accounting

| Polaris `Result` | Source |
|---|---|
| `Answer` | Nova's `Result.Answer`, unless an early exit set its own |
| `Citations`, `Cards`, `Chart` | `ctx` (app state, unchanged) |
| `CostUSD` | Nova usage total plus `ctx.ExtraCostUSD`, once |
| `PromptTokens`, `CompletionTokens`, `CacheReadTokens` | Nova `Usage` totals |
| `LastPromptTokens`, `LLMCalls`, `ContextTokens` | accumulated from `usage` events / `OnMessage` |
| `TurnCount` | Nova `Result.Turns` |
| `ResearchCalls` | the nudge struct's counter |
| `Transcript` | `Result.New`, mapped back to `llm.ChatMessage` |
| `PendingQuestion`, `WizardFinal`, `DailyItemsFinal`, `TrailClassification` | set by the early-exit hook |

`ContextTokens` is the last call's prompt plus completion, which Nova does not keep in `Result`; the
adapter tracks it from the per-call `usage` events (or `OnMessage`'s usage pointer).

## 6. Workstreams

Each is one issue or one commit series. A workstream is done when its cases pass locally, the
branch is green under CI (`go build ./...`, `go vet ./...`, `go test ./...`), and any workstream
that touches concurrency runs its cases under `-race`. The flag stays off until N10.

### N0 (Nova) — `tool.Context.CallID`

**Blocker found during review.** Every Polaris tool tags its live events with the call id
(`ctx.Emit("tool_call", {..., "call_id": callID})`), and the browser correlates the tool card by it.
Nova's `tool.Context` has no call id, and the adapter cannot invent one: a synthesized id would not
match the assistant message's `tool_calls[].id`. `agent.runCall` already builds a fresh
`tool.Context` per call and tags its own progress events with `call.ID`, so this is one field
beside `tctx.Emit`.

Cases: the wrapper sees the id the `tool_call` frame carried; two parallel calls see their own ids
under `-race`; an empty id does not panic a tool. Additive, no protocol change, lands on nova
`main` with its test before N1.

### N1 — Skeleton and wire parity

**Behaviour.** `agent.Run` dispatches to `runWithNova` under the flag. Provider, history, tools, and
result mapping are in place. No nudges, no pseudo calls, no images.

**Cases.**
- A plain two-message, no-tool turn produces the same request as `llm.Client` apart from
  `stream_options` (5.1): model, temperature, max_tokens, `provider.order`/`allow_fallbacks`,
  `reasoning` (absent or the same object), `stream`, `cache_control`, tools in `catalogOrder`, no
  `reasoning_effort`, no `session_id`.
- A tool-calling turn sends one assistant message with every call, then every tool result, in
  order, and no user message between results.
- The response's content and usage map to `Result.Answer`, `CostUSD`, `PromptTokens`,
  `CompletionTokens`, `CacheReadTokens`.
- `MaxTurns` is 50 by default and doubled under Deep Research, matching today.
- Flag off = today's loop exactly (a scripted turn produces identical requests and transcript).

**Notes.** The expected request difference is `stream_options` (5.1), not a field to copy: Polaris
sends none, Nova sends `{"include_usage": true}`. Build the tool registry per run; registering in
the wrong order silently breaks caching, which a request-body golden catches.

### N2 — Events and running cost

**Behaviour.** The eight Polaris event kinds keep their payload shape, and `cost_update` carries the
same running total at the same points.

**Cases.** A scripted turn with text, reasoning, and one tool call produces the same ordered event
list as the current loop (normalizing timestamps/ids); the cost after each model call equals the
sum of provider costs so far; a tool that calls `ctx.AddCost` moves `cost_update` at the same point
it does today; Nova's tool events never reach the client (no duplicate cards).

### N3 — Tool round-trip parity

**Behaviour.** A real tool batch: dispatch, results, transcript, barrier semantics.

**Cases.** A batch of three tools runs with the same concurrency and produces results in call
order; a `code_exec` call is a barrier (no other call overlaps it); a panicking tool becomes an
error result and the turn continues; tool results are untruncated in the transcript and reach
`Result.Transcript` in the same order; a call outside the per-run view is an error result and the
turn continues; the `call_id` on each tool event equals the assistant message's id (N0).

**Notes.** Nova makes a non-parallel tool (`Info.Parallel == false`) a dispatch barrier, which is
how `code_exec`'s serialization is expressed — no per-tool special case in the adapter. Mark
tools parallel/barrier to match today's behaviour exactly: Polaris runs everything concurrently
except `code_exec`. The unknown-tool text differs by design (`error: unknown tool x` today,
`unknown tool "x"; known tools: ...` in Nova); it is model-facing only, so it is recorded as an
accepted difference rather than faked in the wrapper.

### N4 — Live filtering and pseudo tool calls

**Behaviour.** A model that writes a tool call as text is hidden live, dispatched after the turn,
and answered with a user-role result message, then the loop continues.

**Cases.** The opening chunks are held until the answer is known not to be a pseudo call, then
released; a pseudo call is never shown live; each parse result is dispatched once and appended as a
user message with the same text as today; the turn continues to a final answer; the stored
`Result.Answer` and the transcript still contain the original text for the parser and for history;
a normal answer is byte-identical to today.

**Notes.** `TextFilter.Flush` returning `""` is the drop path; the original text survives in the
stored message and `Result.Answer`, which is what `parsePseudoToolCalls` reads. The `TurnEnd` hook
must run the parser on the assistant message, not on the filtered stream.

### N5 — Research nudges and the empty-answer retry

**Behaviour.** Check-in every N research calls, stale-streak warning, repeated-search-query nudge,
and the empty-answer retry, all with today's wording and ordering.

**Cases.** Five research calls inject the same check-in message; two calls with no new citations
inject the stale warning; a repeated query injects its nudge; the Deep Research multipliers apply;
an empty final answer injects the retry and continues; a stop between turns does not inject.

**Notes.** Ordering is part of the contract: today a batch appends every tool result, then the
pending image messages, then the nudges, before the next request. The hook builds its `Inject`
slice in that order.

### N6 — Early exits

**Behaviour.** `ask_user_question`, `finalize_wizard_prompt`, `finalize_daily_items`,
`record_classification` end the turn without another model call and set their structured field.

**Cases.** Each one: exactly the same number of requests (no extra turn), `End` is returned,
the answer is the question/draft/flattened items/synthetic line, the structured field reaches
`Result`, the "token" text is emitted once, and the next turn is not ended by stale state.

**Notes.** `TurnEnd` runs after the batch; `End: true` leaves `Result.Answer` as the last assistant
text, so the adapter must overwrite it from the structured field when it builds `Result`. The
gateway must not also stream a duplicate answer.

### N7 — Images

**Behaviour.** `view_image` sees and describes, `show`/`highlight`, and images on the wire.

**Cases.** `see` queues one synthetic user message after the whole batch, in call order, with the
image part and caption; `describe` calls the vision client with one image part and its cost lands
in the turn exactly once; a text-only model gets the note and no image; a `show` call still returns
its card; the transcript round-trips through `llm.ChatMessage.ImageURLs`; the request body for an
image message is the same array shape as today (text first, then `image_url`).

**Notes.** Polaris's `PendingImageMessages` flush in the `TurnEnd` hook, after the tool results and
before the nudges. The adapter registers Polaris's configured models with `InputModalities` (N7,
open question 3); an unknown model is text-only in Nova, which would silently strip an image.

### N8 — Researcher fan-out

**Behaviour.** `spawn_researchers` uses `agent/sub` with a scoped tool view, the depth and
concurrency caps, tagged events, and folded usage.

**Cases.** A child sees only its scoped tools; its events carry a child id; its usage lands in the
parent exactly once; the cap is honoured under `-race`; cancelling the parent stops every child;
a panicking child tool is an error result; the report parsing stays Polaris's.

**Notes.** The child's `ToolData` is a fresh `*tools.Context` carrying the shared dependencies but
its own citations/cards; the adapter must not hand the parent's mutable accumulators to a child.

### N9 — Wrap-up and parity harness

**Behaviour.** An exhausted run makes one no-tools request with Polaris's instruction and fallback,
and the harness runs both loops against the same script.

**Cases.** A script that exhausts the budget produces the same final answer, the same request
sequence, and the same cost; a wrap-up that still calls a tool produces today's human fallback text;
the extra call is counted exactly once; the flag can be flipped per test.

**Notes.** `WrapUp.Instruction`/`Fallback` carry Polaris's exact strings, so the human-facing text
does not change.

### N10 — Flip

**Behaviour.** Default on, delete `runBespoke`, delete the dead helpers, keep the flag as a
kill switch for one release.

**Cases.** The whole suite passes with the flag off and on; a golden replay of every shape (plain,
tools, nudges, pseudo, early exit, images, research, wrap-up) is identical; a benchmark run matches
within noise on cost and turn count.

## 7. Parity harness

Two layers, both using `llm/llmtest.MockClient` so nothing touches the network:

1. **Scripted turn record.** A helper runs one script through both loops and records: the request
   bodies (parsed and normalized), the ordered event list, the transcript, the `Result` fields, and
   the cost. Assertions compare records, not individual calls, so a new workstream adds a script
   rather than a new test harness.
2. **Wire goldens.** For three fixed scripts (plain, one tool, image), the current loop's request
   bodies are committed as goldens; N1 asserts Nova's adapter produces the same body with the
   documented differences in 5.1 (`stream_options`, `session_id`) normalized out. This is the one
   check that catches a reordered tool list, a stray `reasoning_effort`, or a changed
   `cache_control` — the defects that pass a behavioural test and quietly cost money.

The harness is the acceptance gate for every workstream: a workstream may not merge with only
behavioural tests.

## 8. Order

N0 (Nova) → N1 → N2 → N3 → N5/N6 (independent, either order) → N4 → N7 → N8 → N9 → N10.

N4 is after N5 because the pseudo-call path injects a user message and runs the research nudges for
the pseudo-dispatched call; N7 after N3 because the image flush rides the batch; N8 after N3
because it reuses the wrapper and the tool view. N9's harness is built alongside N1, not at the
end; the last item is the flip and the deletion.

## 9. Invariants

Hold at every commit on the branch:

- The flag is off by default and the old loop is untouched.
- One `run_end` per run, and exactly one answer emitted to the client.
- No media on a `tool` or `assistant` message; images arrive as a synthetic user message after the
  whole batch.
- `Result.CostUSD` counts every billed call exactly once, including tool-side calls.
- A tool's events carry the assistant message's `call_id`.
- The transcript is what `gateway/` persists; the next turn replays it byte-for-byte.
- No Nova edits from this branch. A gap is a nova issue, built there first.

## 10. Risks and gotchas

| Risk | Why it bites | Mitigation |
|---|---|---|
| Reasoning shape | Nova sends `reasoning_effort`; Polaris sends a `reasoning` object | leave `ReasoningEffort` empty, use `ProviderOptions`, golden the body |
| Tool order | definitions are part of the cached prefix | register in `catalogOrder`; golden the body |
| Request drift | Nova adds `stream_options`, drops `session_id`, differs on `cache_control` | normalize the known differences in the harness; set `PromptCache` to match |
| Double-counted cost | tools use `AddCost`, Nova has `AddUsage` | `AddCost` only until a tool moves to `Complete` |
| Duplicate tool events | Nova's loop emits, 38 tools emit too | the adapter drops Nova's tool events |
| Missing call id | `tool.Context` has no id today | N0 |
| Retry drift | Nova retries 3, Polaris 2 with different delays | set `RetryPolicy` or record the difference |
| `MaxTurns` unset | Nova has no default cap | always set it from `max_agent_turns` |
| Wrap-up wording | Nova's defaults are shorter than Polaris's | pass `Instruction` and `Fallback` explicitly |
| Empty-answer check | `gateway/turn.go` owns the "no answer" error | don't let a hook fabricate an answer; keep the check |
| Compaction | Nova's is not Polaris's; Polaris compacts in the gateway | leave gateway compaction alone in v1 |
| Per-turn state | nudges and image queues are per turn | capture them in the `TurnEnd` closure, reset per run |

## 11. Out of scope

- Replacing the gateway, store, SSE contract, or frontend.
- Polaris's compaction (`gateway/turn_compaction.go`), title/suggestion generation, and scheduler paths.
- Her-go's `tool.yaml` traces — Nova does not have them, and this adoption does not need them.
- Porting tools to `tool.Result`, `Context.Complete`, or `Context.Decide`. One tool may move per
  later commit; none has to for parity.
- Nova's permission policy, sessions, and TUI.
- A tagged Nova release. The branch uses `replace`; a tag is a flip-time decision.

## 12. Open questions

1. **Flag scope.** One env flag for all seven `agent.Run` call sites, or only interactive turns
   (`gateway/turn_agent.go`) first, with the scheduled/research paths left on the old loop until
   parity is proven? *Leaning: one flag, but the first end-to-end test uses
   `gateway/turn_agent.go` only.*
2. **Retry parity.** Match Polaris's 2 retries/2s base exactly, or accept Nova's defaults and record
   the difference? *Leaning: match, then widen later on purpose.*
3. **Model catalog.** Register Polaris's configured models with `InputModalities` and costs, or set
   `agent.Config.InputModalities` from `modelCfg.Multimodal` for the one model? *Leaning: register
   the configured models once at startup; it also gives Nova the cost fallback.*
4. **Nova pin.** `replace` to the working tree through the whole branch, or pin a pseudo-version
   after each Nova commit? *Leaning: `replace` until N10, then a tag or a pseudo-version.*
5. **`Context.Complete` migration.** Do `view_image`'s describe and `web_read`'s filter pass move to
   `Context.Complete` in this workstream, or stay on their own clients? *Leaning: stay, so the port
   is behaviour-neutral; move later with their own tests.*

## 13. References

- Nova: `docs/INTEGRATION.md` (the polaris section and the compatibility checklist),
  `docs/IMAGE.md`, `docs/DECISIONS.md` D115 to D130, D144 to D149, nova#22, nova#31, nova#33.
- Polaris: `agent/driver.go`, `agent/pseudocall.go`, `agent/subagent.go`,
  `agent/spawn_researchers.go`, `tools/context.go`, `tools/registry.go`, `llm/client.go`,
  `gateway/turn_agent.go`, `docs/plans/verbatim-turn-transcripts.md`.
