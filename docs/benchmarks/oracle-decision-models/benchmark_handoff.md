# Oracle decision-model benchmark: handoff for the full re-run

> **TEMPORARY FILE. Delete it when the benchmark work is finished** (final step, "Clean up", at the
> bottom). It exists only to hand tomorrow's session everything it needs; it is not permanent docs.

Written 2026-10-15 at the end of day one. Read this top to bottom, then do "The plan for the day". The
operator (Autumn) wants the full run done and a showcase document written. Tomorrow is a **big
benchmarking day: the goal is to run a LOT of benchmarks**, not a single comparison. Everything below
was verified today; where something is a judgment call it says so.

## What this is, in one paragraph

`polaris oracle-bench` compares **decision models** (OpenRouter's Decisions API) on the Oracle-mode
task: 22 classification questions asked in one call before every turn. It runs the committed case bank
(`oraclebench/cases`, hand-labeled) through the real `gateway.RunOracleRaw`, scores raw-answer accuracy
plus whether production's rules actually delivered the nudge/action, and prints a leaderboard and a
latency ranking. Design and decisions: `docs/plans/oracle-benchmark.md`. Authoring rules for cases:
`oraclebench/AUTHORING.md`. Weights (tunable, no code change): `oraclebench/weights.yaml`.

## Operator priorities (these decide the winner, not the headline score alone)

1. **Speed is the whole point.** The operator picked Jev because it is fast. A model that scores higher
   but is dramatically slower is not a candidate: today `perplexity/pplx-decider-v1.1-27b` scored
   91.6 vs Jev 1.14's 88.0 but runs 7x slower (p50 2.2s vs 0.29s), and the operator said she would never
   use it. Judge candidates on accuracy **and** latency together; lead the write-up with both.
2. **Production's real Oracle timeout is 2.5s** (`oracleTimeout`, `gateway/oracle.go`). The bench's
   *scoring* budget is 5s (`latency_budget` in `weights.yaml`), set generously on purpose. Always also
   report the share of calls under 2.5s (command below): pplx was only 71%, Jev 99.8%.
3. The models she cares about: **`typesafe/jev-1.14` (currently pinned in production), `typesafe/jev-1.13`,
   `microsoft/microsoft-decision-1`, `inception/mercury-decide`, `liquid/d1`.** She expects Mercury and
   Liquid d1 to be fast. **Tomorrow's roster is 7, possibly 8:** those five, plus
   `perplexity/pplx-decider-v1.1-27b` (showcase only; she'd never use it, it's slow), plus
   **`cloudflare/clef-flash`** (spike: ~1.0s, $0.000097/call), plus optionally one more, e.g.
   `cloudflare/clef` or `upstage/solar-decide-flash` (2.4s in the spike, borderline), if she wants an 8th.
   **Both Jev models and pplx must be RE-RUN**, not reused from today: today's runs used the old
   281-case bank and old weights, so they are not comparable (see "State of the repo").
4. **Excluded:** `openai/gpt-6-luna-decisions` (too expensive, and it refuses sensitive prompts because
   the underlying chat model does). `respan/*` can't take Choice questions (the runner marks them N/A).
   Do not run `--all-decisions` without asking: ~20 models is about $4 and an hour.
5. She wants to be told **what each run costs** every time, and wants progress/ETA when a run is slow.

## State of the repo when you start

- Production Jev is **pinned**: `jev.Model = "typesafe/jev-1.14"` (pushed in `26c08b3`).
- The benchmark system and the case bank are pushed. **`d243f6a` (14 `deep_research` cases + the pplx
  result file) is local and may not be pushed yet. Check `git status -sb`; push only if asked.**
- Case bank: **295 cases** (281 original + 14 `deep_research`), 21 disputed-label cases (reported
  separately, excluded from the headline). Weights hash is shown at the top of every leaderboard; it
  changed after today's runs because `deep_research` was added, so **today's numbers are not comparable
  to tomorrow's.** Today was a cursory baseline.
- Today's saved baselines: `runs/2026-10-15-jev-1.13-vs-1.14.json`, `runs/2026-10-15-pplx-decider-v1.1-27b.json`.
  Reference numbers (old 281-case bank, no `deep_research`): Jev 1.14 = 88.0, Jev 1.13 = 86.5,
  pplx = 91.6; all three had 0 errors and 0 refusals. Use them only as a sanity range.

## Lessons from day one (do not repeat these)

- **Rebase before you run anything.** Origin gained an Oracle `deep_research` check (#176) while we were
  building. `git pull --rebase origin main` first, then build and run `go test ./...`.
- **`prompts.yaml` hot-reloads while a run is in flight.** Pulling or editing it mid-run changes the
  questions for the remaining calls (it silently skipped `deep_research` for the last ~18 pplx calls).
  **Do not pull, checkout, or edit anything in the tree during a run.** Build a binary and run that.
- **"Fired" is not "a nudge was delivered".** `gateway.CheckOutcome.Fired` is true for any winner that
  clears its bar, including quiet options. The scorer uses "delivered" (Fired and nudge text, or for
  `deep_research` the promotion action). Don't "simplify" this; there are tests that fail if you do.
- All models in one invocation run **sequentially, 2 calls in flight**, so every model is measured under
  the same load. Keep `--concurrency 2` for any run whose latency you will report; Mercury went from
  1.4s to 4.1s at concurrency 5 in the spike. Do not run two invocations at the same time.
- Make file changes with the Edit/Write tools (the operator corrected scripted edits before). Commit at
  each stage. Don't push or touch the potato without being asked.

## The plan for the day

### 1. Pre-flight (about 5 minutes, free)

```bash
cd /Users/autumn/Documents/Projects/Polaris
git status -sb && git pull --rebase origin main      # rebase FIRST
go build ./... && go vet ./... && go test ./... 2>&1 | grep -v "no test files"
go build -o /tmp/oracle-bench-bin .                  # run the binary, not `go run`
```

If the pull brought in new Oracle checks or changed `prompts.yaml`'s `oracle:` section, **stop and fix
the bench first**: `TestDefaultWeights` fails when a check has no `weights.yaml` entry, and a new
*action-style* check (one whose "yes" does something rather than injecting text) needs the same
treatment `deep_research` got (`ExpectsNudge`/`nudgeDelivered` in `oraclebench`). Then author a few
cases for it (`oraclebench/AUTHORING.md`). Confirm the model ids exist in the live catalog:

```bash
curl -s "https://openrouter.ai/api/v1/models?output_modalities=decisions" | jq -r '.data[].id' | sort
```

Expected to include `typesafe/jev-1.13`, `typesafe/jev-1.14`, `inception/mercury-decide`,
`liquid/d1`, `microsoft/microsoft-decision-1`, `perplexity/pplx-decider-v1.1-27b`. If an id moved, use
the catalog's current one and say so.

### 2. Smoke test (about 3 minutes, about 2 cents)

Proves every model answers Choice questions and the pipeline works before the long run.

```bash
/tmp/oracle-bench-bin oracle-bench --n 12 --repeats 1 --seed 3 --label smoke --save-dir /tmp/ob-smoke \
  --models typesafe/jev-1.14,inception/mercury-decide,liquid/d1,microsoft/microsoft-decision-1,perplexity/pplx-decider-v1.1-27b,cloudflare/clef-flash
```

`--save-dir /tmp/ob-smoke` keeps this throwaway leaderboard out of the repo; its bulky output still goes
to the gitignored `oraclebench/results/` (delete that directory after the smoke test so the real run
starts clean: `rm -rf oraclebench/results`).

Look for: every model listed with a score (not `N/A`), `err 0%`, and `resolved_models` populated in the
saved JSON (`jq '.entries[]|[.model,.resolved_models]' /tmp/ob-smoke/*.json`). A model that errors on
every call is a finding, not a retry; report it and drop it from the full run if it's unusable.

### 3. The full run (about 1.25 hours, about $1.25, 7 models)

One invocation, so there is one leaderboard, one weights hash, and the same sampled cases for everyone.
Run it in the **background** and poll with short commands (the operator dislikes long blocking waits).
**This includes re-running both Jev models and pplx** (see priorities above).

```bash
mkdir -p /tmp/ob-full
nohup /tmp/oracle-bench-bin oracle-bench --repeats 3 --seed 1 --concurrency 2 --timeout 15s \
  --label "full re-run, 295-case bank" \
  --models typesafe/jev-1.14,typesafe/jev-1.13,inception/mercury-decide,liquid/d1,microsoft/microsoft-decision-1,cloudflare/clef-flash,perplexity/pplx-decider-v1.1-27b \
  > /tmp/ob-full/run.log 2>&1 &
# progress:   tr '\r' '\n' < /tmp/ob-full/run.log | tail -2
```

Put the slow model (pplx) last so the fast models report first. Add an 8th model to `--models` if the
operator wants one.

Rough budget (895 calls per model at 3 repeats; the judge adds ~2-3 minutes at the end):

| model | per call | time | decision cost |
|---|---|---|---|
| jev-1.14 / jev-1.13 | ~0.3s | ~3 min each | ~$0.18 each |
| mercury-decide | ~1.1-1.4s (spike) | ~9 min | ~$0.09 |
| liquid/d1 | ~1.1s (spike) | ~8 min | ~$0.08 |
| microsoft-decision-1 | ~1.1s (spike) | ~9 min | ~$0.13 |
| cloudflare/clef-flash | ~1.0s (spike) | ~8 min | ~$0.09 |
| pplx-decider-v1.1-27b | ~2.2s | ~19 min | ~$0.10 |
| judge (all models) | | | ~$0.15-0.20 |
| **total** | | **~65-75 min** | **~$1.25** |

Tell the operator the estimate before starting and the actual cost after (it's in the saved leaderboard
JSON: `jq '.entries[]|[.model,.score.cost_usd,.score.judge_cost_usd]' <file>`). If the process dies, the
raw per-call log is still written as calls complete, but a half run is never scored (by design);
re-run it.

### 4. Results are saved automatically (verify, then commit)

The command saves everything itself when a run finishes, with no output flags needed. The final lines
of the run print the exact paths:

- `docs/benchmarks/oracle-decision-models/runs/<stamp>-<label>.json` and `.txt`: the leaderboard and
  the printed table. **Small. Commit these.** Files are timestamped and never overwritten.
- `oraclebench/results/<stamp>-<label>-calls.jsonl` (raw calls, written as they complete),
  `...-scores.jsonl` (per-case scores), `oracle-bench.db` (tracking DB): bulky, **gitignored**, kept
  locally for investigating surprises and for the showcase's per-case detail.

Check that the run ended with the "Saved:" block and no `ERROR: the leaderboard was NOT saved`. If it
errored, the raw calls are still on disk; do not just re-run without saying so. Then commit the
leaderboard files. Every run tomorrow (smoke, full, any follow-up) should be committed with its label
so the showcase can use them all. Add the production-timeout column the leaderboard doesn't have yet:

```bash
jq -s '[.[]|select(.err==null)] | group_by(.model)[] | {model: .[0].model, n: length,
  under_2_5s: (map(select(.latency_ms<=2500))|length), under_5s: (map(select(.latency_ms<=5000))|length)}' oraclebench/results/*-calls.jsonl
```

(Optional improvement worth doing: add a `within_production_timeout` field to `ModelScore` that does
this at 2.5s, and a column for it in `Render`, so the leaderboard and showcase carry it natively.)

### 5. Report to the operator

Give: the leaderboard table; cost (decision and judge, per model and total); the latency ranking; the
share of calls under 2.5s; any disqualifications (error/refusal rate over 5%, dangerous false-nudge
rate over 5%); weakest tracks per model; the `cases worth a human look` list and the label-audit
disagreement rate. Apply the rules in `docs/plans/oracle-benchmark.md` ("Deciding a winner"): a gap
under 2.5 points is a tie, so prefer the faster model; prefer a pinned version over an alias. Frame the
recommendation around the operator's priorities above (speed first). **Do not change `jev.Model` or
`oracleTimeout` without asking.** If a slower model wins on accuracy, say what the timeout would have to
become and what share of turns it would silently drop at 2.5s.

### 6. The showcase document (the operator asked for this)

The operator wants a polished, shareable write-up of the benchmark with charts. Build it **after** the
full run, from the saved leaderboard JSONs, so it can be regenerated when models are added. Suggested
shape (use the `dataviz` skill for the charts; keep the styling calm and professional, matching
PRODUCT.md's "night sky, not tech neon"):

- Location: `docs/benchmarks/oracle-decision-models/README.md`, charts as SVG in the same folder.
- Make it reproducible: a small generator (suggest `polaris oracle-bench report --runs
  docs/benchmarks/oracle-decision-models/runs/*.json`, written in Go like the rest of the repo) that
  reads the JSONs and emits the SVGs plus the tables, instead of hand-drawn charts.
- Sections: what Oracle is and why the decision model matters; method (case bank, the two scoring
  layers, repeats, judge, fixed concurrency, weights) in plain language; headline results; the charts;
  per-track strengths and weaknesses; cost; the recommendation and its caveats; how to re-run it.
- Charts: (1) accuracy vs latency scatter (the money chart; mark the 2.5s production timeout and the 5s
  bench budget); (2) latency ranking bars, p50 with p95 whiskers; (3) per-track score heatmap by model;
  (4) cost per 1,000 turns; (5) trigger/verdict/focus breakdown. Be honest about uncertainty: show the
  95% intervals and say that gaps under 2.5 points are ties.
- Honest caveats to include: the bank is synthetic and hand-labeled (the judge disagreed with labels
  about 1-2% of the time); 3 repeats; latency depends on provider load and was measured at concurrency
  2 on one day; one operator's idea of the right label.

### 7. Clean up: delete this file (the last thing you do)

When the full run, its saved results, and the showcase document are all done and committed, **remove
this handoff**; it is a temporary hand-off, not permanent documentation. Do it as its own commit, and
only after confirming these three things (check each, don't assume):

1. Every run's leaderboard is committed under `docs/benchmarks/oracle-decision-models/runs/`
   (`git status` clean, `ls` that directory).
2. The showcase document exists and was generated from those files.
3. Anything from this file still worth keeping has a permanent home: the "lessons" (rebase first,
   `prompts.yaml` hot-reloads mid-run, "Fired is not a nudge", fixed concurrency 2) belong in
   `DEVELOPMENT.md`'s "Oracle benchmark" section or `docs/plans/oracle-benchmark.md`; the open items
   belong in that plan doc's "Open questions" or in GitHub issues.

```bash
git rm docs/benchmarks/oracle-decision-models/benchmark_handoff.md
git commit -m "Remove oracle benchmark handoff"
```

Also update the plan doc's status line to say the benchmark is finished, and tell the operator the
handoff has been removed. Don't push unless she asks.

## Open items (not blocking the run)

- Weak spots from day one worth a look in the results: Jev 1.14 on `locale` (61), Jev 1.13 on
  `clarify` (61), pplx on `contested` (67).
- One case the judge flagged: `fo-researcher-loan-vs-index` (a bank problem, not a model problem).
- Real option overlaps in `prompts.yaml` that make some labels genuinely ambiguous (`brief` vs `off`,
  `place`/`travel`/`image`, `private_person`'s `no` vs its inject text, `legal` vs `financial`,
  `clarify` "obvious default"). Changing them changes live Oracle behavior, so raise it with the
  operator rather than fixing it as a side effect.
- The `jev.Model` pin is version-level (`typesafe/jev-1.14`); a dated pin
  (`typesafe/jev-1.14-20261014`) is also accepted but a retired snapshot would silently turn Oracle off.
- Other catalog models worth trying later (she said "maybe later"): `cloudflare/clef`,
  `upstage/solar-decide-flash`, `nace-ai/drex-v2.0`, `~perplexity/pplx-decider-latest`. (`clef-flash`
  is in tomorrow's roster.)
