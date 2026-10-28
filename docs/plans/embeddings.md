# Embeddings — a shared semantic index for chat history and stars

**Added: 2026-10-28.**

**Status: phases 1a–1c implemented (2026-10-28) on `feature/embeddings` — provider abstraction,
schema + indexer, backfill CLI, cost bucket and settings-panel rows. Phases 2–4 (hybrid retrieval
for `stars`/`search_stars`/`search_chats`, then onboarding `agent/query_similarity.go`) are not
started; until they land, nothing about search or a turn has changed. Where the plan and the code
disagree, the code is authoritative — the deltas are recorded in "Implementation notes" at the
bottom.**

Live verification so far: the real 2,344-message history backfilled 1,598 eligible messages + 220
stars for **$0.0022** in one CLI run against a scratch copy of the production database, 100%
coverage reported, and `reindex --yes` reproduced it from zero for another $0.0022.

Supersedes the deferred "v2: semantic search" section of `docs/plans/search-chats.md` and the
"deliberately FTS5, not embeddings" call in `docs/plans/knowledge-vault.md`, both of which parked
semantic retrieval for three reasons that a remote embedding provider now removes:

1. *"There's no guarantee `ollama.base_url` is even configured."* — a hosted provider needs no
   local runtime, and this deployment already holds an OpenRouter key.
2. *"Backfilling embeddings for a real message history on that hardware is a genuine resource
   question."* — measured: the whole real history costs **$0.004** and 80 seconds, off-device.
3. *"Real operational cost, not just build cost."* — still true, which is why this doc front-loads
   the schema, indexer lifecycle, and eligibility rules rather than leaving them to implementation.

The one objection that remains valid, and is *not* addressed here: "this is search over your own
words, not someone else's." That applies unevenly — see "Why these three tools" below, which is why
stars go hybrid before chat history does in the rollout order.

## Why these three tools

The operator's ask: `search_chats`, `search_stars`, and the `stars` tool should all get proper
semantic retrieval, because a round trip that fails on vocabulary costs a full model turn.

They are not equally strong candidates, and the difference matters for rollout order:

- **`stars`** (`tools/stars.go` → `store.SearchLibraryStars`) — *strongest case*. Stars are
  Weaver-synthesized long-form prose about the person. A query like "what do you know about how I
  like my coffee" has almost no lexical overlap with a star titled "Morning Routine and Caffeine
  Preferences". Stars are also few (220 today), so the index is trivially complete and the
  candidate set is small enough for exact brute-force scoring with no approximation.
