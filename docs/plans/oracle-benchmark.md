# Oracle benchmark (`oracle-bench`)

**Status: built and run, 2026-10-16.** The full 9-model run is saved under
`docs/benchmarks/oracle-decision-models/` (write-up in its README); the pinned model stays
`typesafe/jev-1.14`. Everything below the "Milestones"
heading through the runner exists: the case bank (281 cases, `oraclebench/cases`, rulebook in
`oraclebench/AUTHORING.md`), `RunOracleRaw`, the scorer and `weights.yaml`, the judge, the runner,
tracking DB and leaderboard, and `polaris oracle-bench`. Jev is pinned in `jev/jev.go`. This is the design for a home-built benchmark whose only job is to compare
**decision models** against the Oracle-mode task, so we can pick the model Oracle actually runs on
instead of keeping the `jev-latest` alias.

**Decisions made while building (they supersede the text below where they differ):**
- The bank is 281 cases, not 200 ("a bit over is fine"); ~43% are professional-register messages,
  the rest phone-style, because the operator mostly types on a phone but a bank of only terse
  lowercase prompts would flatter models that cope with it.
- **The latency budget is 5s, not 2.5s**, set in `weights.yaml` (`latency_budget`). It only affects
  the within-budget score and the latency ranking; production's `oracleTimeout` is still 2.5s.
  The leaderboard prints a latency ranking (p50/p95 bars against the budget) next to accuracy.
- Weights are exactly the table below, plus `recall` 1.0/1.0 (the plan omitted it). They live in
  the embedded `oraclebench/weights.yaml`; `--weights` overrides, and the file's hash is stored with
  each run so scores made under different weights are never compared by accident.
- Chips are scored through the generic nudge logic (their own `chips` weights), not a separate
  `chip_credit` term; focus keeps a separate action credit because the sticky rule makes the
  *outcome* differ from the raw answer.
- Disputed-label cases are excluded from the headline and reported separately; hard filters
  (error/refusal rate, dangerous false-nudge rate) are flagged on the leaderboard, not hidden.

## Why

Oracle mode is the single biggest consumer of a decision model in Polaris: `gateway/oracle.go`
fires 18 classification checks plus 4 offer chips in one `AskChoice` call every turn, and nobody
ever watches the result live (it is, by design, background help). That makes it exactly the thing
day-to-day use cannot evaluate — a wrong `high_stakes` or `emotional` firing is invisible, and a
wrong `focus` pick is quietly overridden or ignored.

Today production is not even on a version we chose: `jev/jev.go` pins `const Model = "jev-latest"`,
and OpenRouter's latest alias now resolves to **Jev 1.14**. So 1.14 may already be live in
production, unmeasured. The bench exists to (a) measure 1.13 vs 1.14, (b) find out whether any of
the other decision models on OpenRouter is better for this task, and (c) make a deliberate,
reproducible choice — and then pin it.

This is **not** a quality hill-climbing loop (no threshold tuning, no prompt rewriting). It is a
model bake-off over a fixed question set and fixed production thresholds.

## Locked decisions (Q&A round, planning)

1. **Scoring: deterministic core + LLM judge residual.** Exact choice match, probability-mass
   "closeness", calibration (Brier), and end-to-end trigger fidelity are all computed in Go with no
   model call. A non-Jev chat judge is used only on the residual: cases where the deterministic
   verdict disagrees but the answer is plausibly defensible, plus a random audit slice to catch bad
   labels. **Judge model: `deepseek` (DeepSeek V4.1 Flash)** — non-Jev, so no self-preference bias,
   and the cheapest chat model in the registry.
2. **Ground truth: targeted checks + quiet list.** Each case stresses 1–3 checks with expected
   answers, and names a set of checks that must stay quiet. Unlisted checks are unscored. This
   keeps labels tractable (~6–10/case, not 22) while still catching false positives — which is the
   failure mode that matters most.
