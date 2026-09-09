# Search previous chats — v1 plan

Implements [#26 "Add a tool to search prior chats"](https://github.com/AutumnsGrove/Polaris/issues/26):
a tool letting Polaris search the user's own prior conversation history, and — the specific ask
that motivated writing this doc — **link back to the actual thread it found**, so "did I already
ask about upgrading my graphics card?" gets an answer that includes a working link straight to
that old chat, not just a paraphrase of what was discussed.

## Why this exists

Came up while building Pulsar's report-staleness fix (see `store.Store.LatestPulseReport`), which
is a narrow, deterministic "find this routine's own last pulse" lookup — not a general search.
This is the deferred general version: letting the model proactively search *any* past thread, not
just a routine's own history, and hand back a real, clickable pointer into it.

Polaris already half-solves this for humans: the sidebar's search box (`gateway/threads.go`'s
`handleSearchThreads`, backed by `store.Store.SearchMessages` and the `messages_fts` FTS5 index)
lets *you* find an old chat by typing keywords. This plan exposes that same capability to the
model as a tool, and adds a `read` action so the model can actually pull a found thread's content
back into the conversation — not just describe that it exists.

## Scope

**v1 in scope:**
- A single new tool, `search_chats`, with two actions: `search` (find matching past threads,
  either by keyword or — with `query` omitted — by recency) and `read` (fetch one past thread's
  full content by ID).
- Keyword search is FTS5, full stop — reusing `store.Store.SearchMessages` (the same index
  already backing the sidebar's search box) essentially unchanged.
- Recency mode: `search` with no `query` lists threads newest-first instead, paginated via a
  cursor — see "Recency mode" below. This is what makes "what have I been asking about lately"
  answerable without the model guessing keywords.
- A real link back to the source thread in every search result, rendered as a normal citation so
  it's clickable in the chat UI exactly like a web citation is today.
- `read`'s filtered mode: the same "double RAG" LLM-filter pattern `web_read` already uses
  (`instructions` in, a small extra LLM pass over the full thread, a condensed answer out) instead
  of always returning a raw content dump — see "The `read` action" below. This is the expected way
  `read` gets used, with a raw-transcript fallback for when filtering genuinely isn't what's needed.
- Proper cost accounting for that filter pass, via a new shared `Context.AddCost` mechanism — which
  also fixes a real pre-existing gap where `web_read`'s own equivalent filter pass's cost isn't
  currently reflected in a thread's total at all. See "Cost tracking for the filter pass" below.
- Toggleable from the settings panel, on by default, same mechanism as `web_search`/`memory`-
  adjacent tools.

**Out of scope for v1** (flagged for later, not forgotten):
- **Semantic/embedding search** — see "v2: semantic search" below for the design and, more
  importantly, why it's deliberately not in v1.
- Pagination within *raw-mode* `read` for a single very long thread — v1 caps and truncates with a
  note instead (see "The `read` action"). Filtered mode doesn't have this problem in the first
  place, since the filter pass absorbs the full thread regardless of size.
- A dedicated batch/offline "trend report" feature (see the old draft of this section, now folded
  into "Recency mode + reasoning over titles" below) — turned out not to be needed: recency mode
  plus the model reasoning over titles/snippets it already gets from `search` covers the actual
  want well enough that a separate aggregation feature isn't worth building for this.

## Walkthrough (the motivating example)

1. User, in a new or unrelated thread: *"did I ever talk about upgrading my graphics card?"*
2. Model calls `search_chats(action="search", query="graphics card upgrade GPU")`.
3. FTS5 (prefix-matched, so "upgrad" still matches "upgrading", "upgrade", etc.) returns:
   ```
   1. "Should I get a 4070 or wait for the 5070?" (thread a1b2c3, 2026-07-14)
      /t/a1b2c3
      "...comparing the RTX 4070 Super against waiting for next-gen, given my 1440p monitor..."
   ```
4. Model replies in prose, citing that result the same way it would cite a web source:
   *"Yes — you asked about this back in July: [Should I get a 4070 or wait for the
   5070?](/t/a1b2c3). You were weighing a 4070 Super against waiting for the 5070 generation."*