- **`search_stars`** (`store.SearchStars`, Weaver's own dedup lead) — *strong case, with a twist*.
  This one is a dedup guard: Weaver is instructed to always call it before `create_star`, and a
  false "nothing found" is exactly the documented cause of duplicate stars
  (`store/star_search.go`'s doc comment records a live duplicate-stars incident from FTS5's
  implicit-AND behavior). Semantic matching directly attacks that failure mode. Twist: its failure
  mode is asymmetric — a *false positive* here is cheap (Weaver reads the star, decides it's not a
  match), while a false negative creates a duplicate that pollutes the library permanently. Tune
  toward recall.
- **`search_chats`** (`store.SearchMessages`) — *weakest but highest volume*. 2,344 messages and
  growing; the query is usually a short keyword phrase. FTS5 already handles short exact-word
  queries well (see the probe table). The win is concentrated in long natural-language queries,
  which is precisely what a model tends to write.

## What the code does today (baseline)

One call site, zero persistence:

- `embed/embed.go` — a ~120-line client for a **local Ollama** `/api/embeddings` (single string in,
  `[]float32` out, 5s timeout), plus `CosineSimilarity`.
- `agent/query_similarity.go` — the only consumer. Embeds each `web_search` query and compares it to
  the *previous* query's embedding; cosine ≥ 0.90 for two consecutive queries fires a "you're
  rephrasing the same dead end" nudge. Vectors live in a per-turn struct and are discarded.
- Wired via `tools.Context.Embed` (`tools/context.go:37`), constructed at `gateway/server.go:187`
  from `cfg.Ollama.BaseURL`, warmed per turn at `agent/driver.go:558` / `agent/nova_loop.go:81`.
- No table, no index, no cache. `schema.go` has FTS5 virtual tables and no vector column anywhere.
- `gateway/verification.go:614` deliberately uses lexical word overlap for chunk selection,
  *because* `Embed` is nil on a real fraction of deployments.

Everything else that looks semantic is not: chat/star search are FTS5, books/music use genre and
list overlap, Comet and Constellation dedup are LLM judgment calls.

## Empirical probe (2026-10-28, real production history)

Run against a read-only snapshot of the live database (`polaris_polaris-data` volume copied out;
WAL not included, so the newest few writes are absent — irrelevant for sizing). **Nothing was run
on the potato itself.**

Corpus: **545 active threads, 2,344 non-empty messages, 220 active stars**, 3.99M characters.

### Model behavior — `perplexity/pplx-embed-v1-0.6b` via OpenRouter

| property | measured |
|---|---|
| endpoint | `POST https://openrouter.ai/api/v1/embeddings`, OpenAI-compatible |
| dimensions | 1024 |
| determinism | **exact** — `cos = 1.000000` across repeat calls, and batch-vs-single |
| output shape | quantized to 1/128 steps, range ±0.977, L2 norm ≈ 4.83 (**not** normalized) |
| `input_type` | accepted, **silently ignored** (bit-identical vectors for query/document/bogus) |
| `dimensions` | works (512 / 256 / 128 verified) — Matryoshka-style truncation |
| batch | 128 inputs / ~24k tokens per request works; token-aware batching needed for long messages |
| context | 32,768 tokens — **zero** of the 2,344 messages exceed it |

Determinism matters structurally: it means stored vectors never go stale against a re-embed of the
same text, so `content_hash` is a valid cache key and a re-index of unchanged text is a no-op.

Not-normalized + quantized means **cosine, not dot product**, is mandatory. And because the output
is exactly int8-representable (`round(v × 128)` round-trips with zero error), storage can be
**1,024 bytes/vector instead of 4,096 — losslessly** (see "Storage" below).

### Cost and throughput, measured end to end

| | pplx-embed (remote) | nomic-embed-text (local, this Mac) |
|---|---|---|
| full corpus (2,344 msgs, 1,007,052 tokens) | **$0.004028** | free, but 363 s |
| wall time | 80 s (53 requests) | 363 s |
| dims / context | 1024 / 32k | 768 / **2,048 tokens** |
| content never embedded | 0 messages | **105 messages (4.5%), 619,331 chars** |
| storage @ full precision | 9.2 MB | 6.9 MB |

Ongoing cost is negligible by construction: 1,007,052 tokens / 2,344 messages ≈ **430 tokens per
message ≈ $0.0000017 per message**, so ~1,000 new messages/month ≈ **$0.002/month**. A query
embedding is ~10 tokens ≈ $0.00000004. This is not a cost-optimization problem; it is a
correctness-and-hardware problem.

### Quality — and an honest note about nomic

Retrieval on eight real queries, top matches over the real corpus:

| query | FTS5 | semantic (pplx) |
|---|---|---|
| "computer less powerful than a raspberry pi that I deploy things on" | **0 hits** | Le Potato (0.463) ✓ |
| "how do astronauts use the bathroom in space" | 2 hits (correct) | Astronaut Hygiene (0.706) ✓ |
| "why do my videos not play properly in safari" | **0 hits** | Udemy Video Playback (0.465) ✓ |
| "car battery keeps dying in the cold" | 1 hit (a Weaver transcript) | Nissan Kicks Battery (0.421) ✓ |
| "fiction about AI companions called AFs" | **0 hits** | Klara and the Sun (0.535) ✓ |
| `dune` | 5 hits, correct | correct but 0.291 — no signal |
| `astronauts` | 5 hits, correct | all junk, top 0.223 |
| `battery` | 5 hits, mixed | correct-ish, 0.300 |

Two conclusions, both load-bearing:

1. **Neither tier is sufficient — fuse them.** FTS5 returns nothing on 3 of 8 natural-language
   queries; semantic retrieval is useless on bare single-word queries. This is a textbook RRF case,
   not an upgrade.
2. **On this sample, nomic and pplx rank the top hit comparably.** Side-by-side on the same six
   queries, nomic's top-1 was correct in 6/6, pplx 6/6; nomic's scores are higher and less
   compressed, pplx's ranking is slightly cleaner with fewer internal-thread hits. The case for
   pplx is therefore **not** "nomic is bad at ranking" — it's that nomic runs on the potato
   (363 s on an M-series laptop for one backfill, worse on an SBC), truncates 2048 tokens, and
   costs the deployment real CPU during every query embedding. Operator's own experience with the
   model in another project is a second, independent signal; recording the measured comparison here
   so later readers don't assume a quality gap the data doesn't show.

### What text to embed (settled 2026-10-28, measured)

Three candidate transforms, same 1,856-message corpus (the eligibility predicate below applied —
21% of raw rows are excluded), 14 hand-written natural-language queries with a known target thread,
scored by whether any message from the target thread landed in the top-k:

| variant | recall@5 | recall@10 | MRR |
|---|---|---|---|
| raw `content` | 13/14 | 14/14 | 0.751 |
| `User:` / `Assistant:` prefix | 13/14 | 14/14 | 0.738 |
| `content` truncated to 1,200 chars | 13/14 | 14/14 | 0.764 |

**Indistinguishable.** The differences are inside sampling noise on 14 queries, and 9 of 14 ranked
first under all three. So: **embed raw `content`, unprefixed, untruncated.** No transform without
evidence — a role prefix or a truncation heuristic would be untested complexity that later readers
would assume was load-bearing.

This also sets a quality bar worth remembering: **recall@10 of 100% and a median rank of 1** on real
paraphrased questions is what the fused tier has to *preserve* — an RRF change that drops below
this is a regression, not a tradeoff.

### Contamination found (must be excluded from the index)

Semantic retrieval surfaces internal scaffolding, because it *is* semantically similar:

- `weaver` transcripts — "Shooting star — …" preambles ("This conversation took place on …")
- `comet_approach` transcripts — literally "Classify this conversation. Conversation: …"
- `pulsar` routine threads
- untitled/near-empty messages ("Question.")

`store.SearchMessages` already encodes exactly the right predicate for the chat corpus
(`root.disabled`, `root.ghost`, `root.source != 'pulsar'/'weaver'/'comet_approach'`, Atlas
visibility, active-variant resolution, `notInFieldWhere(exclude_from_chat_search)`). **The semantic
tier must reuse that same SQL fragment, not re-derive it** — two copies of an eligibility rule this
subtle will drift, and the failure mode is leaking a hidden or opted-out thread into a result.

## Decisions (settled 2026-10-28)

### Provider and model

- **`perplexity/pplx-embed-v1-0.6b` via OpenRouter**, 1024 dims, $0.004/M tokens.
- Reuses the existing `openrouter.api_key` — **no new secret, no new `docker-compose.yml`
  passthrough, no new `.env` line.** `OPENROUTER_API_KEY` is already forwarded to the container
  (`docker-compose.yml:60`).
- Configurable, not hardcoded: `config.yaml` gains an `embeddings:` section (`provider`,
  `model`, `base_url`, `dimensions`). `provider: ollama` remains supported as the local option, so a
  future deployment can point at the SBC or at `jina-embeddings-v2-base-code` (already pulled
  locally) without code changes. `provider: ""` disables the whole subsystem — every consumer must
  degrade to today's FTS-only behavior, exactly as `Context.Embed == nil` does now.
- Defaults ship on: an install with no `embeddings:` block should still work (falls back to local
  Ollama if `ollama.base_url` is set, else FTS-only).

### Storage

- **One polymorphic table**, not one table per kind:

```sql
CREATE TABLE IF NOT EXISTS embeddings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	-- kind/ref_id identify the source row. 'message' refs messages.id,
	-- 'star' refs stars.id. Deliberately generic: adding a kind later
	-- (threads, memories, chapters) must not need a new table or a new
	-- backfill path.
	kind TEXT NOT NULL,
	ref_id INTEGER NOT NULL,
	-- model/dims are stored per row so a model swap is detectable rather
	-- than silently mixing incomparable vectors (CosineSimilarity returns
	-- 0 on a length mismatch, i.e. "nothing matches" — a silent wrong
	-- answer, not an error).
	model TEXT NOT NULL,
	dims INTEGER NOT NULL,
	-- int8-quantized: every value is exactly round(x*128), verified
	-- lossless for pplx-embed (values are emitted in 1/128 steps).
	-- 1,024 bytes/vector instead of 4,096.
	vec BLOB NOT NULL,
	-- sha256 of the exact embedded text — lets a re-index skip unchanged
	-- rows for free, and is what makes a re-run of the backfill cheap.
	content_hash TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(kind, ref_id)
);
CREATE INDEX IF NOT EXISTS embeddings_by_model ON embeddings(model, kind);
```

- **No vector index extension.** Exact brute-force cosine over the loaded set: 2,344 × 1,024 ≈
  2.4M multiply-adds, sub-millisecond in Go. Revisit point recorded explicitly: **beyond ~50k
  vectors reach for `sqlite-vec`**; before that, an ANN index is pure complexity, and the size
  estimate (50k × 1KB = 50 MB) is still comfortable in RAM on a 2 GB SBC.
- Vectors are held in a process-local, read-mostly slice guarded by an `RWMutex`, loaded at startup
  and appended by the indexer — every search is then a pure in-memory scan with zero SQLite work.
  SQLite stays the source of truth; the in-memory copy is a cache with a documented rebuild path.

### Indexing lifecycle

- **Sweep-driven, asynchronous, never in the request path** (revised from the original
  enqueue design — see "Implementation notes"). Every 30 s the indexer asks the store what's
  outstanding, embeds up to 200 rows in one batched call, writes them, and reloads the cache. An
  idle Polaris costs one indexed SQL query per tick and nothing else.
- **Backfill sweep on the Pulsar scheduler tick** (`gateway/pulsar_scheduler.go`, the established
  home for due-check work — same as Comet's and `backup.go`'s daily job). Picks up rows written
  before the subsystem was enabled, rows that failed, and rows inserted while the provider was
  down. Bounded per tick so a large backlog can't stall the scheduler.
- **Manual, operator-driven backfill** is the primary path for a fresh install with existing
  history — see "CLI" below. The sweep is the safety net, not the plan of record.

### Retrieval

- **Hybrid, RRF-fused.** For each tool:
  1. run the existing FTS5 query unchanged;
  2. run a semantic scan over the eligible candidate set;
  3. fuse with Reciprocal Rank Fusion (`k = 60`, the standard constant), because the two score
     scales are incomparable (bm25 vs cosine 0.2–0.7 — see the table).
- **Every tool's output shape is unchanged.** `search_chats` still returns message hits with thread
  title + `[title](/t/id)` citation; `stars` still returns `star_id`/category/title/summary lines;
  `search_stars` still returns IDs. RRF changes *which* rows come back, not how they're rendered —
  that keeps the blast radius off the frontend and off the prompts.
- **Semantic-only hits carry no snippet markers.** `search_chats` renders FTS5's `\x02…\x03`
  highlight markers; a semantic hit has no matched span, so it must fall back to a plain prefix of
  the message, and the frontend must tolerate a snippet with no highlights (it already splits on the
  markers and renders text nodes, so absence is safe — but this must be a test, not an assumption).
- **Coverage-aware degradation.** If the index covers less than some fraction of the corpus (or the
  provider is unreachable), the tool silently returns FTS-only results. A partially-built index must
  never make search *worse* than today.

### Privacy and eligibility (non-negotiable)

- **Ghost threads are never embedded, ever.** Not stored, not queued, not in the in-memory cache.
  `messages` rows for a ghost thread are hard-deleted on session end via `ON DELETE CASCADE`; an
  `embeddings` row keyed by `(kind, ref_id)` would **not** cascade, silently leaving recoverable
  vector data for a conversation the person asked to be forgotten. This is the single most dangerous
  detail in the design. Two belts: (a) never enqueue for a ghost thread, (b) a deletion hook that
  removes embeddings when a thread is hard-deleted, plus a sweep that deletes orphans.
- **Field opt-outs are honored through the shared predicate**, not a second copy: a Field with
  `exclude_from_chat_search = 1` must not contribute to semantic chat search, and a Field with
  `constellation_visible = 0` must not contribute to star retrieval.
- **Opted-out content is indexed, then filtered at query time (settled 2026-10-28).** Deliberately
  *not* "never embed it": storing and filtering keeps a single eligibility predicate on the read
  path (where it already lives, in `SearchMessages`), and lets an opt-out be reversed without a
  backfill. Two conditions make that acceptable: the filter is the **shared** predicate rather than
  a second implementation, and flipping a Field *to* opted-out **deletes that Field's existing
  embedding rows** — an opt-out is a removal request, not a display preference. Ghost content is
  the exception and is never written at all — see above.
- **Internal threads excluded**: `pulsar`, `weaver`, `comet_approach`, plus Atlas threads that were
  never continued — same list `SearchMessages` already enforces.
- **Weaver's own tool context stays Embed-free.** `gateway/constellation_weaver.go:348` deliberately
  builds a narrow `tools.Context`; semantic retrieval for `search_stars` there must go through the
  store layer's own functions, not by widening Weaver's context.

### Cost accounting and the settings panel (fully wired)

Every embedding call reports real cost in the response's `usage.cost`, so nothing is estimated:

- **Write** via `store.RecordAuxCost("embedding:"+model, cost)` for background/backfill spend, and
  `ctx.AddCost` for spend inside a live turn (a query embedding). This is the existing split —
  see `store/usage.go:136`'s doc comment.
- **Surface** as a new `CostBySource.Embeddings` bucket in `store.Stats`, because the existing
  buckets are all *content* sources (Polaris/Pulsar/Daily/Constellation/Comet) and an index
  maintenance cost belongs in none of them. Folding it into Polaris as "aux" (as ghost spend is)
  would misattribute infrastructure cost to conversation cost.
- **The "fully tacked" checklist** — every one of these, or the number is wrong somewhere:

  | file | change |
  |---|---|
  | `store/stats.go` | `CostBySource.Embeddings`; add it to both grand-total sums |
  | `web/src/lib/settings.svelte.ts` | `UsageStats.cost_by_source.embeddings` (+ update the "splits both totals five ways" comments) |
  | `web/src/lib/components/SettingsPanel.svelte` | "Embeddings" row in Cost by source **and** both `headline*CostUSD` derivations (they enumerate buckets by hand — a missing addend is the documented past bug that made the breakdown not sum to the headline) |
  | `cmd/stats.go` | `printStats` bucket line |

- Plus, in the same panel, a **Semantic index** row (`1,598 / 1,598 messages, 220 / 220 stars`),
  the model id underneath it, and a "Needs reindex" warning when rows from a previous model are
  still present — so "why is my search dumb" is answerable without SSH'ing in. Coverage is reported
  against the *eligible* count, not the raw row count: the real snapshot holds 2,344 messages, but
  only 1,598 pass the eligibility predicate, and a permanently-stuck 68% because the rest are
  excluded by design would read as a bug. The numbers come from `/api/stats`' new
  `embedding_index` section (gateway-side, since "which model is configured" isn't a SQL question),
  not from `store.Stats`.

### CLI (thin client, per the Docker-only rule)

A new `polaris embeddings` command group, following `cmd/constellation_backfill.go`'s shape exactly:

- `polaris embeddings backfill [--kind message|star|all] [--limit N] [--force]` — POSTs to the
  running server's backfill endpoint, prints progress, exits non-zero on partial failure.
- `polaris embeddings status` — coverage, model, dims, cost to date, oldest unindexed row.
- `polaris embeddings reindex --kind …` — drops and rebuilds (the model-swap path). Same command
  with `--force`, or a separate verb? **Open question** — see below.

These are thin HTTP clients against the container (`cmd/docker_client.go`'s
`runDockerModeCall` / `dockerLocalBaseURL()`), never direct database access — the DB lives in the
Docker volume and SQLite is single-writer. They must appear in `cmd/docker_cli_parity_test.go`'s
coverage the same way `stats`/`backfill` do.

`reindex` is a **distinct verb, not `backfill --force`** (settled 2026-10-28): it deletes rows, and
that should be visible in the command name rather than hidden behind a flag.

Server side: `POST /api/embeddings/backfill`, `POST /api/embeddings/reindex`,
`GET /api/embeddings/status`, all behind the same auth as the other admin endpoints, with a
**single-flight guard** (`ErrBackfillAlreadyRunning`, mirroring `store.ErrBackfillAlreadyRunning`)
— and the run must hold the same "server busy" signal that `handleServerBusy` consults
(`gateway/constellation_routes.go:154`), so a self-update can't recreate the container mid-backfill.

**The settings panel gets no action button (settled 2026-10-28)** — a cost row and an index-coverage
status row only. Backfill and reindex stay CLI-only, which also keeps a destructive operation off a
phone screen.

## Component layout

```
embed/                      provider abstraction; keeps CosineSimilarity
  embed.go                  Provider interface + OpenRouter + Ollama impls
  openrouter.go             POST /api/v1/embeddings, batch, usage.cost
  ollama.go                 the existing local implementation, moved
  quant.go                  float32 <-> int8 (verified lossless for pplx)
  embed_test.go             + determinism/quantization round-trip tests

store/
  schema.go                 embeddings table (both CREATE and the migration)
  embeddings.go             Upsert/Delete/List/Coverage/OrphanSweep + Hashes
  embedding_search.go       semantic candidates + RRF fusion helpers
  message_search.go         extract the eligibility predicate into a shared const
  star_search.go            same, for stars

agent/query_similarity.go   switch to the shared provider (behavior unchanged)

tools/
  context.go                the new closures (query embedding + per-kind semantic scan)
  search_chats.go           hybrid search
  stars.go                  hybrid search
  create_star/read_star     unaffected (read-by-id needs no similarity)

gateway/
  embedsvc.go               indexer goroutine, in-memory cache, sweep hook
  embeddings_routes.go      backfill/status endpoints, single-flight, busy signal
  server.go                 construct the service; keep the old nil-Ollama behavior
  turn_context.go           wire query-embedding + semantic closures, ghost-gated
  stats.go                  pass the new bucket through

cmd/embeddings.go           backfill / status / reindex (thin client)
cmd/root.go                 register

web/src/lib/settings.svelte.ts, components/SettingsPanel.svelte
docs/FEATURES.md, SETUP.md, CLAUDE.md, HelpModal.svelte (if it earns a name)
```

**Naming:** the service is internal, so it needs no themed name — the settings row can just read
"Embeddings". If it does earn one (`Astrolabe` was the one candidate floated — an instrument for
locating positions), it needs a `HelpModal.svelte` `TERMS` entry in the same change per CLAUDE.md.

## Rollout

1. **Provider + persistence, no behavior change.** `embed` refactor, `embeddings` table, indexer,
   backfill CLI. `search_chats`/`stars` still FTS-only. Verify: `polaris embeddings backfill` on a
   real database, coverage in `status`, cost visible in `polaris stats`.
2. **`stars` + `search_stars` go hybrid.** Smallest corpus, highest win-per-risk, and the failure
   mode (Weaver sees a star it didn't strictly need) is benign.
3. **`search_chats` goes hybrid.** Largest corpus, needs the snippet/highlight handling and the
   coverage-aware degradation most.
4. **`agent/query_similarity.go` moves onto the shared service**, and — cheaply — starts comparing
   against every query in the turn rather than only the previous one, which is the same signal with
   a better window.

Each step ships independently and each must leave the previous behavior intact when the provider is
down.

## Verification (per this repo's live-verify culture)

- **Model probe re-run in CI-free form**: a `go test` that hits the real provider is wrong; instead a
  `scripts/`-style check documented in the doc, run manually, asserting 1024 dims and
  `round(v*128)` exactness.
- **Determinism round-trip test**: embed the same text twice, assert bit-identical int8.
- **Eligibility parity test** (the important one): for a fixture DB containing an opted-out Field, a
  ghost thread, a Weaver thread, a Comet approach thread, an Atlas thread never continued, and a
  soft-deleted thread, assert the semantic candidate query returns **exactly** the row set
  `SearchMessages` returns. A regression here is a privacy leak, so it gets the same treatment as
  `store/fields_test.go`'s migration tests.
- **Ghost deletion test**: hard-delete a ghost thread, assert zero `embeddings` rows survive.
- **RRF unit tests**: identical FTS and semantic orderings; disjoint orderings; one tier empty.
- **Real-data backfill**: run against the copied snapshot, confirm cost ≈ $0.004 and coverage
  100%, then confirm `search_chats` finds the three queries FTS5 returns zero hits for.
- **Live check on the potato only after operator approval**, per the standing instruction.

## Open questions

1. **Do threads also need vectors** (title+summary) so "find the conversation" ranks once per thread
   instead of once per message? The probe was message-level and behaved well (MRR 0.751, recall@10
   14/14); adding a second kind is cheap given the generic schema, but it is extra surface — defer
   until step 3 shows message-level ranking is actually noisy.
2. **Query embedding latency.** ~0.4–0.6 s per embedded query on the tool path. Cheap mitigations
   (small LRU keyed on the query's content hash) are probably enough; measure before adding.

### Resolved 2026-10-28

- **What to embed per message**: raw `content`, no role prefix, no truncation — measured, see "What
  text to embed" above.
- **Opted-out content**: index it, filter at query time, purge on opt-out. See "Privacy and
  eligibility".
- **Model swap**: fail closed. Startup detects rows from a different model, logs, warns in the
  settings panel's index-status row, and serves FTS-only until `polaris embeddings reindex` runs.
  No surprise backfill on boot — the cost is trivial but the load spike and the "why is the SBC
  pegged" question are not.
- **Settings panel**: status only, no buttons.
- **Reindex verb**: distinct `reindex`, not a `--force` flag.

## Implementation notes (phases 1a–1c, 2026-10-28)

Deltas between this plan and the code that actually shipped, recorded so a reviewer can tell a
deliberate change from a bug.

### The indexer is sweep-driven, not queue-driven

The plan called for on-write enqueue: `AddMessage`/star writes hand a `(kind, refID)` to a queue.
That was dropped. Enqueueing means threading a callback through every write path (message insert,
star create, star merge, Pulsar, Weaver, imports), and the freshness it buys is nil in practice —
a 30 s tick that finds nothing pending costs one indexed SQL query, and the only thing the delay
costs is that a thread written *this instant* isn't semantically searchable for a few seconds.

Instead: `MessagesNeedingEmbedding` / `StarsNeedingEmbedding` derive what's outstanding from what
is stored, so the sweep is also naturally resumable, and `embedService.Kick()` exists for the one
case where freshness will matter (a chat searching its own just-written turn) when phase 3 wires
it up.

### The in-memory cache is invalidated by an epoch, not by "did I write anything"

This was a real bug caught by `TestEmbedIndexer_PurgesAfterFieldOptOut`, not by review. The first
implementation reloaded the cache only when the sweep itself had written rows — so a purge with no
writes (an opt-out, a ghost hard-delete) left the deleted vector **resident and still
semantically searchable** after the row behind it was gone.

`store.EmbeddingsEpoch` fixes it properly: an atomic counter bumped by every mutation of the
`embeddings` table (upsert, delete, purge, reindex), which the service compares against the value
it last loaded from. That also made the ghost purge's bump need to happen *after* the transaction
commits, not inside it, or a rollback would trigger a pointless reload.

### `searchableThreadWhere` is one shared value, used by three call sites

Extracted out of `SearchMessages` into `store/message_search.go`, and consumed by: the FTS path,
the indexer's "what may be indexed" query, and its "what is no longer eligible" anti-join. A
per-rule purge list would have been a second copy of a six-condition predicate; a rule added to one
copy and not the other either leaks a hidden thread or silently drops an eligible one.
`TestEmbeddings_EligibilityParityWithSearch` compares the two paths by thread id on a fixture
containing one of every shape (ghost, disabled, pulsar, weaver, comet_approach, Atlas-continued,
Atlas-not, Field-opted-out).

### Config: a new `embeddings:` block, with the old behavior as the fallback

```yaml
embeddings:
  provider: "openrouter"   # openrouter | ollama | "" (unset)
  model: "perplexity/pplx-embed-v1-0.6b"
  base_url: ""             # per-provider default
```

Resolved by `config.Config.NewEmbedClient()`. An unset provider keeps the pre-subsystem behavior
(Ollama if `ollama.base_url` is set, otherwise no embeddings at all), so an existing `config.yaml`
behaves exactly as before. The `openrouter` provider reuses `openrouter.api_key` — no new secret,
no new `docker-compose.yml` passthrough, no new `.env` line.

### CLI as shipped

```
polaris embeddings status
polaris embeddings backfill [--kind message|star|all] [--limit N]
polaris embeddings reindex  [--kind message|star|all] [--yes]
```

`reindex` without `--yes` refuses before making a request. All three are thin HTTP clients against
`/api/embeddings/{status,backfill,reindex}`; the backfill/reindex POSTs are single-flight (409 on a
second concurrent run) and hold `embeddingBackfillRunning`, which `handleServerBusy` now consults so
a self-update can't recreate the container mid-run.

### A pre-existing settings-panel bug fixed along the way

`SettingsPanel.svelte`'s `headlinePeriodCostUSD`/`headlineTotalCostUSD` enumerated five buckets by
hand and summed only four — **Comet was silently missing from the headline**, so the panel's
"$X last 30 days" under-reported by the Comet amount while the rows beneath it added up to more.
Found while adding the sixth bucket, which is exactly the failure mode
`store.GetStats`' invariant is supposed to prevent. Both derivations now sum every bucket, and
`TestBuildStats_EmbeddingBucketSumsBackToTotal` asserts the invariant server-side.

### Known-unrelated flake

`TestRunDailyPipeline_ItemizedTopStory` fails roughly 1 run in 15 on both this branch and `main`
(confirmed by running it repeatedly in a clean `main` worktree). It's a concurrency-dependent
Pulsar Daily test — the mock's scripted responses race Stage A's concurrent block calls — not an
embeddings regression. Not fixed here.

## Implementation notes (phase 2 — hybrid star retrieval, 2026-10-28)

`stars` (the assistant's library tool), the Library's search box, and Weaver's `search_stars` are
now hybrid. Same output shapes as before; only which rows come back changed.

**One fusion helper, two surfaces.** `gateway/embedding_search.go` holds `fuseRRF` (RRF, k=60) and
the two service methods. The semantic side runs against the in-memory cache and returns ids; the
ids are then re-fetched through the *existing* eligibility queries (`store.StarsByIDs`,
`store.LibraryStarsByIDs` mirroring `SearchStars` / `SearchLibraryStars`), so Weaver's surface still
sees rejected stars and the Library's still can't. That keeps each surface's rule where it already
lives instead of duplicating a status filter into the vector path.

**Degradation is the default path, not an error path.** `searchStarsHybrid`/`searchLibraryStarsHybrid`
take a possibly-nil service and call the plain FTS method when it's nil, and `embedService.embedQuery`
returns nil (→ keyword-only) rather than an error when the provider is unreachable. A broken
provider can therefore never make search worse than it was before this subsystem existed.

**Query embeddings cost real money per call** (about $0.0000001 — negligible, but recorded), so
`embedQuery` writes an `aux_usage` row per query. The ideal would be `ctx.AddCost` against the live
turn; the seam these tools call through has no turn to bill, and both paths land in the same
Embeddings bucket the settings panel shows, so this is a known, documented approximation rather
than an oversight.

### Live check on the real library (220 stars, $0.0005 to index)

`gateway/embedding_search_livecheck_test.go` (build tag `livecheck`), run against a copy of the
production database. The clearest result, verbatim:

```
Q: what do I like to drink in the morning
  FTS (5): Loves when fiction describes its technology concretely | Biology curiosity: ...
  HYB (5): Green tea over coffee: light, sweet, never bitter | ...
```

FTS5 returned five stars, none about anything she drinks. Fusion returned the actual green-tea star
first. Same pattern for "places to eat near that jazz club" (fusion added "Jinya ramen is a
favorite") and "music that sounds like longing" (fusion replaced three unrelated stars with the
atmospheric-fiction and Wayfaring-stranger ones). One query where neither did well ("how does she
keep her notes organized") — there may simply be no such star.

### Weaver's context still gets no Embed client

The plan said Weaver's tool context must stay Embed-free. It does: `tools.Context` gained no field.
The query embedding happens inside the service, which Weaver reaches only through the `search`
closure `weaverToolClosures` already took. Threading the service into
`RunShootingStar`/`RunShootingStarRecovered`/`runConstellationTick`/`BackfillConstellation` as an
explicit parameter (rather than a package-level hook) is the only signature change.
