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
model as a tool, adds a second, complementary semantic-search path for when your phrasing doesn't
match the old chat's wording, and adds a `read` action so the model can actually pull a found
thread's content back into the conversation — not just describe that it exists.

## Scope

**v1 in scope:**
- A single new tool, `search_chats`, with two actions: `search` (find matching past threads) and
  `read` (fetch one past thread's full content by ID).
- Hybrid search: the existing FTS5 keyword index (always available, reused as-is) plus a new
  semantic/embedding path (only active when Ollama is configured — see "Degradation" below),
  merged via Reciprocal Rank Fusion.
- A real link back to the source thread in every search result, rendered as a normal citation so
  it's clickable in the chat UI exactly like a web citation is today.
- Toggleable from the settings panel, on by default, same mechanism as `web_search`/`memory`-
  adjacent tools.

**Out of scope for v1** (flagged for later, not forgotten):
- Pagination within `read` for a single very long thread — v1 caps and truncates with a note
  instead (see "The `read` action").
- A dedicated settings toggle for *just* the semantic half (e.g. "keyword-only mode") — v1's one
  toggle covers the whole tool.
- Retuning `embed.CosineSimilarity`'s threshold/RRF's `k` against real usage — ships with
  reasonable starting constants (documented inline, same "not yet tuned" honesty as
  `agent/query_similarity.go`'s own thresholds), not a validated-against-real-traffic value.

## Walkthrough (the motivating example)

1. User, in a new or unrelated thread: *"did I ever talk about upgrading my graphics card?"*
2. Model calls `search_chats(action="search", query="graphics card upgrade GPU")`.
3. The tool runs both search tiers, merges them, and returns something like:
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
					"type":        "string",
					"description": "Required for search. Free-text description of what you're looking for.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Optional for search: max results to return (default 5, max 15).",
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

## Search implementation (hybrid)

### Tier 1 — keyword (FTS5), always available

Reuses `store.Store.SearchMessages` completely unchanged — it already does the right thing
(resolves forked threads to their root, excludes disabled/pulsar/non-continued-Atlas threads,
ranks by FTS5 bm25). No new code here beyond a thin wrapper closure (see "Wiring").

### Tier 2 — semantic (embeddings), available only when Ollama is configured

Polaris already has an embeddings client (`embed.Client`, `embed/embed.go`) and a cosine-similarity
helper (`embed.CosineSimilarity`) — today used for exactly one thing, `agent/query_similarity.go`'s
in-turn "are you repeating the same search" signal. Nothing about either is specific to that use;
this reuses both for a second, independent purpose: persistent semantic search over message
history.

**New table**, added via the normal `migrations` slice in `store/store.go`:

```sql
CREATE TABLE IF NOT EXISTS message_embeddings (
	message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
	embedding  BLOB NOT NULL,   -- little-endian float32s, packed via encoding/binary
	model      TEXT NOT NULL,   -- the embed_model that produced this vector
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

BLOB (packed float32s), not a JSON array column: unlike `blocked_sources.txt`/`domain_rankings.yaml`
-style config, this table is never meant to be hand-edited, so the usual "keep it human-readable"
reasoning doesn't apply — a JSON array of ~768 floats per row is pure overhead here (parse cost, disk).

**Why `model` is stored per row, not assumed:** if the user ever changes `ollama.embed_model` in
`config.yaml`, a query embedded with the *new* model is not comparable to a vector stored under the
*old* one — cosine similarity across two different embedding models' vector spaces is meaningless,
not just "less accurate." The semantic search path must only compare against rows whose `model`
matches the currently configured `embed_model`; rows from a stale model are treated exactly like
rows that were never embedded at all (see backfill, below) — this is a correctness requirement, not
a nice-to-have, and needs a regression test (embed under model A, switch config to model B, confirm
the stale row is excluded from results and gets re-embedded).

**Indexing — two paths, both best-effort and non-blocking:**

1. **On write.** Wherever a user/assistant message is persisted via `store.Store.AddMessage`
   (`gateway/turn.go`'s main turn-completion path), fire a background goroutine that embeds the new
   message's content and calls a new `store.Store.SaveMessageEmbedding(messageID, vec, model)` —
   same "fire-and-forget, log at Debug on failure, never affect the actual response" shape as
   `warmUpEmbedClient` in `agent/query_similarity.go`. A failed or slow embed here must never delay
   sending the reply to the user.
2. **Backfill sweep**, for messages that predate this feature or whose stored `model` no longer
   matches config. Piggybacks on the existing once-a-minute Pulsar scheduler tick
   (`gateway/pulsar_scheduler.go`'s `RunPulsarScheduler`, same "no new ticker" pattern it already
   uses for the wizard-session sweep and the Daily due-check) rather than a blocking startup
   migration: a fresh install or a newly-configured Ollama could have thousands of pre-existing
   messages, and embedding all of them synchronously at startup (or in a SQL migration, which can't
   even call Ollama) would either block server start or silently fail if Ollama isn't up yet. Each
   tick embeds a small bounded batch (e.g. 20 rows: `store.Store.MessagesMissingEmbeddings(20)`,
   selecting messages with no `message_embeddings` row *or* a stale `model`) — slow enough to never
   hammer a modest Ollama instance running on the same potato as everything else, self-healing if
   Ollama is briefly down (just tries again next tick), and naturally idle (does nothing) once
   caught up.

Both paths no-op entirely when the server's embed client is nil (`ollama.base_url` unset) — same
nil-check gating as every other optional-dependency client in this codebase (`tavily.NewClient`,
`places.NewFoursquareClient`, `embed.NewClient` itself).

**Query time:** embed the query string once via the same `embed.Client.Embed`, then a new
`store.Store.SearchMessagesSemantic(vec []float32, limit int) ([]MessageSearchResult, error)` does
a brute-force cosine-similarity scan over `message_embeddings` (joined against `messages`/`threads`
with the *exact* same filter clause `SearchMessages` already uses — disabled/pulsar/non-continued-
Atlas exclusion, fork-root resolution — factor that `WHERE`/`JOIN` shape into a shared helper so the
two tiers can never drift apart on which threads are eligible). Brute-force, not an ANN index: this
is a single-operator install with realistically thousands, not millions, of messages — a linear
scan in Go is plenty fast at that scale, and adds no new dependency (sqlite-vec or similar) for a
problem this small.

### Merging the two tiers

Reciprocal Rank Fusion — not a new technique for this codebase, the exact same algorithm
`docs/plans/local-search-frontend.md`'s Atlas ranking design already specifies for combining
multiple engines' incomparable rankings into one list. Same reasoning applies here: FTS5's bm25
score and cosine similarity aren't on comparable scales, so merging by *rank position* (score =
Σ 1/(k + rank_i) across whichever list(s) a result appears in, k = 60, the standard RRF constant)
sidesteps having to normalize two unrelated scoring functions against each other. Implemented as a
plain Go function in the tool handler (`tools/search_chats.go`), not store-side — it's pure
in-memory merging of two already-fetched result lists, no SQL involved.

### Degradation

No Ollama configured → Tier 2 never runs (embed client is nil) → the tool is FTS5-keyword-only,
silently, with zero difference to the model-facing tool contract. This is deliberate: the feature
must work identically well on a bare-metal or Docker install that never set `ollama.base_url`,
matching how `agent/query_similarity.go`'s signal already degrades to "just doesn't fire" under the
same condition.

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
`read_attachment` pages through a PDF) is a reasonable v2 if truncation turns out to bite in
practice — not built now on the theory it might be needed.

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
value in `tools/catalog.go` (`ctx.SearchThreadsKeyword != nil`, mirroring `memory_store`'s
`ctx.WriteMemory != nil` check):

```go
SearchThreadsKeyword  func(query string, limit int) ([]store.MessageSearchResult, error)
SearchThreadsSemantic func(vec []float32, limit int) ([]store.MessageSearchResult, error) // nil-safe to call even with embed disabled — see below
ReadThread            func(threadID string) (*store.ThreadReadResult, error)
```

`SearchThreadsSemantic` is wired to a real closure even when Ollama isn't configured — it just
returns an empty slice, so the tool handler doesn't need its own separate "is semantic available"
branch; whether Tier 2 contributes anything is decided once, inside the closure, not scattered
across call sites.

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

Unit tests (Go, `store` and `tools` packages): `SearchMessagesSemantic` filters out stale-model
rows; RRF merge produces a stable, deduped-by-thread-and-message ordering given two overlapping
input lists; the `read` action's compaction-substitution matches `loadHistory`'s existing behavior
bit-for-bit (best done by extracting the shared helper, not duplicating the logic and hoping the
two stay in sync); `chat_search` gating in `catalog.go` behaves like `memory_store`'s.

Per this repo's own stated culture (`CLAUDE.md`'s "Verify on real hardware, not just review or
mocked tests"), this feature specifically needs a live check beyond `go test`: confirm the backfill
sweep actually catches up a real `polaris.db` with pre-existing message history once
`ollama.base_url` is set, using `dev/fakeopenrouter` for the chat completions side and a real (or
locally-run) Ollama for the embeddings side — a mocked embed client can't catch a real backfill
batch size/rate mismatch or a genuinely broken model-mismatch filter the way actually watching the
`message_embeddings` table fill in over several scheduler ticks can.

## Open questions for later

- RRF's `k=60` and the semantic-tier's own result cap are unvalidated starting points, same
  "needs real-usage tuning" caveat `agent/query_similarity.go` already carries for its own
  constants.
- Whether `read`'s 8,000-char truncation is the right budget, or whether real long-thread usage
  demands actual pagination sooner than expected.
- A "forget this from search" per-thread opt-out (some old threads might be sensitive enough that
  the user wouldn't want the model surfacing them unprompted) isn't in v1 — worth a follow-up ask
  if it comes up in practice, but nothing in the current design blocks adding it later (a `threads`
  column + a `WHERE` clause both search tiers already share via the joined-filter helper above).