5. The chat UI turns that markdown link into a real citation chip (reusing the existing citation
   pipeline — see "Linking back to the thread"), and tapping it opens that exact old thread.
6. If the user follows up *"what did we land on?"*, the model calls
   `search_chats(action="read", thread_id="a1b2c3", instructions="what GPU did we end up deciding on, and why")`
   — a filter LLM pass reads the full old thread and hands back just that, not a raw transcript —
   and the model answers from it directly, instead of re-searching or telling the user to go read
   it themselves.

**A second, recency-mode example:** user asks *"what have I been asking you about lately?"* — no
keyword to search for. Model calls `search_chats(action="search")` with no query, gets back the 10
most recent threads (titles + dates + short previews), and answers directly from that list — e.g.
*"Mostly GPU shopping and a couple of cooking questions this week — you also asked about
[refinancing the car loan](/t/f9e8d7) a few days ago."* No `read` calls needed here at all; the
titles/previews already carry enough signal to answer a "what have I been up to" question.

## Tool design: `search_chats`

One tool, two actions — same shape as `memory` (`tools/memory.go`), which already established the
"one tool, an `action` enum, instead of N single-purpose tools" pattern for exactly this reason:
adding `search_chats_search` + `search_chats_read` as separate catalog entries would cost two tool-
menu slots (tokens in every turn's tool list) for what's really one capability with two verbs.

```go
var searchChatsDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "search_chats",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{"search", "read"},
					"description": "search: find past threads matching a query. read: fetch one past " +
						"thread's full content by the thread_id a search result returned.",
				},
				"query": map[string]interface{}{
					"type": "string",
					"description": "Optional for search. Free-text description of what you're looking for. " +
						"Omit entirely to list your most recent threads instead, newest first — use this " +
						"for \"what have I been asking about\" style questions instead of guessing keywords.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Optional for search: max results to return (default 5, max 15).",
				},
				"cursor": map[string]interface{}{
					"type": "string",
					"description": "Optional for search: pass back the cursor a prior search call returned " +
						"to fetch the next page — only meaningful for recency mode (no query); a keyword " +
						"search's results aren't paged.",
				},
				"thread_id": map[string]interface{}{
					"type":        "string",
					"description": "Required for read: the thread_id from a prior search result.",
				},
				"instructions": map[string]interface{}{
					"type": "string",
					"description": "Optional for read: what specifically to pull out of the thread " +
						"(\"what did we decide about X\", \"the exact wording of the plan we settled on\"), " +
						"instead of the full transcript. Strongly preferred over omitting this — see the " +
						"read action's own notes below.",
				},
			},
			"required": []string{"action"},
		},
	},
}
```

`api_description` (in `tools/descriptions/search_chats.yaml`, hot-reloadable like every other
tool's) should tell the model plainly: only search when the user references past conversation
("did I already ask...", "what did we decide about...", "that thing I mentioned before") — not on
every turn; always cite a search hit's link in the reply if you reference it, the same way a web
source gets cited, so the user gets a working pointer back to the original chat, not just a
description of one; prefer `read` over re-explaining from the snippet alone once you actually need
the old thread's content to answer, not just its existence; and when you do call `read`, pass
`instructions` describing what you're looking for rather than omitting it — you'll get back a
focused answer instead of a raw transcript, exactly the same trade-off `web_read`'s own
`instructions` parameter already makes, and it costs you nothing extra to ask for it.

This schema is deliberately future-proof for v2: if semantic search is added later, it slots in
*behind* `action="search"` as a second internal ranking signal merged into the same result list —
no new action, no new parameter, no change to how the model calls this tool. That's a design
constraint worth keeping in mind, not just a happy accident: it's why v2 stayed a "later, if
needed" appendix instead of something that had to be decided now.

## Search implementation (v1: FTS5 only)

Reuses `store.Store.SearchMessages` essentially unchanged — it already does the right thing
(resolves forked threads to their root, excludes disabled/pulsar/non-continued-Atlas threads,
ranks by FTS5 bm25). No new index, no new table, no new background job. The tool handler
(`tools/search_chats.go`) is a thin wrapper: format results, cap at `limit`, call `AddCitation`
per hit (see "Linking back to the thread").

## Recency mode (`search` with no `query`)

`query` omitted switches `search` from FTS5 ranking to plain recency ordering — newest thread
first, same `updated_at` field the sidebar already sorts by. This is the mechanism behind "what
have I been asking about lately": the model calls `search_chats(action="search")` with no query,
gets back a page of recent threads (title + date + short preview, no full content — cheap, the
same shape a keyword hit already returns), and reasons over that list directly rather than
guessing keywords to search for.

**Fixed page size: 10 threads per page**, not the keyword path's tunable `limit` — recency mode
is meant to be paged through deliberately (page 1, then page 2 if the model decides it needs
more), not dialed up to a big single fetch, so a flat, predictable page size is more useful here
than a model-guessed number. `store.Store` needs a new method for this — `ListThreadsPage(cursor
string) (threads []ThreadSummary, nextCursor string, error)`, cursor-paginated (not
offset-based, so it stays stable if a new thread is created between page fetches) over the same
`disabled`/`pulsar`/non-continued-Atlas exclusion `SearchMessages` already applies. `search`'s
formatted result includes the returned `nextCursor` inline (e.g. "10 more results — pass
cursor=\"...\" to see the next page") so the model can decide whether it's worth fetching another
page rather than always chaining through the model's own judgment being the only thing bounding
how many pages get pulled.

Each page is small by construction (10 titles + short previews, not full thread bodies), so even
a model that pages through several screens of recent threads to answer a broader question stays
cheap — this is the whole reason recency mode is a `search`-shaped feature and not a `read`-shaped
one; see the next section for why `read`'s cost profile is completely different.

## The `read` action

Fetches a specific past thread's content by `thread_id`, either as a filtered extract
(`instructions` given) or the raw transcript (`instructions` omitted). Both modes reuse
`store.GetThread` + `store.GetMessages` plus the exact compaction-substitution logic
`gateway/turn.go`'s `loadHistory` already has (a long thread that's been auto-compacted has its
early messages replaced by `Thread.CompactedSummary` — `read` should show that same collapsed
view, not error or silently omit it) — worth factoring that little substitution loop out of
`loadHistory` into a small shared helper both call, rather than duplicating it.

### Filtered mode (`instructions` given) — the expected default path

This is the "double RAG" pattern `web_read` already established (`tools/web_read.go`'s
`filterExtractedText`/`args.Instructions` branch), applied here instead of to a fetched web page:
run one extra small LLM pass over the *full* reconstructed thread text, asking it to pull out only
what `instructions` asked for, and return that instead of a raw chunk. Concretely, mirroring
`web_read`'s implementation almost exactly:

- Reuses `ctx.LLM` — the turn's own already-configured client/model, not a separate one, for the
  same reason `web_read` does (the provider pin and its prompt-cache pricing are already set up on
  it). Gated the same way too: only runs when `ctx.LLM != nil && !ctx.QuickMode` (Atlas's Quick
  Answer mode skips the filter pass for `web_read` to save a sequential round-trip; `read` should
  make the identical trade-off for the identical reason).
- A new `prompts.Tools.ThreadReadFilterSystem` prompt fragment (`prompts.yaml`, hot-reloadable,
  alongside the existing `web_read_filter_system`) — same "narrow, mechanical extraction, no
  commentary" instructions as `WebReadFilterSystem`, reworded for "a past conversation" instead of
  "a page."
- Filter input is the *whole* reconstructed thread (compaction-substituted, uncapped up to a
  generous bound — mirroring `web_read`'s `maxFilterInputChars` (100,000), since the point of the
  filter pass is exactly to reach content that would never fit in a raw display window anyway).
  This is why filtered `read` doesn't need the old 8,000-char display cap at all: the *filter
  model* absorbs the full size, and only its condensed answer — typically a few sentences to a
  short paragraph — lands in the root turn's context.
- On filter failure (LLM call errors), fall back to raw mode rather than failing the whole tool
  call — same non-fatal-degradation choice `web_read` makes.

This is what makes "read 10 threads to reason about a trend" cheap in practice, not the recency
mode's titles/previews alone: even a `read` on a genuinely huge thread costs the root turn only as
much context as the filtered answer itself, regardless of how large the source thread was. It also
makes the char-budget concern from the original design (raw dumps compounding across several
`read` calls in one turn) mostly moot for the mode the model should actually be using.

### Raw mode (`instructions` omitted) — the fallback, not the norm

Returns the thread's transcript directly, still capped at a fixed display budget (8,000 chars,
same truncate-with-a-continuation-note behavior as before) since there's no filter pass to absorb
the size here. This exists for the genuine cases filtering can't serve well — the model wants to
quote something verbatim, or the user explicitly asked to see the whole conversation — not as the
expected everyday path. `search_chats.yaml`'s `api_description` should say this plainly, the same
asymmetry `web_read`'s own description already implies by making `instructions` optional but
clearly worth using: pass `instructions` unless you specifically need the raw, full transcript.