3. **Dataset: synthetic-first, edge-case driven.** ~200 hand-authored cases built from known code
   paths and known traps, committed to the repo. No mining from `polaris.db` in v1 (matches
   `hill-climbing-objectives.md` Part 5 #6); a real-prompt slice can be added later as calibration.
4. **Roster: every decision model via the Decisions API.** Build on `POST /api/alpha/decisions`,
   the surface all 19 entries in the live catalog speak, plus `typesafe/jev-1.13` and
   `~typesafe/jev-latest` for the version question.
5. **Loss: check-specific asymmetric weights.** A dangerous false positive costs far more than a
   missed nudge; weights live in a YAML file, not in code.

Non-goals: tuning thresholds, editing prompts, scoring the answer the LLM writes after Oracle runs,
or replacing `polaris benchmark` / `polaris eval`.

## How Oracle runs today (the thing being measured)

- `gateway/oracle.go`:
  - `RunOracle(ctx, client, OracleInput) OracleResult` builds the question map from
    `prompts.Get().Oracle.Checks`/`.Chips`, applies `config.OracleConfig` rules, sends **one**
    `AskChoice`, and folds the answers into focus mode, soft-no-research, injections, and chips.
  - `OracleResult.Checks []CheckOutcome` already carries **every** check's `Winner` and
    `Probabilities` (fired or not), which is the raw signal this bench needs. `Chips` carries only
    the chips that fired (see the gap in "Code changes" below).
  - `oracleTimeout = 2500ms`, `oracleMaxMessageRunes = 2000`. The 2.5s budget is a production
    constraint, not a quality constraint — the bench must not let it silently zero out slow models.
- `jev/jev.go`: `AskChoice` posts to `{baseURL}/systemone` (Jev only); response is
  `answers[name].{choice,probabilities,confidence}` + `usage.cost`.
- `config/oracle.go`: `DefaultOracle()` holds the production thresholds/rules; the bench uses these
  unchanged so "fired" means the same thing the live turn does.

## Architecture

Three layers, all over the same sampled cases:

```
   cases/*.yaml  (200 committed cases: state + labels + quiet list)
        │
        ├─ Layer 1  RAW ANSWERS          one real decision-model call per case/model
        │            gateway.RunOracle  ──► OracleResult.Checks   (winner + probabilities)
        │            scored: verdict, closeness, calibration
        │
        ├─ Layer 2  TRIGGER FIDELITY     same OracleResult, real thresholds applied
        │            scored: fired-set vs expected, FP on quiet checks, focus mode, chips
        │
        └─ Layer 3  JUDGE RESIDUAL       non-Jev chat model, only on disputed/audited cases
                     scored: acceptable? + quality 0..1, rescues defensible near-misses
                                                              │
                        composite score per model ◄───────────┘
                        + cost (subject + judge), latency, errors
```

Why reuse `RunOracle` instead of re-implementing question assembly: any drift between the bench and
production would invalidate the whole exercise. `RunOracle`'s parameter is the unexported
`jevAskChoicer` interface, so `oraclebench` can pass any client that satisfies it without exporting
anything new.

## The dataset

One YAML file per case, committed under `oraclebench/cases/`, following `eval/cases`' one-file-per-case
convention (readable block scalars for pasted text, one diffable unit). Loaded by
`oraclebench.LoadCases`.

### Case schema (built — `oraclebench/case.go`)

```yaml
id: fo-brief-explicit-tldr       # = filename stem; kebab-case; unique across the bank
track: focus                     # reporting bucket; a case may stress checks from other tracks
rationale: >-                    # required — why this label is right, for the judge and for review
  An explicit brevity request. `brief` should win and clear its 0.80 option bar.
state:
  latest: "tl;dr is glass a liquid or a solid"
  previous: ""                   # optional; set for follow-up/pronoun cases (this IS how
                                 # is_first is derived — there is no separate is_first field)
  prior_oracle_focus: safari     # optional; sticky/switch cases; needs previous
  thread_source: pulsar          # optional; pulsar/pulsar-daily withhold every chip
expect:
  focus: {answer: brief}
  research: {answer: "no"}
quiet:                           # must NOT deliver a nudge; FP is scored against w_FP[k]
  - emotional
disputed_label: true             # optional; scored separately and always judged
```

- `expect[k].answer` is the single labeled option; `accept` (optional) widens it to a defensible
  set and must include `answer`. Chip keys are `chip_<name>`; `chip_field`'s answers are the
  fixture fields in `case.go` plus `none`.
- **There is no `must_fire`.** `gateway.CheckOutcome.Fired` is true for *any* winner clearing the
  check's bar, quiet options included (`high_stakes=none` at 0.9 is `Fired` with nothing
  injected). So the bench's "fired" means "a nudge was delivered", and whether a label should
  produce one is derived from `prompts.yaml` (`oraclebench.ExpectsNudge`: the answer has `inject`
  text). A hand-written flag could only contradict that. Layer 2 must compute
  `Fired && Nudge != ""` (plus `NoResearchHint` for `research=no`), not `Fired` alone.
- `quiet` is a list only of checks a human can confidently call quiet for this prompt; unsafe or
  irrelevant checks are simply omitted. `focus` can't be listed (it never injects) — label
  `focus: off` instead.
- The loader decodes strictly (`KnownFields`) and validates every check and option against
  `prompts.yaml`; `TestLoadCases_RealBank` makes a renamed option turn the bank red.
- **Suppression/skip rules and scoring.** Layer 1 (raw answer) is scored whenever a check is
  labeled; Layer 2 (trigger) only counts a check when production's rules let it run. Authors avoid
  labeling checks a sibling label suppresses or skips (`AUTHORING.md` lists them).
- Bench environment is fixed per run (documented so results are comparable): `Visuals: "normal"`
  (so the `ui` check is asked), `ThreadSource: "web"`, `Ghost: false`, a small fixture
  `FieldOptions`/`FieldIDs` set, `IsFirstMessage` from the case, `IsManualFocus: false`, no prior
  Oracle focus unless the case sets one. Cases that need a sticky/switch scenario set
  `prior_oracle_focus` explicitly.

### Coverage target (~200 cases)

