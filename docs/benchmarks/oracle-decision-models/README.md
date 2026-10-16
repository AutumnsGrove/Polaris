# Oracle decision-model benchmark

Which model should answer Oracle's questions? Oracle mode asks a small, fast **decision model** 22
classification questions before every turn ("does this need fresh data?", "is this about a private
person?", "is the user asking for something deep?") and quietly adjusts how the turn is answered. The
decision model sits in front of every message, so it has to be **fast, cheap, and right**, in that
order.

This page records a full run of `polaris oracle-bench` across nine candidate models, and how to
repeat it.

<p align="center"><img src="scorecard.svg" alt="Scorecard: nine decision models ranked by score, with median latency, share of calls under Oracle's 2.5 second timeout, and cost" width="860"></p>

## The short version

- **Nobody beats Jev 1.14 on the thing that matters most: speed.** Its median call takes 152ms and
  every call finished inside Oracle's real 2.5s timeout. No other model matches both.
- **Perplexity's 27B decider is the most accurate model (91.4),** and its lead over Jev 1.14 (87.8) is
  larger than the 2.5-point noise floor. But it takes 2.0s at the median and about **1 in 9 calls
  would miss Oracle's 2.5s cutoff** and silently turn Oracle off for that turn.
- **Three models are statistically tied with Jev 1.14:** Microsoft Decision 1 (87.5), Jev 1.13 (86.5),
  and Mercury Decide's free tier (89.0). Microsoft is the credible alternative: same score, 100% of
  calls under 2.5s, about 2x slower than Jev, and about 25% cheaper per turn.
- **Liquid D1 and both Cloudflare Clef models trail by 9 to 14 points,** mostly on *focus actions*
  (68 to 75% right, against 95 to 98% for the leaders).
- **Paid Mercury Decide could not be scored.** 96% of its calls returned an upstream HTTP 429, so it is
  disqualified for this run. That says nothing about its quality; see [Mercury](#mercury-decide).

Recommendation: **keep `typesafe/jev-1.14` pinned.** Details and caveats below.

## Results

Run of 2026-10-16: 295 hand-labeled cases (274 count toward the score; 21 with disputed labels are reported separately), 3 repeats each, 2 calls in flight, weights `a6cbc140bd53`.
The raw leaderboard is saved in [`runs/`](runs/); this table is generated from it
([`results.md`](results.md)). The two earlier files in `runs/` (2026-10-15) used an older, smaller case
bank and weights, so they are not comparable to this run.

| # | Model | Score (95% CI) | Verdict | Trigger | Focus | Danger FP | Errors | p50 | p95 | Under 2.5s | $/1k turns |
|--:|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 1 | `perplexity/pplx-decider-v1.1-27b` | **91.4** (87.7-94.4) | 97% | 98% | 95% | 0% | 1% | 2.0s | 3.7s | 88.9% | $0.115 |
| 2 | `inception/mercury-decide:free` | **89.0** (84.0-93.3) | 97% | 97% | 95% | 1% | 0% | 618ms | 4.3s | 87.8% | free |
| 3 | `typesafe/jev-1.14` (pinned) | **87.8** (83.2-92.0) | 96% | 96% | 98% | 1% | 0% | 152ms | 189ms | 100.0% | $0.212 |
| 4 | `microsoft/microsoft-decision-1` | **87.5** (82.9-91.5) | 95% | 96% | 95% | 1% | 0% | 347ms | 424ms | 100.0% | $0.157 |
| 5 | `typesafe/jev-1.13` | **86.5** (81.4-90.9) | 96% | 96% | 95% | 1% | 0% | 184ms | 272ms | 99.8% | $0.211 |
| 6 | `liquid/d1` | **78.5** (72.7-83.5) | 95% | 95% | 68% | 1% | 0% | 260ms | 411ms | 100.0% | $0.089 |
| 7 | `cloudflare/clef` | **76.6** (71.2-81.3) | 95% | 94% | 75% | 1% | 0% | 820ms | 1.9s | 98.9% | $0.286 |
| 8 | `cloudflare/clef-flash` | **73.4** (68.2-78.5) | 94% | 94% | 72% | 1% | 0% | 596ms | 1.3s | 100.0% | $0.118 |
| x | `inception/mercury-decide` | 4.6 (2.4-7.5) | 4% | 97% | 100% | 0% | 96% | 3.9s | 13.3s | 1.2% | $0.004 |

*Under 2.5s* is the share of all calls that returned an answer inside Oracle's real timeout
(`oracleTimeout` in `gateway/oracle.go`); a call over that is dropped and the turn runs without Oracle.
*$/1k turns* is the decision-call cost for 1,000 Oracle turns (one call each). Trigger and focus
describe what production's rules *did* with the answer, not just the raw answer. The whole run cost
**$1.22** ($1.06 for decision calls, $0.16 for the judge).

### Overall quality

The headline score on its own, with speed deliberately left out.

<p align="center"><img src="overall-quality.svg" alt="Overall quality score by model with 95% intervals" width="860"></p>

The intervals overlap heavily across the top five. With 274 scored cases, a gap under about 2.5 points is
noise, so the honest reading is three tiers: **Perplexity and Mercury free** at the top, **Jev 1.14,
Microsoft and Jev 1.13** tied just behind, and **D1, Clef, Clef Flash** clearly below.

### Accuracy against speed

The chart that decides it. Up is better, left is faster, and the amber line is the 2.5s cutoff Oracle
actually enforces.

<p align="center"><img src="accuracy-vs-latency.svg" alt="Scatter of score against median latency with Oracle's 2.5 second timeout marked" width="860"></p>

Only Jev 1.14, Jev 1.13, D1 and Microsoft are both fast and consistent: their 95th-percentile call
finishes well under half a second. Perplexity and Mercury free score highest, but their slowest
calls are exactly the ones Oracle throws away.

<p align="center"><img src="latency.svg" alt="Median and 95th percentile latency per model" width="860"></p>

### Cost

<p align="center"><img src="cost-efficiency.svg" alt="Score against cost per 1000 turns with the efficient frontier" width="860"></p>

<p align="center"><img src="cost.svg" alt="Cost per 1000 Oracle turns, ranked" width="860"></p>

Cost is a rounding error at this scale: even the priciest model is about $0.29 per 1,000 turns, so a
heavy month of use costs cents. The frontier is only useful for the free-versus-paid question: Mercury
free and Perplexity are the only models nothing beats on both price and score. Jev 1.14 is off the
frontier on cost, which is the price of its speed.

### Every axis

Eight other ways to rank the same run: raw answer accuracy, closeness, trigger decisions, focus
actions, the contested-label cases, calibration, dangerous false nudges, and errors.

<p align="center"><img src="metrics.svg" alt="Eight ranked panels, one per metric" width="860"></p>

Two details worth knowing. Jev 1.14 is the **best of all nine at focus actions (98%)**, which is the
decision that changes what Oracle *does* rather than what it says. And every model keeps dangerous
false nudges to about 1% or less, so none is disqualified for acting where it should stay quiet.

### Strengths and weaknesses by question family

<p align="center"><img src="tracks.svg" alt="Score for each model on each family of questions, best in each row boxed" width="860"></p>

<p align="center"><img src="breakdown.svg" alt="Verdict, closeness, trigger and focus accuracy per model" width="860"></p>

Where a model is unusually weak (25+ points below its own overall score):

- **Jev 1.14:** `locale` (61). Perplexity scores 97 and Mercury free 99 on the same family, so this is
  a real gap rather than a hard question; Jev 1.13, Microsoft, D1 and Clef sit at 60 to 62 too.
- **Microsoft Decision 1:** `clarify` (43), `locale` (62) and `private_person` (62).
- **Mercury free:** `clarify` (62) and `emotional` (43).
- **Clef:** `emotional` (42).

## Recommendation

Judged on the operator's stated priorities (speed first, then accuracy):

1. **Keep Jev 1.14.** It is the fastest model tested, 100% inside the timeout, best at focus actions,
   and tied on score with every model except Perplexity. The 1.14 over 1.13 gain (87.8 vs 86.5) is
   inside the noise, so the decision to pin 1.14 stands but is not a strong claim.
2. **If Jev ever needs replacing, try Microsoft Decision 1 first.** Same score, same "everything
   finishes in time" behavior, cheaper, about 200ms slower at the median. Its weak spots (`clarify`,
   `private_person`) would need a look before switching.
3. **Perplexity only wins if Oracle's timeout is raised.** To use it without dropping turns, the 2.5s
   cutoff would have to rise to roughly 4s, which adds seconds of dead air before every answer. At
   today's 2.5s it would turn Oracle off for about 11% of turns. Not recommended.
4. **Skip D1 and the Clef models.** They are fast enough but lose 9 to 14 points, mostly on focus
   actions.

`jev.Model` and `oracleTimeout` were not changed by this run.

### Mercury Decide

Mercury has two entries and neither is a clean result:

- **`inception/mercury-decide`** returned `HTTP 429 rate_limit_exceeded` ("Request or spending limit
  exceeded") on 847 of 885 calls, with the same error in an earlier smoke test. The API key itself had
  plenty of headroom, so the limit is on Inception's side. The 38 calls that did answer are not
  enough to score it. **Re-run it later** before drawing any conclusion.
- **`inception/mercury-decide:free`** answered almost everything (1 error in 885) and scored 89.0, but
  its tail latency is poor (p95 4.3s, about 12% of calls over the timeout). A free tier may also be
  rate limited or withdrawn without notice; that was not investigated here.

## Method

- **Cases.** `oraclebench/cases` holds 295 hand-labeled messages across the question families Oracle
  asks about, including 14 `deep_research` cases. 21 are marked as having genuinely disputed labels and
  are reported separately, so the headline score covers the other 274.
- **Two layers of scoring.** The *raw answer* layer asks whether the model picked the labeled option and
  how close its probabilities were. The *production* layer asks whether production's own firing rules
  then delivered the right nudge or action, because a correct raw answer that does not clear its
  threshold changes nothing for the user, and a quiet option winning is not a delivered nudge.
- **Repeats.** Each case is asked 3 times and the probabilities averaged, since they move by a few
  hundredths from call to call.
- **Fixed load.** Models run one after another with 2 calls in flight, so every model is timed under
  the same load. Latency moves with provider load, so treat it as one day's measurement.
- **Judge.** A separate chat model (`deepseek`) reads the disputed cases, near-misses, and a 10% random
  spot-check of ordinary answers. Across 318 spot-checks it disagreed with the hand label twice (1%),
  which is the best available estimate of how wrong the bank is. It flagged
  `dr-remote-work-relocation`, `fo-researcher-loan-vs-index` and `pr-followup-japan-crowds` for a
  human look.
- **Weights.** Scoring weights and the 5s latency budget live in
  [`oraclebench/weights.yaml`](../../../oraclebench/weights.yaml). The 5s budget is deliberately looser
  than the 2.5s production timeout, which is why the timeout share is reported separately.
- **Failures are results.** A refusal or timeout is recorded against the model and never retried. A
  model over 5% errors or over 5% dangerous false nudges is disqualified.

## Caveats

- The case bank is synthetic and hand-labeled by one operator, so it measures agreement with one
  person's idea of the right answer. The judge found 1% disagreement, not zero.
- Three repeats and 274 scored cases give intervals of about plus or minus 4 points. Do not read ranks inside
  a tier as meaningful.
- Latency was measured once, on one day, at concurrency 2.
- Some `prompts.yaml` options genuinely overlap (`brief` vs `off`, `legal` vs `financial`), so a few
  labels are ambiguous by construction. Changing them changes live Oracle behavior, so they were left
  alone.

## Reproduce it

```bash
# one invocation so every model sees the same cases, weights and load
polaris oracle-bench --repeats 3 --seed 1 --concurrency 2 --timeout 15s \
  --label "full run" \
  --models typesafe/jev-1.14,typesafe/jev-1.13,liquid/d1,microsoft/microsoft-decision-1,cloudflare/clef-flash,cloudflare/clef,inception/mercury-decide,inception/mercury-decide:free,perplexity/pplx-decider-v1.1-27b

# regenerate every chart and results.md from the saved leaderboard
polaris oracle-bench report \
  --run docs/benchmarks/oracle-decision-models/runs/<stamp>-<label>.json \
  --calls 'oraclebench/results/<stamp>-<label>-calls.jsonl'
```

The run saves its leaderboard to `runs/` on its own. Do not edit `prompts.yaml` or pull while a run is
in flight, because it hot-reloads and would change the questions for the remaining calls. About 8,000
calls take roughly 50 minutes plus a judging pass, and cost about $1.20. Design notes and the scoring
rules are in [`docs/plans/oracle-benchmark.md`](../../plans/oracle-benchmark.md); how to write cases is
in [`oraclebench/AUTHORING.md`](../../../oraclebench/AUTHORING.md).