No separate per-turn cumulative budget on top of this (the earlier draft of this plan added one,
modeled on `tools.ResearchBudget`) — dropped as unneeded machinery once filtered mode is the
expected path: `web_read` itself has no cumulative cap across multiple calls in one turn either,
and chaining several *filtered* reads doesn't have the same blow-up shape raw reads did, since
each one's contribution to context is bounded by its answer's own brevity, not by source-thread
size. The single-call 8,000-char cap on raw mode is enough of a backstop on its own, consistent
with how `web_read` is trusted to behave without a second, turn-level guard rail.

## Cost tracking for the filter pass — fixed, already shipped

**This part is done, not just planned.** Checked before designing the rest of this feature, since
a second hidden LLM call per `read` is exactly the kind of cost that's easy to lose track of:
`web_read`'s existing `instructions` filter pass turned out not to be counted in a thread's total
cost at all — and neither was `read_attachment`'s own copy of the same filter call. Both call
`filterExtractedText` (`tools/web_read.go`), which got back a full `*llm.ChatResponse` — including
a populated `CostUSD` field, confirmed against `llm/client.go`'s `ChatResponse` and how every other
caller of that method uses it (`gateway/turn.go`'s title-generation, compaction, and suggestion
calls all thread `resp.CostUSD` through to `store.Store.AddThreadCost`) — but only ever used
`resp.Content`, discarding the rest. Neither tool's `tool_result` event nor `agent.Run`'s own
`totalCost` accumulator (`agent/driver.go`) ever saw it: `totalCost` only sums `resp.CostUSD` from
the main tool-calling loop's own per-turn completion calls, with no visibility into an LLM call a
tool handler makes on its own. So every real thread that used either tool's `instructions` param
had that filter call's actual spend silently missing from its displayed total — confirmed live via
`dev/fakeopenrouter` (see below), not just by reading the code.