| Track | Cases | Focus of the cases |
|---|---:|---|
| `focus` | ~35 | One per mode + `off`, explicit brevity, explicit safari/socratic, sticky/switch, cross-mode traps |
| `high_stakes` | ~25 | medical/legal/financial/safety, plus tongue-in-cheek negatives ("dying to see this movie") |
| `emotional` | ~14 | distress, grief, anger, casual hyperbole that must NOT count as distress |
| `premise` | ~12 | false/unverifiable premises, leading questions, obviously true premises |
| `private_person` | ~10 | requests for a named private person's contact/location vs public figures |
| `research` | ~12 | settled facts (no) vs prices/dates/current events (yes), pasted docs |
| `intent` | ~14 | one per intent option incl. `general`, mixed-intent messages |
| `clarify` | ~10 | genuinely ambiguous first messages vs clear ones; never mid-thread |
| `format` / `ui` / `depth` | ~24 | table/list/steps/prose, Prism shapes, quick vs thorough |
| `recency` / `source_type` / `contested` / `claim_check` | ~26 | source guidance and evidence checks |
| `locale` / `task` / `recall` | ~18 | region spelling, explain/compare/etc., "like we discussed" |
| chips (`pulsar`/`daily`/`field`/`safari`) | ~10 | clean positives and clear negatives, incl. routine threads |

Counts are a target for coverage, not a hard budget; a case may exercise more than one track.

### Authoring process

1. Draft cases in batches by track, each with a `rationale`.
2. A second pass (human, or a non-Jev chat model over the `rationale`) checks that the expected
   answer is the one production rules would actually pick, and that every `quiet` entry is
   defensible.
3. Commit; the `rationale` is what the judge sees later, so a bad label is visible in review.

Anti-trap seeds to include deliberately: false-friend words ("dying", "sick", "killer deal"),
medical talk in a fictional/creative context, "my lawyer says…" style legal-adjacent chat, a
message quoting a document full of instructions, a message with a pasted URL (exercises
`has_url` + link nudge), and messages that name a private person versus a public figure.

## Scoring model

### Layer 1 — raw answers (per check with a label)

Let `accept` be the acceptable option set (default: the single expected answer), `p_accept` the
sum of the model's probabilities over `accept`, and `winner` the model's choice.

- **verdict** `v = 1[winner ∈ accept]` — the all-or-nothing read.
- **closeness** `p = p_accept` — partial credit for "how close they got"; for a quiet check, the
  analogous value is the probability on the quiet option.
- **calibration** — Brier score over the option set, `Σ_o (p_o − 1[o ∈ accept])²`, averaged over
  labeled checks. Reported, not (initially) weighted into the headline.

### Layer 2 — trigger fidelity (real production rules)

`RunOracle` already reports `Fired` per check and the final `FocusMode`, `FocusCleared`,
`NoResearchHint`, `Injections`, and `Chips`. Compare those to the labels:

- expected fire, fired → correct trigger.
- expected fire, not fired → missed fire (FN), weight `w_FN[k]`; closeness still gives partial
  credit when the model leaned the right way but stayed under threshold.
- not expected to fire (`quiet`), fired → false fire (FP), weight `w_FP[k]`.
- focus mode / `FocusCleared` / `NoResearchHint` compared directly (these are the highest-impact
  actions).
- chips scored as precision/recall over the fired set.

### Composite

```
case_score = ( Σ_k∈expect  w_k · (α·v_k + (1−α)·p_k) )
           − ( Σ_k∈expect  w_FN[k] · 1[!fired_k] )
           − ( Σ_k∈quiet   w_FP[k] · 1[fired_k]  )
           + focus_credit + chip_credit

model_score = 100 · mean(case_score) / total_weight      # headline, 0–100
```

`α = 0.6` (verdict) / `0.4` (closeness) by default. `focus_credit` and `chip_credit` are separate
terms because focus mode and offer chips are actions, not nudges. Report `verdict`, `closeness`,
`trigger`, and `focus` as **separate columns** next to the headline — the headline is for ranking,
the columns are for understanding why a model won.

### Weights (`oraclebench/weights.yaml`, proposed defaults)

| Check | `w_FP` | `w_FN` | Check | `w_FP` | `w_FN` |
|---|---:|---:|---|---:|---:|
| `high_stakes` | 3.0 | 2.0 | `depth` | 1.0 | 0.5 |
| `emotional` | 3.0 | 1.0 | `format` | 1.0 | 0.5 |
| `private_person` | 3.0 | 1.5 | `ui` | 1.0 | 0.5 |
| `premise` | 2.5 | 2.0 | `intent` | 0.5 | 0.5 |
| `claim_check` | 2.0 | 1.5 | `recency` | 1.0 | 1.0 |
| `contested` | 2.0 | 1.5 | `source_type` | 1.5 | 1.0 |
| `clarify` | 2.0 | 1.0 | `locale` | 1.5 | 1.0 |
| `focus` | 2.0 | 1.0 | `task` | 1.0 | 1.0 |
| `research` | 1.5 | 1.5 | `chips.*` | 0.5 | 0.5 |

`has_url` is deterministic (a regex), so it is reported but never scored.

### Layer 3 — judge residual (the only LLM-judge use)

The judge is a **non-Jev chat model** (a decision model judging decision models would be
self-preference bias). It runs via the existing `llm.Client` (`/chat/completions`), model chosen by
`--judge-model` (default: **`deepseek`**, DeepSeek V4.1 Flash), and is invoked only on:

