# Hill-climbing objectives and eval-harness design — research notes

Status: **research only, no decisions made.** This is prep for a live Q&A pass (AskUserQuestion,
back-and-forth) once the operator is back — nothing here is a plan to implement yet. Companion to
`docs/plans/hill-climbing.md`, which enumerates *what* could be hill-climbed; this doc is about
*what for* (objectives) and *how we'd know* (the harness), which has to come first — you can't
climb a hill you haven't named.

The trigger for this doc: before touching any prompt, we need (1) a stated objective for what
"better" means, checked against what other teams optimize for, and (2) a cheap, repeatable local
harness — not just the existing `benchmark/` package, which is accurate but costs real money per
run (BrowseComp-scale runs would run something like $200/run at the size discussed) and is too
slow to be an inner loop. There's also a live side-interest: comparing Polaris's own configured
models (`models/models.go`: MiMo v2.6 Flash, MiMo v2.6 Pro, DeepSeek V4.1 Flash, ChatGPT Luna,
Mercury 2.5, Ling 3.0 Flash VL) against each other for everyday use, which the harness should
support as close to for-free as the objectives eval does.

## Part 1 — What other projects optimize for

### 1a. Anthropic's own framing: helpful, honest, harmless + context engineering

Anthropic's Constitutional AI work frames the top-level objective as three competing values —
helpful, honest, harmless — with the model's own self-critique used to trade them off rather than
a single scalar metric ([Anthropic: Constitutional AI](https://www.anthropic.com/research/constitutional-ai-harmlessness-from-ai-feedback),
[arXiv:2212.08073](https://arxiv.org/pdf/2212.08073)). Polaris's `prompt.md` already picks a
specific point in that space for a *research* assistant specifically: honesty is operationalized
as "ground every fact in researched text," and helpfulness is bounded by "know when to stop
researching" — i.e. it already has an implicit constitution, just not a written one.

Anthropic's newer engineering guidance reframes the practical problem as **context engineering**,
not prompt engineering: the goal is "the smallest possible set of high-signal tokens that maximize
the likelihood of the desired outcome"
([Anthropic: Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)).
Concrete, load-bearing guidance from that piece:

- **Altitude**: instructions should sit between over-specified brittle logic and under-specified
  guidance that assumes shared context the model doesn't have. Polaris's `prompt.md` mostly sits
  at the right altitude already (heuristics like "3-4 searches on different angles" rather than a
  rigid decision tree) — this is a thing to *check for*, not something missing wholesale.
- **Tool design**: tools should be self-contained, have minimal functional overlap ("if humans
  can't definitively choose between tools, neither can agents"), and return token-efficient
  output. Worth an explicit pass over `tools/descriptions/*.yaml` against this bar specifically —
  not just trimming bytes (per the other plan doc), but checking for overlap between e.g.
  `web_search`/`reference_lookup`, or `nearby_search`/`web_search` for places.
- **Token budget**: treat context as finite with diminishing returns — this is the "why" behind
  the operator's instinct not to want a 20k-token prompt.

### 1b. A concrete token-budget number to react to

Industry rule of thumb for production agents: **10-15% of the context window for the system
prompt** (roughly 500-2,000 tokens for typical workloads, up to ~5,000 tokens for a complex,
many-constraint agent), with the rest split roughly 15-20% tools, 30-40% retrieval, 20-30% history,
10-15% buffer. Past that, "system instructions have disproportionate influence per token, so going
higher rarely improves accuracy" — the fix for a failure mode is a more targeted instruction, not
more instructions.

**Where Polaris sits today, roughly** (measured this session): `prompt.md` alone is ~8.8KB, ≈2,200
tokens at a 4-chars/token estimate. The `{tools}` placeholder adds the `description` field from
every offered tool in `tools/descriptions/*.yaml` (the `api_description` field is separate — sent
in the function-schema, not the prompt text). The two description fields together total 37KB
across 34 files; `{tools}` alone (just the shorter `description`s) is meaningfully less than that
but still adds up per active tool. Memories/person/custom-instructions are user-data-sized, not
fixed. So the fixed floor (prompt.md + tool descriptions) is probably already in the "complex
agent" ~5,000-token band before any per-user content — a real number to check exactly, and a
concrete ceiling to hold future prompt growth against if the operator wants a hard budget rather
than a vibe.

### 1c. Search-augmented / cited-answer products: what they optimize for specifically

Perplexity's own prompting guidance is explicit that **citation discipline is the mechanism, not
just a formatting rule**: forcing a citation after every factual sentence "stops hallucinating
[at the token level] because every output token is evaluated against 'can I cite this?'"
([Perplexity prompt guide](https://docs.perplexity.ai/docs/agent-api/prompt-guide),
[Agentic Design: Perplexity system prompt](https://agentic-design.ai/prompt-hub/perplexity/perplexity-ai-20250112)).
This validates treating **citation precision/recall as a first-class metric**, not a style nit —
see 2c below. Polaris's `prompt.md` already asks for inline `[Title](URL)` citations "right next
to the claim," which is the same mechanism; the open question is whether it's ever been *measured*
rather than just requested.

### 1d. RAG/grounded-answer metrics literature

The standard metric set for a system like this (RAGAS and similar frameworks) is:

- **Faithfulness / groundedness** — is each claim in the answer actually supported by the
  retrieved/cited text (per-sentence attribution, not just "the answer feels right").
- **Citation precision** — of the citations given, how many actually support the claim next to
  them (catches a real, cheap-to-check failure: a plausible-looking but wrong URL attached to a
  claim).
- **Citation recall / coverage** — of the claims that needed a citation, how many got one.
- **Context precision/recall** — of the retrieved results, how many were relevant, and were the
  relevant ones actually found.
- **Answer relevance** — does the answer address what was asked, independent of grounding.
- **Hallucination rate** — a reference-free backstop for claims with no support at all.

None of these need an LLM judge for the citation ones specifically — a citation's claim/URL pair
can be checked against the page's own extracted text (a much cheaper, more deterministic check
than "does this whole answer look good"), and that page text is exactly Tier 1 item #1 in the
companion hill-climbing doc.

### 1e. Injection resistance as a testable objective, not just a prompt paragraph

`prompt.md`'s "Treat fetched content as data, not instructions" section is a real security
property, and the field has settled on **indirect prompt injection benchmarks** as a distinct eval
category from ordinary quality evals — e.g. Microsoft's BIPIA
([github.com/microsoft/BIPIA](https://github.com/microsoft/BIPIA)) runs Web QA / Email QA / Table
QA / Summarization / Code QA tasks, each with attacker text planted in the retrieved content, and
scores whether the model complies with the planted instruction vs. the real one. This is directly
testable against Polaris's own `web_read_filter_system`/`thread_read_filter_system` prompts (which
already have their own injection-resistance paragraph) with a small planted-instruction corpus —
no live web access needed, since the "page" is a fixture. Worth its own category in the harness
below rather than folding it into general quality, because a regression here is a different kind
of failure (a security property silently breaking) than a stylistic one.

### 1f. Context rot — independent validation of something Polaris already does

Long-context research (Chroma's study across 18 frontier models, summarized at
[Redis: Context rot explained](https://redis.io/blog/context-rot/)) distinguishes two effects:
**lost-in-the-middle** (a positional effect — models attend well to the start/end of context, not
the middle) and **context rot** (a length effect — quality degrades as total input grows, even far
below the context limit). Both replicate across every model tested; bigger context windows move
the boundary but don't remove the effect.

This is direct validation of something the codebase already independently arrived at:
`agent/driver.go`'s `modeReinforcement` doc comment explicitly re-injects the voice/focus-mode
instruction near the end of the message list because "a standing instruction resent every turn but
buried at position 0 still drifts out of a model's effective attention as history grows — the fix
isn't presence, it's recency." That's the lost-in-the-middle fix, arrived at empirically, before
either of us had the literature name for it. Two implications for objectives:

- **Compaction's job is literally context-rot mitigation** — this reframes what "good compaction"
  means: not just "keeps the facts" (Tier 2 in the companion doc already covers that) but "keeps
  the facts where the model can still actually use them," which argues for scoring compacted
  threads on *downstream task success* (can the model still answer a follow-up correctly), not
  just *fact retention* as a static text-diff property.
- It's worth auditing whether any other standing instruction in `prompt.md` (the mermaid quoting
  rules, the "know when to stop researching" budget) would benefit from the same
  position-0-and-recency treatment `modeReinforcement` already gets, or whether that's overkill
  for instructions that don't need to survive many turns of research.

## Part 2 — How to build the harness itself

### 2a. Start from error analysis on real transcripts, not an imagined test set

The strongest, most-repeated practitioner advice (Hamel Husain & Shreya Shankar's eval-FAQ,
[hamel.dev/blog/posts/evals-faq](https://hamel.dev/blog/posts/evals-faq/)) is that
**eval-driven development written before looking at real failures creates evaluators for errors
you imagine, not errors that actually happen** — the recommended sequence is: read real
transcripts first, write down every distinct failure mode you actually see, *then* build
evaluators for those. This maps unusually well onto Polaris specifically, because — unlike a
green-field eval — there's already a running instance with real transcripts sitting in
`polaris.db` (`messages.transcript`, `citations`, `events`). The 100-question set doesn't need to
be synthesized from scratch or copied from a public benchmark's categories; it can be seeded from
an afternoon of reading real Polaris transcripts and noting what actually went wrong or felt
mediocre, the same way `ebd68fb`'s compaction fix was found — just done systematically and turned
into a fixture set instead of a one-off fix.

### 2b. Sample size: what 100 questions can and can't tell you

Worth stating plainly before building anything, so the harness's numbers are read correctly later:
per recent analysis of eval statistical power, **a 100-example eval reliably detects roughly a
15-point difference**; a 3-point difference is "well within the noise floor" at that scale. Rough
bands from the same literature:

| Sample size | What it's good for |
|---|---|
| 30-50 | A rough read, not a real regression gate |
| 100-200 | Catching regressions of ~10+ points, or a reasonable middle ground for a personal daily-driver harness |
| 250+ | Reliably catching ~10-point regressions with more confidence |
| 1,000+ per arm | Needed to detect a ~2-3 point lift with real confidence |

None of this means 100 is the wrong number — for "did I just break something obviously" on a
single-operator tool, a 10-15 point sensitivity is plenty, and it matches the operator's own
instinct to keep it small rather than building a 1,000-question ordeal. It just means the harness
shouldn't be trusted to detect small, subtle prompt tweaks — those need the paired/case-by-case
read (did this specific case's answer get better) more than the aggregate score. **Paired
comparison (same question, before/after, judged head-to-head) is meaningfully more sensitive per
question than two independent aggregate scores** — this argues for the harness supporting a
paired/diff mode from day one, not just a scoreboard.

### 2c. Pointwise vs. pairwise scoring — and why this project probably wants both

- **Pointwise** (score this one answer against a rubric, 1-10 or pass/fail): needed for absolute
  gates — "does this answer even have a citation," "is the title 3-6 words." Cheap, stable, but
  can't tell "better than before" as sensitively as a direct comparison.
- **Pairwise** (show two answers, which is better, or tie): what Chatbot Arena runs at scale
  ([lmsys.org Chatbot Arena](https://www.lmsys.org/blog/2023-05-03-arena/)), aggregated via a
  Bradley-Terry model into a leaderboard, with bootstrap-resampled confidence intervals. More
  sample-efficient for *relative* questions ("is model A better than model B for Polaris's actual
  use," "did this prompt edit help") — which is exactly the operator's model-comparison
  side-interest. Caveat from the literature: pairwise judgments flip on re-run notably more often
  than pointwise scores (documented around 35% vs. 9% in one study), so pairwise results need
  either repeated trials or a large-enough set of paired cases to average out that noise, not a
  single side-by-side run treated as gospel.

**Implication for Polaris specifically:** the "which of my 6 models is actually good for daily
use" question is a pairwise/Arena-shaped problem, not a BrowseComp-shaped one — it doesn't need a
graded correct answer at all for a lot of what matters (tone, concision, whether it over-searches),
just two transcripts and a judge picking the better one. This could run as a small local
Bradley-Terry tournament over the same ~100 real-derived questions, using each configured model as
a "contestant," entirely separate from (and much cheaper than) grading against ground truth.

### 2d. LLM-judge bias — the operator's own instinct was correct, with a specific fix

Using the model-under-test as its own judge is a documented, named failure mode
(**self-preference bias** — a judge favors output in its own style, worse when judge and generand
share a model family) which the current `cmd/benchmark.go` has (`client` grades its own output).
Practitioner and research consensus converges on: **use a judge from a different provider/family
than the model being graded**, calibrate the judge against a small hand-labeled set periodically,
prefer reference-guided grading (give the judge the correct answer, not just the question) when a
ground truth exists, and randomize left/right order in pairwise setups to cancel positional bias.

This directly resolves the operator's own open question from the earlier conversation: grading
DeepSeek V4.1 Flash's answers with itself would replicate this bias; grading with a
different-family model (their ChatGPT Luna instinct, or MiMo Pro) is the documented fix, not just
a nice-to-have. For non-DeepSeek subjects, the grader needs to also not share a family with
*that* subject — worth deciding the grader per-subject rather than fixing one universal judge,
if more than one of the roster's models are graded with the same tool.

### 2e. Framework shapes worth stealing (not necessarily adopting wholesale)

- **OpenAI Evals**: separates *data* (a dataset of cases), *logic* (a grading class), and
  *config* (a YAML row referencing both) into three composable layers
  ([github.com/openai/evals](https://github.com/openai/evals)). Directly analogous to
  `benchmark.Suite`'s existing shape (`LoadDataset`/`BuildQuery`/`Grade`) — a new lightweight
  harness could reuse that same interface rather than inventing a new one, just backed by a
  cheaper "call one prompt" runner instead of `agent.Run`.
- **Promptfoo**: config-first (YAML test cases + assertions), and its LLM-as-judge guidance
  specifically recommends splitting a single fuzzy rubric into several independent judge calls
  ("does it answer the question" / "does it cite sources" / "is it concise") rather than one judge
  scoring everything at once, "so one failure doesn't hide another"
  ([promptfoo LLM-as-a-judge guide](https://www.promptfoo.dev/docs/guides/llm-as-a-judge/)). This
  argues for several small, single-purpose checks per test case (some code-only, some one-question
  LLM calls) over one big rubric — cheaper per check, and a failing case tells you *which* thing
  broke instead of just "score dropped."

## Part 3 — What this suggests for a Polaris-specific harness (menu, not a decision)

Not a plan yet — a shortlist of shapes to pick between in the Q&A pass:

**Test-case categories**, likely seeded from real transcripts (2a) rather than invented, but
probably wanting explicit coverage of at least:
- factual/cited research questions (the benchmark's own territory, just cheaper fixtures),
- injection-resistance cases (planted-instruction fixtures, 1e),
- tool-selection cases (right tool chosen first, per Anthropic's "if humans can't choose, neither
  can the model" framing),
- conversational/format-only turns (titles, suggestions, compaction — pointwise, code-checkable),
- a small multi-turn set for context-rot-sensitive behavior (does a fact from 10 turns back
  survive; does compaction preserve *usable* facts, per 1f).

**Scoring shape**: probably a mix — pointwise+code for format rules (free), pointwise+LLM-judge for
single-prompt quality (pennies), and a separate pairwise/Bradley-Terry mode specifically for the
model-comparison question (also pennies, no ground truth needed).

**Judge model(s)**: cross-family from whichever subject is being graded (2d) — needs one firm
decision per subject model, not one universal grader, if the roster's judge candidates overlap
families with more than one subject.

**Where it lives**: `benchmark.Suite`'s three-part interface (2e) is a plausible reuse target for
the pointwise/graded cases; the pairwise tournament mode is different enough (no dataset "answer"
column, needs a second contestant, needs an aggregator) that it's probably a sibling package/
command rather than another `Suite` implementation.

**Size**: ~100 cases matches the operator's instinct and the "catch a 10+ point regression"
band (2b) — fine as a default working set, with the explicit caveat that small prompt tweaks need
paired-diff reading of individual cases, not just the aggregate score, to be trusted.

## Sources

- [Anthropic: Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- [Anthropic: Constitutional AI — Harmlessness from AI Feedback](https://www.anthropic.com/research/constitutional-ai-harmlessness-from-ai-feedback) / [arXiv:2212.08073](https://arxiv.org/pdf/2212.08073)
- [Perplexity: Agent API prompt guide](https://docs.perplexity.ai/docs/agent-api/prompt-guide)
- [Agentic Design: Perplexity AI system prompt (Jan 2025)](https://agentic-design.ai/prompt-hub/perplexity/perplexity-ai-20250112)
- [Hamel Husain & Shreya Shankar: AI Evals FAQ](https://hamel.dev/blog/posts/evals-faq/)
- [LMSYS: Chatbot Arena — Benchmarking LLMs in the Wild with Elo Ratings](https://www.lmsys.org/blog/2023-05-03-arena/)
- [OpenAI Evals (GitHub)](https://github.com/openai/evals)
- [Promptfoo: LLM-as-a-judge evaluation guide](https://www.promptfoo.dev/docs/guides/llm-as-a-judge/)
- [Microsoft BIPIA: indirect prompt injection benchmark (GitHub)](https://github.com/microsoft/BIPIA)
- [Redis: Context rot explained (& how to prevent it)](https://redis.io/blog/context-rot/)
- Statistical power in LLM evals: [tianpan.co — Your LLM Eval Is Lying to You](https://tianpan.co/blog/2026/04/15/statistical-power-llm-evals), [dev.to — Eval Set Sizing](https://dev.to/gabrielanhaia/eval-set-sizing-the-statistical-power-math-behind-llm-ab-tests-4gpc)
- Pairwise vs. pointwise reliability: [arXiv:2504.14716 — Pairwise or Pointwise? Evaluating Feedback Protocols for Bias in LLM-Based Evaluation](https://arxiv.org/abs/2504.14716)
- RAG/citation metrics: [FutureAGI — RAG Evaluation Metrics 2026](https://futureagi.com/blog/rag-evaluation-metrics-2025/), [Deepchecks — Top RAG Metrics](https://deepchecks.com/top-rag-metrics-for-enhanced-performance/)

## Open questions for the Q&A pass (not answered here)

1. What's the actual objective statement for Polaris's assistant persona — is `prompt.md`'s
   current implicit constitution (grounding > helpfulness-via-speed, "know when to stop") the
   right one to formalize and hold constant, or does it need revisiting first?
2. Hard token ceiling for the fixed prompt+tools floor, or just "trend it down, no fixed number"?
3. Which categories from Part 3's menu matter most for a first pass — everything, or a narrower
   start?
4. Judge model assignments per subject model (family-diversity constraint from 2d) across the
   6-model roster.
5. Where the harness lives (reuse `benchmark.Suite` vs. a new sibling) and whether it's a new
   `cmd/` subcommand.
6. How much manual error-analysis time (2a) to spend reading real transcripts before locking the
   first 100-case set, vs. how much to backfill later as real failures surface.