**Shipped fix**, in the same commit that found this — `tools.Context` gets a new mutex-protected
accumulator, the same shape as `Citations`/`Cards`:

```go
// tools/registry.go, alongside citationsMu/cardsMu
extraCostMu  sync.Mutex
ExtraCostUSD float64

// AddCost records LLM spend a tool handler incurred internally (a filter/
// extraction pass, e.g.) that agent.Run's own per-turn completion calls
// never see — without this, that cost is real (already billed by
// OpenRouter) but invisible everywhere Polaris reports a thread's cost.
func (c *Context) AddCost(usd float64) {
	c.extraCostMu.Lock()
	defer c.extraCostMu.Unlock()
	c.ExtraCostUSD += usd
}
```

No separate snapshot method — `agent.Run` reads `ctx.ExtraCostUSD` directly at each `Result{}`
construction, the same way it already reads `ctx.Citations`/`ctx.Cards` directly rather than via
`CitationsSnapshot()`/`CardsSnapshot()`: safe because that read only ever happens after a turn's
dispatch has fully joined. `filterExtractedText` now returns its `costUSD` alongside the filtered
text, and both callers (`handleWebRead`, `handleReadAttachment`) call `ctx.AddCost(filterCost)` on
success; `agent.Run` folds `ctx.ExtraCostUSD` into every `Result{CostUSD: ...}` it constructs
(seven return points — an early error, `ask_user_question`, the normal end-of-loop path, etc. —
each already threading `ctx.Citations`/`ctx.Cards` through the same way).