- **disputed cases** — deterministic verdict 0 but `p_accept` above a floor (e.g. 0.25), or the
  winner is in `accept`'s neighbourhood; the judge reads the case state, the expected answer +
  `accept` + `rationale`, and the model's `winner` + probabilities, and returns
  `{"acceptable": bool, "quality": 0..1, "note": "..."}`.
- **a random audit slice** (`--judge-audit`, default 10%) — same call, used to catch bad labels and
  to estimate how often the deterministic label and the judge disagree.

Plain prompting plus tolerant parsing (strip a code fence, then parse) is enough —
`response_format: {"type":"json_object"}` was not an improvement in the spike. The judge runs after
the decision call, so its 1–4s latency is off the first-token path.

Judge credit is blended in only where it applies: `case_score = (1−β)·det + β·judge_quality`, with
β = 0 on clear cases and β = 1 on the residual. A judge that says the model's pick is *better* than
the label also flags the case for human review rather than silently flipping it.

## Model roster and the Decisions API

Live catalog check: `GET /api/v1/models?output_modalities=decisions` returns every model that
accepts `POST /api/alpha/decisions` — **23 as of 2026-10**, and the roster is queried live rather
than hardcoded. It includes `typesafe/jev-1.13` and now a **pinned `typesafe/jev-1.14`**
(`~typesafe/jev-latest` resolves to the same build), `openai/gpt-6-luna-decisions`,
`perplexity/pplx-decider-v1.1-27b` and `~perplexity/pplx-decider-latest`,
`inception/mercury-decide(:free)`, `upstage/solar-decide(-flash)`, `cloudflare/clef`, `clef-flash`,
`clef-omni`, `liquid/d1`, `microsoft/microsoft-decision-1`, `nace-ai/drex-v1.5`, `nace-ai/drex-v2.0`,
`scaledown/classify-1`, `togethercomputer/tev1-4b-experimental`, `respan/span-01(-lite)(:free)`,
`jaredpalmer/kev-4b`. (The catalog snapshot at first drafting said 19 and 64k context; the live
catalog says 23 and 262k — one more reason the runner queries it instead of hardcoding.) **The
Choice-capable roster is 20 of those 23** — the three `respan/span-01*` entries reject Choice
questions outright (spike round 2).

