---
name: oracle-bench
description: >
  Run one or more new decision models through `polaris oracle-bench` end to end:
  catalog check, smoke test, the full 295-case run, the call-stats merge, chart
  and table regeneration, and the docs and PR that record it. Use when the user
  says "run the oracle bench", "bench a model", "a new decision model dropped",
  or hands over OpenRouter decision-model ids to evaluate against the Oracle-mode
  task.
license: MIT
metadata:
  author: Autumn
  version: "1.0.0"
allowed-tools:
  - Bash(go:*)
  - Bash(./polaris:*)
  - Bash(curl:*)
  - Bash(git:*)
  - Bash(gh:*)
  - Bash(wc:*)
  - Read
  - Write
  - Edit
---

# Oracle bench run

Compare decision models on the Oracle-mode task and record the result where the next reader will find
it. The design record is `docs/plans/oracle-benchmark.md`; the method, caveats and per-model detail live
in `docs/benchmarks/oracle-decision-models/notes.md`. Read both before changing anything here.

## Inputs

- `$models`: comma-separated catalog ids, e.g. `liquid/d1.2,liquid/d1.2-ultra`
- `$slug`: short label for filenames, e.g. `d1-2-pair` (it becomes `<timestamp>-<slug>.json`)

## Goal

One committed leaderboard per model, regenerated charts and `results.md`, updated README/notes/plan
prose, and an open PR — with every model measured under identical cases, weights and load.

## Steps

### 1. Confirm the catalog entry

```bash
KEY=$(grep -m1 'api_key' config.yaml | sed 's/.*"\(sk-[^"]*\)".*/\1/')
curl -s "https://openrouter.ai/api/v1/models/$m/endpoints" -H "Authorization: Bearer $KEY"
```

Decision models do not appear in `/api/v1/models` (output modality `decisions`), so query the id
directly; a 404 means the id is wrong. Record the created timestamp and build suffix
(`liquid/d1.2-20261116`), context length, per-million pricing, and whether a `:free` variant exists
(pacing, and a 1,000-request/day cap per OpenRouter account).

**Success criteria**: 200, `architecture.output_modalities` includes `decisions`, and an endpoint whose
model id matches the id you were given.

### 2. Prepare the checkout

- Clean tree, branch cut from `main`.
- `go build -o polaris .` so the bench runs the current weights and case bank.
- Write a throwaway config with **only** an `openrouter:` block and the real base URL
  (`https://openrouter.ai/api/v1`), e.g. `/tmp/ob-config.yaml`.

**Hard rule**: never bench against the repo's `config.yaml` while it has a `providers:` block.
`config.Load` copies `providers["openrouter"]` back over `openrouter:`, so the bench silently measures
the local `fakeopenrouter` double: 0ms calls, $0, every check with probability 1.0 on one option, scored
and saved exactly like a real run. This has happened.

**Success criteria**: the throwaway config has no `providers:` block; the binary builds.

### 3. Smoke test

```bash
./polaris oracle-bench --config /tmp/ob-config.yaml --models $models \
  --n 8 --repeats 1 --no-judge --save-dir /tmp/ob-runs --label "$slug smoke"
```

Check, in order:

- **No `HTTP 422`.** That is a question cap (Oracle asks 23). Probe the exact limit, add the model to
  `questionCaps` in `oraclebench/select.go` (with a test), and re-smoke — the full run would waste 885
  failed calls otherwise.
- **No errors, no refusals**, p50 in the hundreds of milliseconds, nonzero cost.
- The raw log under `oraclebench/results/*-calls.jsonl` shows a real `resolved_model` build and
  `cost_usd > 0`.
- Every answer `probability 1.0` on one option for every call means the fake double or a broken model,
  not a fast model. Stop and investigate.

The smoke score is **not** a ranking signal (eight cases; one model went 97 on the smoke and 80.7 on the
bank). It is for compatibility and cost.

**Success criteria**: every model answers all 8 cases, resolves to the build from step 1, and bills a
nonzero amount.

### 4. Full run

```bash
./polaris oracle-bench --config /tmp/ob-config.yaml --models $models \
  --repeats 3 --seed 1 --concurrency 2 --timeout 15s --label "$slug"
```