**Live-verified**, not just unit-tested: `dev/fakeopenrouter` got a small addition too — a `cost`
field on its scriptable `queuedResponse`, since every response defaulted to reporting `$0` before,
which made it impossible to script a real non-zero-cost scenario against a running server at all.
With that in place, a scripted `web_read` turn (a tool-call response, the filter pass's own
response, the final answer — three separately-costed calls) against a real running `polaris run`
process reported `cost_usd: 0.05` end-to-end, both in the `POST /api/ask` response and the
thread's persisted `cost_usd` fetched back via `GET /api/threads/:id` — and, rebuilding the
pre-fix code and replaying the identical script, `0.013` (exactly the two main-loop calls, missing
the filter pass's $0.037) — confirming both that the fix works and that the bug was real.

This work (shared infrastructure both `web_read`/`read_attachment` and `search_chats`'s filtered
`read` use) already exists on this branch; `search_chats`'s own filtered `read` handler just needs
to call the same `ctx.AddCost` when it's built, not invent anything new.

## Linking back to the thread

Every `search` hit's formatted result includes a plain relative link, `/t/{thread_id}` — the same
route `web/src/routes/t/[id]/+page.svelte` already serves for opening any thread — and the tool
handler calls `ctx.AddCitation(tools.Citation{Title: thread.Title, URL: "/t/" + threadID})` for
each hit, exactly like `web_search`'s `formatSearchResults` does for every web result it surfaces.
This is deliberately not a new mechanism: `Citation` is already general-purpose (its own doc comment
says as much), and `renderInlineCitations` (`web/src/lib/citations.ts`) already turns any markdown
link in the model's prose whose URL matches a tracked citation into a clickable chip — so a model
that writes `[Should I get a 4070...](/t/a1b2c3)` in its answer, backed by that `AddCitation` call,
gets the exact same chip UI a web source gets, for free.

One relative-URL wrinkle worth calling out, not a blocker: `citationLabel()` in `citations.ts`
already degrades gracefully for a relative URL (`new URL('/t/xyz')` throws with no base — caught,
falls through to `citation.title`, which is always set here), so the inline chip needs *no* change.
The one cosmetic gap is `ChatTurnView.svelte`'s expandable full source-list footer, whose own local
`hostname()` helper falls back to printing the raw path (`/t/a1b2c3`) as the "domain" subtext under
a title, instead of something sensible — small frontend fix: treat a citation URL starting with `/t/`
as an internal link there too (e.g. show "This chat" instead of a fake domain). Everything else
about citation rendering — new-tab open, sanitization, dedup by URL — already works unchanged,
including for this URL shape, since browsers resolve a relative `href` against the current page
regardless of what `citationLabel`'s own URL-parsing does for display purposes.

## Wiring

New closures on `tools.Context` (`tools/registry.go`), same pattern as `WriteMemory`/`GetMemory`/
etc. — a nil closure means "not available," gating the tool off via a new `chat_search` `Requires`
value in `tools/catalog.go` (`ctx.SearchThreads != nil`, mirroring `memory_store`'s
`ctx.WriteMemory != nil` check):

```go
SearchThreads     func(query string, limit int) ([]store.MessageSearchResult, error)
ListRecentThreads func(cursor string) (threads []store.ThreadSummary, nextCursor string, err error)
ReadThread        func(threadID string) (*store.ThreadReadResult, error)
```

All three wired together at the same call sites or not at all — same "one closure being non-nil
implies the rest are too" convention `memory`'s five closures already establish, rather than each
being independently checked.

**Every entry point that builds a `tools.Context` for a real turn needs this wired**, not just the
main chat path — this is precisely the class of gap CLAUDE.md already documents for `web_search`
(`cmd/search.go`'s CLI one-shot path originally had none of the DB-backed closures the web UI got,
found only by actually running `polaris search` live). Grepping how `WriteMemory` is wired today
shows exactly two sites that matter here:
- `gateway/turn.go` (main chat/pulsar turn path)
- `cmd/search.go` (CLI one-shot `polaris search`)

`cmd/benchmark.go` deliberately gets none of these (same as it deliberately skips the memory
closures today — an isolated benchmark run shouldn't read or write a real chat history). Deep
Research Tier 2 sub-agents (`Context.SubAgentRole != ""`) are automatically excluded too, with no
extra code needed — `tools/catalog.go`'s `subAgentToolNames` allowlist already restricts sub-agents
to a fixed tool set that a new tool isn't part of unless explicitly added.

**Catalog entry** (`tools/catalog.go`'s `catalogOrder` + a new `tools/descriptions/search_chats.yaml`):
add `"search_chats"` to `catalogOrder` (after `memory` reads naturally, alongside the other
history/context tools), `Requires: "chat_search"`, **no `Category`** (deliberately *not* tagged
`"research"` — this searches local chat history, not the outside world, so it should stay available
even in chat/`NoResearch` mode the same way `memory` does, and it costs no external API budget the
`Category: "research"` bulk-exclusion exists to protect). Not added to `nonToggleable` — the
settings panel lists it like any other optional tool, on by default, per your answer above.

## Testing & verification

Unit tests (Go, `store` and `tools` packages): `search_chats` result formatting and citation
emission; recency mode's cursor pagination (a fixed 10-per-page, stable across a page 2 fetch even
if a new thread was created after page 1 was returned); the `read` action's compaction-substitution
matches `loadHistory`'s existing behavior bit-for-bit (best done by extracting the shared helper,
not duplicating the logic and hoping the two stay in sync); filtered-mode `read` falls back to raw
mode on a filter-LLM failure rather than erroring the whole call (mirroring `web_read`'s own
`filterExtractedText` failure path — a good candidate for a shared test helper between the two,
since the fallback shape is identical); and that the new `read` handler's own filtered call reaches
`ctx.AddCost` the same way `web_read`/`read_attachment`'s already-shipped fix does (see
`TestHandleWebRead_FilterPassCostReachesContext`/`TestHandleReadAttachment_InstructionsRunFilterPass`
and `TestRun_ExtraToolCostIncludedInResultCost` for the pattern to follow — `Context.AddCost`
itself and its concurrent-safety are already covered by `TestContext_AddCost_Accumulates`/
`TestContext_AddCost_ConcurrentCallsAllLand`). `chat_search`
gating in `catalog.go` behaves like
`memory_store`'s; the CLI wiring in `cmd/search.go` actually has `SearchThreads`/`ReadThread` set
(a plain "does `polaris search` end up with a working `search_chats` tool" check — this is exactly
the class of gap that bit
`web_search` in this same file before, per CLAUDE.md).

