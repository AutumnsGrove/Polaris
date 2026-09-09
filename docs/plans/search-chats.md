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
- A per-turn budget on cumulative `read` content, so a broad scan across several threads can't
  silently eat the whole context window — see "Context budget for `read`" below.
- Toggleable from the settings panel, on by default, same mechanism as `web_search`/`memory`-
  adjacent tools.

**Out of scope for v1** (flagged for later, not forgotten):
- **Semantic/embedding search** — see "v2: semantic search" below for the design and, more
  importantly, why it's deliberately not in v1.
- Pagination within `read` for a single very long thread — v1 caps and truncates with a note
  instead (see "The `read` action").
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
   `search_chats(action="read", thread_id="a1b2c3")` to pull the full old conversation back in and
   answer from it directly, instead of re-searching or telling the user to go read it themselves.

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
description of one; and prefer `read` over re-explaining from the snippet alone once you actually
need the old thread's content to answer, not just its existence.

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

Fetches a specific past thread's content by `thread_id`. Reuses `store.GetThread` +
`store.GetMessages` plus the exact compaction-substitution logic `gateway/turn.go`'s `loadHistory`
already has (a long thread that's been auto-compacted has its early messages replaced by
`Thread.CompactedSummary` — `read` should show that same collapsed view, not error or silently omit
it) — worth factoring that little substitution loop out of `loadHistory` into a small shared
helper both call, rather than duplicating it.

v1 caps total returned content at a fixed character budget (e.g. 8,000 chars, generous enough for
the large majority of real threads) and truncates with a plain "...(N earlier messages omitted,
thread continues)" note rather than building real pagination — matching how `read_attachment`
handles a similarly-shaped "this could be arbitrarily large" problem for PDFs, but simpler, since
unlike a PDF there's no natural page boundary to page through by. A truncated `read` result still
includes the thread's link, so the model can at least point the user at it even when it can't
inline the whole thing. Real pagination (page through a long thread's messages the way
`read_attachment` pages through a PDF) is a reasonable follow-up if truncation turns out to bite in
practice — not built now on the theory it might be needed.

## Context budget for `read`

A single `read` is capped at 8,000 chars (above), but that only bounds *one* call — nothing stops
the model from calling `read` on several threads in the same turn, and real threads vary a lot in
size (some of this codebase's own long deep-research or coding threads are genuinely a meaningful
fraction of a model's context window on their own). Ten such reads in one turn, even truncated to
8,000 chars each, is up to 80,000 chars (~16-20K tokens) added to that turn's request — a small
slice of a 128K-context model's budget, but a serious bite out of a smaller one, and either way
it's real cost and latency for content the model may not have actually needed in full.

Fix: a per-turn cumulative budget on `read` content, reusing this codebase's existing
circuit-breaker shape (`tools.ResearchBudget`, `tools/research_budget.go` — currently scoped to
Deep Research's search-call count, but the same "track usage on the turn's `Context`, refuse once
a ceiling is hit, tell the model plainly why" pattern applies directly here). Concretely: a
mutex-protected running total on `tools.Context` (turn-scoped already, same lifetime as
`Citations`/`Cards`), incremented by each `read` call's actual (post-truncation) content length,
hard-capped at a fixed ceiling — e.g. 24,000 chars, room for two or three full-size reads before
it kicks in. A `read` call past the ceiling returns an error explaining the budget is spent for
this turn and nudging the model to work with what it already pulled, or fall back to `search`'s
titles/snippets for anything it hasn't read yet, rather than silently truncating further or
refusing with no explanation.

This is a backstop, not the primary defense — the real mitigation is that recency mode (above)
never needs `read` at all for a broad scan: titles and short previews are what the model should be
reasoning over when the question is "what have I been asking about" across many threads, and
`read` is for drilling into the one (or two) threads that scan already identified as actually
relevant. `search_chats.yaml`'s `api_description` should say this directly — prefer scanning
`search` results over chaining `read` calls — so the budget cap is there for when that guidance
doesn't fully land, not the thing doing the steering.

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
SearchThreads func(query string, limit int) ([]store.MessageSearchResult, error)
ReadThread    func(threadID string) (*store.ThreadReadResult, error)
```

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
not duplicating the logic and hoping the two stay in sync); the per-turn `read` budget actually
refuses a call once the cumulative ceiling is crossed, and that the ceiling resets between turns
(it lives on `tools.Context`, which is already turn-scoped — same lifetime guarantee `Citations`
relies on); `chat_search` gating in `catalog.go` behaves like `memory_store`'s; the CLI wiring in
`cmd/search.go` actually has `SearchThreads`/`ReadThread` set (a plain "does `polaris search` end
up with a working `search_chats` tool" check — this is exactly the class of gap that bit
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

- The three new tunable constants (recency mode's 10-per-page, `read`'s 8,000-char single-read
  cap, and the 24,000-char per-turn cumulative `read` budget) are reasonable starting points, not
  validated against real usage — same "needs live-usage tuning" caveat this doc already applies to
  RRF's `k` in the parked v2 design.
- Whether the per-turn `read` budget should be configurable (e.g. larger on a big-context model,
  smaller on a small local one) rather than one fixed constant — v1 ships the fixed constant and
  only adds this if it turns out to actually matter in practice.