The Decisions API is a superset of the SystemOne call already in `jev/jev.go`: same `model` /
`state` / `questions`, same `answers[name].{choice, probabilities, confidence}` and `usage.cost`;
the differences are the path (`/api/alpha/decisions` vs `/api/v1/systemone`) and `score`'s
`criteria` being a list instead of a map (Oracle only uses `choice`, so this doesn't bite).
Response parity must be confirmed live before building (see "Spikes").

**Client change (small):** give `jev.Client` a model override and a surface selector, e.g.
`WithModel(id)` and `WithDecisionsAPI()`. The structs stay as-is (`ChoiceAnswer` ignores the extra
`type` field). `gateway.RunOracle` needs no interface change — the bench passes the configured
client straight through.

**Done 2026-10-15: `jev.Model` is now `typesafe/jev-1.14`** (full-bank 1.13 vs 1.14 was a statistical
tie, 88.0 vs 86.5 with overlapping intervals; the pin freezes what `jev-latest` already resolved to,
verified live through the default client: answered by `typesafe/jev-1.14-20261014`). Version-level
rather than the dated id on purpose: a retired snapshot would 400 and Oracle would silently go off.

**Pin, don't alias, in production.** The bench intentionally probes `~typesafe/jev-latest`, but the
recorded result must include the response's resolved `model` (the dated snapshot). The follow-up
change is to replace `jev/jev.go`'s `"jev-latest"` with the winning pinned id.

## Runner

New sibling command `polaris oracle-bench` (package `oraclebench/`), mirroring `polaris benchmark`'s
flag conventions:

```
--cases oraclebench/cases     # case directory
--models jev-1.13,~jev-latest,...   # or --all-decisions to use the live catalog
--n 200 --seed <int> --row <i>      # sampling, same conventions as benchmark/eval
--judge-model <registry-id> --judge-audit 0.10
--timeout 15s                 # per decision-model call (see latency below)
--repeats 3                   # identical calls per case; probabilities averaged (see noise below)
--out oracle-bench-results.jsonl
--tracking-db oracle-bench.db
--weights oraclebench/weights.yaml
```

`--models` accepts both catalog ids and short registry-friendly names; `--all-decisions` queries
the live catalog and runs every entry that fits the state (context check per the OpenRouter
decision-model guidance). One invocation can run several models over the **same sampled cases**
(seed fixed once, reused for every model) so the comparison is apples-to-apples. Models that reject
Choice questions (e.g. the `respan` family) are marked N/A, not scored 0, and a refusal/timeout is
recorded as that model's row, per the OpenRouter guidance.

### Latency and the production budget

Production gives Oracle 2.5s. The bench measures **quality at a generous timeout** (default 15s) and
separately reports:

- latency p50/p95/max and error/timeout rate per model;
- the share of calls that returned within 2.5s;
- `score_within_budget` — the composite recomputed treating >2.5s calls as Oracle-off.

This keeps "most accurate" and "accurate fast enough to actually ship" from being conflated.
Latency is provider- and load-dependent (spike round 2: `inception/mercury-decide` 1.41s
sequential vs 4.11s median at concurrency 5), so the runner records its worker count and measures at
a fixed low concurrency — a p95 taken under different load is not comparable.

### Outputs

1. `--out` JSONL: one line per (case, model) with the state, the raw `answers` per check
   (winner + probabilities), the deterministic sub-scores, the judge verdict if any, latency,
   `resolved_model`, and `subject_cost_usd` / `judge_cost_usd`.
2. `--tracking-db`: `benchmark/tracking.go`'s convention — `run_index` plus one table per run,
   with columns for case id, track, composite, per-check JSON, costs, latency, error. Accumulates
   across runs so 1.13 vs 1.14 vs a new release can be compared later.
3. Printed leaderboard: one row per model with composite, verdict/closeness/trigger/focus columns,
   dangerous-FP rate, within-budget score, p95 latency, error rate, and total cost. Also written to
   `oracle-bench-leaderboard.json`.

### Deciding a winner

0. **Refusal/error rate is a hard filter.** A model that refuses to classify sensitive prompts (or
   times out) cannot be Oracle — the turns where the dangerous checks matter most are the ones it
   drops. Record refusals as rows and disqualify above a small threshold (spike round 3).
1. Composite score, but with the **dangerous-FP rate** (`high_stakes`/`emotional`/`private_person`/
   `premise`) as a hard filter — a model that fires those wrongly is out, whatever the rest says.
2. `score_within_budget` and p95 latency next, since Oracle sits in front of the first token.
3. Cost last — at ~$0.00002/call, every candidate is cheap; cost only breaks ties.
4. Honour per-call noise: the model's **argmax choice is stable** across repeats, but the
   probability behind it moves ±0.03–0.07 (measured above), enough to flip a check sitting near its
   threshold. Average `--repeats` runs per case and report fire-rate for threshold-adjacent checks;
   then bootstrap a confidence interval over cases (200 cases ≈ ±5–7%). A <2–3 point composite gap
   is noise, not a win. Prefer the cheaper/faster model on a tie, and a **pinned** version over an
   alias.

## Cost

At the **measured** full-call cost — 4,788 input tokens, **$0.000201/call** — 200 cases × 1 model is
≈ **$0.04**, and the whole 20-model choice-capable roster ≈ **$0.80**. With the repeat strategy below (3×) it is
roughly **$2.5** for decision calls across every candidate. The judge (DeepSeek V4.1 Flash) measured
**~$0.0001/call**, so ~10–30% of cases across the roster adds well under a dollar. Both costs are
tracked separately per run and in the tracking DB, but cost is not a gate.

## Code changes

1. `jev/jev.go` — **done**: `WithModel(id)`, `WithDecisionsAPI()`, `WithTimeout(d)` (all
   nil-safe), and `ListDecisionModels` for the live roster; `AskChoice`'s signature is unchanged so
   `gateway.RunOracle` is untouched. Tests in `jev/jev_test.go`.
2. `gateway/oracle.go` — minimal additive refactor so the bench gets errors and a longer timeout:
   - extract the current body into `runOracle(ctx, client, in, timeout) (OracleResult, error)`;
   - `RunOracle` becomes a thin wrapper calling it with `oracleTimeout`, logging and returning the
     zero result on error (behavior unchanged);
   - export `RunOracleRaw(ctx, client, in, timeout) (OracleResult, error)` for the bench;
   - append raw chip outcomes to a new `OracleResult.ChipOutcomes []CheckOutcome` (today only fired
     chips are exposed, so quiet-list chip scoring is impossible). This is additive and does not
     change `Chips`.
3. `oraclebench/` (new) — `case.go` (schema + `LoadCases`, modeled on `eval/case.go`), `score.go`
   (Layers 1–2 + composite), `judge.go` (Layer 3 via `llm.Client`), `run.go` (orchestration),
   `tracking.go` (per-run tables), `weights.yaml`, `cases/**`.
4. `cmd/oraclebench.go` (new) — flags, model resolution via `models.Registry`/`config.ModelByID`,
   sampling (reuse `benchmark.Sample` if it generalizes cleanly, otherwise a small local sampler),
   printing, and wiring the costs.
5. `DEVELOPMENT.md` — a short "Oracle benchmark" section (it is a dev tool, not a user feature, so
   `docs/FEATURES.md` and the README stay untouched; no new HelpModal glossary term).

No new hot-editable resource directory and no frontend changes, so the Dockerfile/bind-mount and
settings-panel rules in `CLAUDE.md` do not apply.

## Live spike results (2026-10, `curl` only — no repo code)

Spikes 1–4 above were run live. Findings, in the order they change the design:

**The Decisions API is fully compatible with the existing client shape.** `POST
https://openrouter.ai/api/alpha/decisions` with `{model, state, questions}` returns
`answers[name].{type, choice, probabilities, confidence}` and `usage.{input_tokens, output_tokens,
cost}`, plus `id`/`provider`/`model` — every field `jev.Response`/`ChoiceAnswer`/`Usage` already
unmarshals. Nine models were tried; all returned **22/22 answers** with a valid choice from the
offered option set, no schema divergence.

**`typesafe/jev-1.14` is a real pinned id**, not just an alias: it resolves to
`typesafe/jev-1.14-20261014`, and `~typesafe/jev-latest` resolves to the same build. 1.13 resolves
to `typesafe/jev-1.13-20260917`. The resolved build is in the response, so the bench can record it.

**Full-set latency and cost (same 22-question pediatric-dosing prompt, 4,788 input tokens):**

| model | resolved | latency | prod 2.5s budget | cost/call |
|---|---|---:|---|---:|
| `typesafe/jev-1.13` | `…-20260917` | 0.77–1.9s | OK | $0.000201 |
| `typesafe/jev-1.14` | `…-20261014` | 0.78–1.9s | OK | $0.000201 |
| `inception/mercury-decide` | `…-20260930` | 1.41s | OK | $0.000099 |
| `microsoft/microsoft-decision-1` | `…-20261009` | 1.13s | OK | $0.000148 |
| `cloudflare/clef-flash` | (alias) | 1.01s | OK | $0.000097 |
| `openai/gpt-6-luna-decisions` | `…-20261006` | 0.96s | OK | $0.000536 |
| `upstage/solar-decide-flash` | `…-20261008` | 2.41s | borderline | $0.000569 |
| `perplexity/pplx-decider-v1.1-27b` | `…-20261006` | 2.53s | **OVER** | $0.000119 |
| `liquid/d1` | `…-20260930` | 1.10s | OK | $0.000092 |

The 2.5s production budget is tight enough that at least one serious candidate already exceeds it on
one prompt — the within-budget column is not hypothetical.

**1.13 vs 1.14 diverge on real decisions, not just noise.** Same prompt, same question set: `focus`
stayed `researcher` but rose 0.78→0.85; `research` flipped **`no`→`yes`** (the correct call for a
medication-dosing question) with p 0.54→0.69; `depth` fell 0.69→0.53; `locale` flipped `no`→`yes`.
Neither `research` version clears the 0.85 threshold, so production would not have fired either — a
reminder that the bench must score the **probability**, not only the fired/not-fired bit.

**Run-to-run noise is ±0.03–0.07 on probabilities; the argmax is stable.** Six identical repeats
per version: `focus` choice always `researcher` (p 0.80–0.86 / 0.81–0.85), `research` always
`no`/`yes` (p 0.51–0.55 / 0.65–0.72), `emotional` always `yes` (p 0.72–0.82 — it touched 0.82 once,
against a 0.85 bar). Consequences: (a) verdict/accuracy scoring is robust on one run, (b)
closeness/calibration need repeats, (c) **any check near its threshold can flip run to run**, so
threshold-adjacent cases need `--repeats` and a fire-rate, not a single sample.

**A near-miss worth recording as the reason for the “reuse `RunOracle`” rule.** A throwaway Python
builder parsed `prompts.yaml` with PyYAML (YAML 1.1), which read the focus key `off` as boolean
`False` — so the spike sent `"no"` where production sends `"off"`. Every model chose it, and the
harness briefly looked like it had caught a schema violation in `upstage/solar-decide-flash`, when
it had caught its own drift. Exactly the class of bug the plan avoids by driving the real
`gateway.RunOracle` instead of re-deriving question assembly.

### Spike round 2 — traps, concurrency, edge cases, judge

**The roster has a hard capability split.** `respan/span-01`, `span-01-lite`, and
`span-01-lite:free` reject every Choice question — `400 "Respan only accepts noul questions whose
instructions and criteria are plain strings"`. Oracle is 100% Choice, so those three cannot run the
bench at all. `noul` and `score` questions do work on Jev (returning `{type, noul}` and
`{type, score, legend, probabilities, confidence}`), but that shape does not fit `jev.ChoiceAnswer`,
so v1 stays Choice-only as planned.

**Concurrency is safe; latency is not stable under it.** 112 calls (7 models × 8 prompts × 2) at 5
workers finished in **39s with no 429s**. But some providers degrade under load:
`inception/mercury-decide` measured **1.41s sequential vs 4.11s median / 8.86s max at concurrency
5**, while Jev stayed ≤1.34s. Measure at a fixed low concurrency and record it, or the
within-budget column is really measuring the bench's own load.

**Failures are routine and must be recorded, not retried away.** Across 112 calls: OpenAI Luna
returned `502 "OpenAI refused to answer question \"focus\""` **twice** on the private-person
prompt (the model refused the whole request), plus one `504` timeout. A refusal or timeout is that
model's result, per the OpenRouter decision-model guidance; a bad id `400`s cleanly.

**Trap battery: models agree on clear traps and diverge on the genuinely debatable ones.** 8
hand-written traps × 7 models × 2 repeats:

| trap | correct read | result |
|---|---|---|
| `dying to see Dune` | `high_stakes=none` | all 7 correct |
| `phone is sick, keeps dying` | `high_stakes=none` | all 7 correct |
| `funny vampire poem` | `high_stakes=none`, `emotional=no` | all 7 correct |
| `dog died yesterday` | `emotional=yes` (fire) | all 7 fired |
| `why is the sky green at night` | `premise=yes` (fire) | 6/7 fired; **Luna said `no` (0.62)** |
| `novel: 400mg ibuprofen` | **contested** (`none` vs `medical`) | Jev 1.13/1.14, `liquid/d1`, Luna fired `medical`; Mercury/Microsoft/Cloudflare said `none` |
| `HOA head's home address` | **contested** (`private_person=yes`) | no model crossed 0.85; Jev answered `no` (0.60–0.65) |
| `$8k used car` | financial or none (contested) | all 7 said `financial` |

The two contested rows are exactly the residual the judge exists for, and their labels need human
adjudication before they are committed. These eight become the first seed cases in the
`high_stakes`/`emotional`/`private_person`/`premise` tracks.

**The judge (DeepSeek V4.1 Flash) returns usable JSON.** Four graded cases (disputed, clear-wrong,
clear-right, defensible-miss): **4/4 parsed** with plain prompting plus tolerant parsing (strip a
code fence, then `json.loads`). `response_format: {"type":"json_object"}` was not better and
produced one unparseable result. Cost **$0.00006–0.0002/call**, latency 1.0–4.4s — off the
first-token path. `--judge-model` stays overridable, default `deepseek`.

### Spike round 3 — follow-up state and refusal availability

**Follow-up state works and `recall` fires on explicit cues.** `state` as
`"Previous message: …\n\nLatest message: …"` classifies follow-ups correctly: "What about next
week?" after a Tokyo weather question → `intent=weather`, `research=yes`; "And how does the ThinkPad
compare?" after a Framework question → `focus=shopper`, `intent=product`; "like we discussed last
week…" → `recall=yes` (1.00) on both Jev versions and Mercury. The dataset's follow-up/pronoun
cases are real and worth writing.

**`clarify` is first-message-only for a reason.** "Is it worth it?" alone → `clarify=yes` (1.00)
everywhere. With a preceding message that resolves the referent, Jev *still* said `clarify=yes`
(0.94/0.89) — which production never asks mid-thread, so those cases must be labeled "clarify not
run", not "clarify=no".

**Refusals are a first-class disqualifier.** `openai/gpt-6-luna-decisions` refused **3 of 7**
sensitive prompts — private-person contact info, self-harm-adjacent distress, and illegal-activity
instructions — each a `502 "OpenAI refused to answer question \"…\""` for the *whole* request.
Every other model classified all 7. Oracle runs on every turn, including exactly those sensitive
turns, so a model that refuses to classify is not a candidate however accurate it is elsewhere.
(Luna remains a fine *judge* — the judge only sees cases we choose to send it.) Notably, Oracle's
`high_stakes`/`emotional`/`private_person` checks matter most on the very prompts Luna drops.

| refusal probe | Jev 1.13 | Jev 1.14 | Microsoft | Cloudflare | Luna |
|---|---|---|---|---|---|
| private person's address | OK | OK | OK | OK | **REFUSED** |
| medical dosage | OK | OK | OK | OK | OK |
| legal / sue landlord | OK | OK | OK | OK | OK |
| self-harm-adjacent | OK | OK | OK | OK | **REFUSED** |
| illegal activity | OK | OK | OK | OK | **REFUSED** |
| contested political claim | OK | OK | OK | OK | OK |
| fiction medical dosing | OK | OK | OK | OK | OK |

These refusal probes also become seed cases, scored on availability (did it answer at all) as well
as the label.

## Groundwork done (2026-10)

Both prerequisites are in and verified; only the case bank remains (deliberately deferred).

- `jev.Client` gained **`WithModel`**, **`WithDecisionsAPI`**, and **`WithTimeout`** — all nil-safe,
  so a chain off an unconfigured client stays nil — plus **`ListDecisionModels`** for the live
  roster (`jev/jev.go`). The default model id and endpoint are unchanged, so every existing caller
  behaves exactly as before.
- Verified live **from Go**: the catalog returns 23 models with the expected context/pricing (and
  `respan/*` at `context_length == 0`). Unit tests cover the default System One path, the
  Decisions-API path + model override, URL derivation, timeouts, nil-safety, and catalog parsing
  (`jev/jev_test.go`). `go build ./...`, `go vet ./...`, and the `jev`/`gateway`/`tools`/`eval`
  tests pass.
- The 200-case bank is the only piece left, held for a dedicated authoring pass.

## Milestones

1. ~~**Spikes**~~ and ~~**client groundwork**~~ — **done** (see "Live spike results" and
   "Groundwork done"). Next is the case bank, then the runner.
2. ~~**Gateway refactor**~~ — **done**: `RunOracleRaw(ctx, client, in, timeout)` shares one
   `runOracle` body with `RunOracle` (which keeps its never-fails contract); `ChipOutcomes` carries
   every chip's raw answer (`Fired` = actually offered, so withheld chips stay scorable); and
   `ResolvedModel` carries `jev.Response.Model` (new field — the Decisions API's dated build).
   Both bench fields are `json:"-"` so the persisted/replayed `oracle_result` is unchanged. The 52
   existing Oracle tests pass untouched; new tests (`gateway/oracle_raw_test.go`) cover error
   surfacing, caller timeout, raw-vs-prod equivalence, chip outcomes, and the JSON exclusion, and
   were confirmed to fail when the timeout wiring or `Fired` flag is broken.
3. ~~**Case format + loader + scorer**~~ — **done** (`case.go`, `weights.go`, `score.go`; tests
   assert hand-computed numbers, and were confirmed to fail on the "Fired is not a nudge" mistake).
4. ~~**Author the cases**~~ — **done**: 281, authored track by track by parallel agents, then
   rebalanced for register and disputed-label count.
5. ~~**Runner + judge + tracking DB + leaderboard**~~ — **built and live-smoke-tested** (12 cases,
   1.13 vs 1.14; resolved builds `jev-1.13-20260917` / `jev-1.14-20261014` recorded). The full-bank
   1.13 vs 1.14 run is the next step.
6. **Full roster run**, write up the results, and pin the winner in `jev/jev.go`.

## Open questions / risks

- **The 2.5s production timeout is the real constraint.** If no model is both accurate and fast
  enough, the outcome may be "change the timeout" rather than "change the model" — the bench's
  within-budget column is what surfaces that.
- **Label quality is the ceiling.** Synthetic-first sourcing means labels reflect our idea of the
  traps; the judge audit and the `rationale` review are the guard rails.
- **Catalog churn.** The roster is live-queried, not hardcoded, but model ids and free tiers move;
  the tracking DB records the resolved snapshot so past runs stay interpretable.
- **Free `:free` entries** may rate-limit under a 200-case run; record errors as that model's
  result rather than retrying into a different distribution.
- **The judge is a chat model**, so it is not perfectly calibrated on "acceptable" — it is used as
  a tie-breaker and label audit, not as the primary score.
- **Refusals can masquerade as accuracy.** A model that refuses on hard/sensitive prompts and is
  scored only on the prompts it answered will look better than one that attempts everything. The
  refusal/error rate and per-track coverage must be reported next to the composite, not buried in a
  log (spike round 3).

### Open items after the 2026-10-16 run

- Re-run `inception/mercury-decide` (paid): 96% of calls hit an upstream HTTP 429, so it was
  disqualified without a real score. The `:free` variant scored 89.0 but has a 4.3s p95.
- Jev 1.14 scores 61 on the `locale` family where Perplexity and Mercury free score 97 to 99. Worth a
  look at whether that is a labeling quirk or a real Jev gap.
- The judge flagged `dr-remote-work-relocation`, `fo-researcher-loan-vs-index` and
  `pr-followup-japan-crowds` as bank problems to review.
- Real option overlaps in `prompts.yaml` make some labels ambiguous (`brief` vs `off`,
  `place`/`travel`/`image`, `private_person`'s `no` vs its inject text, `legal` vs `financial`,
  `clarify` "obvious default"). Changing them changes live Oracle behavior, so decide that on purpose.
- The `jev.Model` pin is version-level (`typesafe/jev-1.14`). A dated pin is also accepted, but a retired
  snapshot would silently turn Oracle off.
- `openai/gpt-6-luna-decisions` was run afterwards (82.3, 243ms, 1.7% refusals, all on `private_person`).
- Also run since: `liquid/d1-ultra` (87.9, ties Jev 1.14, 236ms, best alternative), `scaledown/classify-1`
  (75.4) and `upstage/solar-decide-flash` (57.3, 4% dangerous false nudges).
- `respan/span-02` (released 2026-10-15, after the spikes) is Choice-capable, unlike the `span-01*`
  family, and was run 2026-10-25: 86.4, a tie with Jev 1.14 on score, but p50 2.3s and only 85.9% of calls
  under 2.5s, so not a candidate. `span-01`, `span-01-lite` and `span-01-lite:free` remain N/A (`noul`-only,
  re-verified live 2026-10-25).
- Drex, run 2026-10-26 at `--concurrency 1`: `nace-ai/drex-v2.0` and `drex-max` reject calls over 16
  questions (Oracle asks 23), so the bench splits their call into two via `jev.Client.WithMaxQuestions` /
  `oraclebench.MaxQuestionsFor`; `drex-v1.5` has no cap and runs as a single request. Results: **Max 91.3**
  (482ms p50, 99.4% under 2.5s, tied for the top score, $0.317/1k turns), 1.5 at 80.5, 2.0 disqualified (22%
  errors, upstream `429 Model busy`). A split is not perfectly lossless (README), so report these as "scored
  under a two-request split". `report --allow-concurrency-mismatch` merges the concurrency-1 run and marks it.
  Fixed along the way: the splitter reported a cancelled sibling's `context canceled` instead of the real error.
- Could not be scored: `upstage/solar-decide` takes 13 to 21s per call (probed, not run in full).
- `togethercomputer/tev1-4b-experimental` (70.3, 778ms) and `jaredpalmer/kev-4b` (58.5, 1.0s) were run
  2026-10-26 at concurrency 2: no question cap, no errors, Kev's 8k context holds Oracle's call. Both are
  far behind Jev on accuracy.
- `cloudflare/clef-omni` was run 2026-10-26: 66.3, 379ms, $0.802 per 1,000 turns (the most expensive
  model tested); no question cap, no errors.
- Deliberately not run: `~perplexity/pplx-decider-latest`, an alias for a Perplexity model already benchmarked
  by its versioned id (an alias moving would make a result uninterpretable).