Live check per CLAUDE.md's "verify on real hardware" culture: run `polaris search`/the web chat
against a real `polaris.db` with actual message history and confirm a `search_chats` call finds a
real old thread and the returned link opens it — `dev/fakeopenrouter` scripts the model side of
this so it's a deterministic thing to test, not a "hope a live model decides to call the tool"
situation.

## v2 (later, if actually needed): semantic search

The original draft of this plan built a full hybrid design here: a second search tier using the
existing (but currently narrowly-scoped) `embed.Client`/`embed.CosineSimilarity` — a new
`message_embeddings` table, an async on-write indexer, a backfill sweep piggybacked on the Pulsar
scheduler tick, and Reciprocal Rank Fusion to merge it with FTS5. That's deliberately *not* what's
being built for v1. Reasoning, worth keeping attached to this doc rather than just dropped:

- **This is a search over your own words, not someone else's.** Semantic search earns its keep
  when query phrasing diverges a lot from the stored text (paraphrase, synonyms, different
  language). Recalling your own recurring topics in your own vocabulary is a much easier case —
  FTS5's prefix matching already tolerates a fair amount of slop, and real vocabulary gaps
  ("GPU" vs. "graphics card") are the exception, not the norm, for one person's own chat history.
- **Real operational cost, not just build cost.** A new table, an async indexer, a backfill sweep,
  and a model-mismatch tracking scheme are ongoing maintenance surface, and the actual production
  target is a Le Potato SBC — a low-power ARM board already running Polaris and SearXNG.
  Backfilling embeddings for a real message history on that hardware is a genuine resource
  question that deserves being watched live before being built, not assumed away.