- Those flags are the standard for every row. The only deviations ever used: `--concurrency 1` for
  models whose one call is really two requests (the capped Drex builds, and Gemini's 429 storms), and
  automatic pacing for `:free` models.
- Run it in the background **without piping through `tail`** — a buffered pipe hides all progress until
  the end. Either keep the job log unfiltered, or check in with
  `wc -l oraclebench/results/<run>-calls.jsonl` (written as each call completes).
- Do not pull, edit `prompts.yaml`, or otherwise touch the checkout while it runs: `prompts.yaml`
  hot-reloads, so the questions would change mid-run.
- `:free` models are one per day per **OpenRouter account** (not per key). A second free model the same
  day needs a key from a different account.

**Success criteria**: the runner reports every call completed, the judging pass finishes, and a
leaderboard JSON + TXT land in `docs/benchmarks/oracle-decision-models/runs/`.

### 5. Merge the new run's call stats

Raw calls are gitignored and `--calls` **rewrites** `call-stats.json` from only the logs it matches, so
merge rather than regenerate:

```bash
./polaris oracle-bench report --out /tmp/ob-stats \
  --run docs/benchmarks/oracle-decision-models/runs/<new>.json \
  --calls 'oraclebench/results/<new>-calls.jsonl'
```

Then append the new models' entries to the committed `call-stats.json` by hand. Keep the file's existing
order (run order, not alphabetical) and never re-dump the whole map — that reorders every key and turns a
two-entry change into a 1,000-line diff.

**Success criteria**: `git diff` on `call-stats.json` adds exactly one block per new model and changes no
existing entry.

### 6. Regenerate the charts and the table

Pre-flight: run `report` into `/tmp` with every run file the committed table already covers and
`diff` the results against the committed `results.md`. The only differences should be the
`--calls`-dependent columns (`Under 2.5s`, `§` marks). If a row moved, the run list is wrong.

Then run the same command against the docs directory **without `--calls`**:

```bash
./polaris oracle-bench report --out docs/benchmarks/oracle-decision-models \
  --run <all run jsons, new one last> --allow-concurrency-mismatch
```

Rules for the run list:

- **One file per model — the best attempt only.** A later run of the same model replaces the earlier one
  on merge, so listing all four Gemini attempts shows the worst (73.3 instead of the row's 80.3). Failed
  attempts stay in `runs/` as records.
- Exclude the two `2026-10-15` files: older, smaller case bank and older weights, not comparable.
- `--allow-concurrency-mismatch` is required for the concurrency-1 rows (Drex, Gemini), and marks them †.

**Success criteria**: the new rows appear in score order, numbering is continuous, and no existing row
lost its †, § or ‡ annotation.

### 7. Update the prose (generated files are not enough)

- `README.md` — a bullet under "The answer" (score, CI, latency, cost, the comparison to the model it
  replaces, whether it is a candidate and why), plus the Results paragraph's date range, invocation count
  and total cost. The table is generated; paste the new rows in and keep the handmade `(retired)` /
  `(split)` / `‡` annotations.
- `notes.md` — a Runs entry (date, models, one invocation or several), a per-model bullet placed by
  score, a section when the run says something new, and the Method judge counts (add this run's
  spot-checks and disagreements).
- `docs/plans/oracle-benchmark.md` — one appended bullet: created date, benched date, score, the
  comparison, and the decision (usually "not a candidate").
- The pin (`decisions.DefaultModel`) changes only if a model beats `typesafe/jev-1.14` on score without
  a worse tail or a risk the pin does not carry. Say so either way.

**Success criteria**: a reader who never saw the run can tell what it scored, what it was compared
against, and whether anything changed.

### 8. Commit and open the PR

- Branch `bench/<slug>` from `main`.
- `docs(bench): <subject>` with a body carrying the numbers, the comparison, and why the pin did or did
  not move.
- Push and `gh pr create --base main` with a small table and the verdict.

**Success criteria**: an open PR URL.

## Hard rules

- Never bench against a `config.yaml` that has a `providers:` block.
- Never pipe a long run through `tail`; keep the progress visible.
- Never pass `--calls` a partial glob; merge the new blocks by hand.
- Never re-sort or re-dump `call-stats.json`.
- Never regenerate raw logs or fixtures to make a test pass.
- Never change the standard flags "just to go faster": accuracy is unaffected by concurrency, latency is
  not, so a different concurrency costs the row its comparability.
- Report noise: ±4 points on the headline; do not rank inside a tier. A family counts as "weak" only at
  25+ points below that model's own score.
- A row marked § predates per-call cost capture; re-running that model replaces the mark with the
  production figure.

## Verification checklist

- [ ] Leaderboard JSON + TXT committed under `runs/`
- [ ] `call-stats.json` diff is additions only
- [ ] `results.md` and the SVGs regenerated by the command, not edited by hand
- [ ] README table numbers match `results.md` exactly, annotations intact
- [ ] README/notes/plan prose updated; judge counts bumped if the judge ran
- [ ] `go build ./...` passes if any Go changed (adding a `questionCaps` entry is the only likely code
      change)