- **Unproven dependency.** There's no guarantee `ollama.base_url` is even configured on a given
  install. Building persistent infra against an optional dependency that may not exist is exactly
  the kind of premature generalization this codebase's own conventions warn against elsewhere.

**Trigger for revisiting:** real usage surfacing a concrete "I know I talked about this but
FTS5-keyword search can't find it" pattern — not a hypothetical one. If that happens, the design
sketch above (new table, on-write + backfill indexing, RRF merge) is still the right shape; it's
parked, not discarded.

## Trend analysis ("what have I been asking about") — resolved via recency mode

Earlier drafts of this doc treated this as a separate, out-of-scope aggregation feature (a
batch job clustering every thread title you've ever written). Landed somewhere smaller and
more useful instead: **recency mode plus the model reasoning over what it gets back is enough**,
without needing a dedicated analytics feature.

`search_chats(action="search")` with no query returns a page of recent threads — title, date,
short preview — and the model can page through a few screens of that (10 threads at a time) and
just reason about what it sees, the same way it reasons about anything else in context. This
isn't exhaustive analysis over your entire multi-year history in one pass, and it won't catch a
topic that only ever came up in a thread from page 40 the model didn't bother fetching — but for
"what have I been asking about lately," recency-ordered titles are already most of what a trend
answer would say, at a fraction of the cost and none of the new infrastructure a real batch
clustering job would need.

If a genuinely comprehensive all-time version of this becomes worth having later, the batch/
title-clustering sketch from the earlier draft is still the right shape for it — pull every
`Thread.Title` in one cheap SQL query, one LLM call to cluster/summarize, closer to Pulsar
Daily's model than a live tool call. Not building that now: recency mode already covers the
version of this you actually described wanting.

## Open questions for later

- The tunable constants (recency mode's 10-per-page, raw `read`'s 8,000-char cap, filtered `read`'s
  100,000-char filter-input bound) are reasonable starting points mirrored from `web_read`'s own
  numbers, not validated against real search-chats usage specifically — same "needs live-usage
  tuning" caveat this doc already applies to RRF's `k` in the parked v2 design.
- Whether `instructions` should be outright required for `read` rather than optional-but-steered —
  v1 keeps it optional (matching `web_read`'s own contract exactly) and leans on prompt guidance to
  make filtered mode the normal path; worth revisiting only if the model turns out to reach for raw
  mode more than expected in practice.
