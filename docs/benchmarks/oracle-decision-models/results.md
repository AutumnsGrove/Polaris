Run 2026-10-16T01:49:50Z: 295 cases x 3 repeats, concurrency 2, weights `a6cbc140bd53`.

| # | Model | Score (95% CI) | Verdict | Trigger | Focus | Danger FP | Errors | p50 | p95 | Under 2.5s | $/1k turns |
|--:|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 1 | `morph/reflex` | **92.7** (89.0-95.9) | 98% | 98% | 95% | 1% | 0% | 1.5s | 1.8s | 99.4% | $0.902 |
| 2 | `perplexity/pplx-decider-v1.1-27b` § | **91.4** (87.7-94.4) | 97% | 98% | 95% | 0% | 1% | 2.0s | 3.7s | 88.9% | $0.115 |
| 3 | `nace-ai/drex-max` † | **91.3** (88.1-94.1) | 97% | 97% | 95% | 0% | 0% | 482ms | 1.2s | 99.4% | $0.317 |
| 4 | `baseten/mammoth-1:free` | **90.1** (87.1-92.9) | 97% | 97% | 92% | 0% | 0% | 621ms | 1.1s | 99.4% | $0.000 |
| 5 | `inception/mercury-decide:free` § | **89.0** (84.0-93.3) | 97% | 97% | 95% | 1% | 0% | 618ms | 4.3s | 87.8% | $0.000 |
| 6 | `liquid/d1-ultra` § | **87.9** (84.2-91.1) | 96% | 96% | 90% | 0% | 0% | 236ms | 340ms | 100.0% | $0.186 |
| 7 | `typesafe/jev-1.14` § | **87.8** (83.2-92.0) | 96% | 96% | 98% | 1% | 0% | 152ms | 189ms | 100.0% | $0.212 |
| 8 | `microsoft/microsoft-decision-1` § | **87.5** (82.9-91.5) | 95% | 96% | 95% | 1% | 0% | 347ms | 424ms | 100.0% | $0.157 |
| 9 | `typesafe/jev-1.13` § | **86.5** (81.4-90.9) | 96% | 96% | 95% | 1% | 0% | 184ms | 272ms | 99.8% | $0.211 |
| 10 | `perplexity/pplx-decider-v1.2` | **86.5** (82.6-90.0) | 96% | 95% | 90% | 1% | 0% | 871ms | 3.7s | 93.0% | $0.080 |
| 11 | `respan/span-02` | **86.4** (81.5-90.4) | 95% | 96% | 98% | 1% | 0% | 2.3s | 2.6s | 85.9% | $0.060 |
| 12 | `openai/gpt-6-luna-decisions` § | **82.3** (76.3-87.5) | 94% | 96% | 95% | 1% | 2% | 243ms | 409ms | 98.3% | $0.555 |
| 13 | `baseten/elephant-1:free` | **81.2** (75.8-86.2) | 95% | 96% | 80% | 0% | 0% | 445ms | 765ms | 98.4% | $0.000 |
| 14 | `nace-ai/drex-v1.5` † | **80.5** (75.0-85.6) | 94% | 96% | 82% | 1% | 0% | 293ms | 851ms | 99.9% | $0.147 |
| 15 | `liquid/d1.1` | **79.3** (74.0-84.2) | 95% | 95% | 80% | 1% | 0% | 421ms | 654ms | 100.0% | $0.075 |
| 16 | `liquid/d1` § | **78.5** (72.7-83.5) | 95% | 95% | 68% | 1% | 0% | 260ms | 411ms | 100.0% | $0.089 |
| 17 | `parallel/decisions-fast` | **78.4** (72.3-84.0) | 94% | 95% | 85% | 1% | 0% | 697ms | 1.0s | 99.8% | $0.000 |
| 18 | `cloudflare/clef` § | **76.6** (71.2-81.3) | 95% | 94% | 75% | 1% | 0% | 820ms | 1.9s | 98.9% | $0.286 |
| 19 | `perplexity/pplx-decider-flash-v1.2` | **76.2** (70.2-81.4) | 95% | 93% | 70% | 1% | 0% | 386ms | 507ms | 99.5% | $0.057 |
| 20 | `scaledown/classify-1` § | **75.4** (70.6-80.0) | 93% | 92% | 82% | 0% | 0% | 579ms | 1.4s | 97.3% | $0.484 |
| 21 | `cloudflare/clef-flash` § | **73.4** (68.2-78.5) | 94% | 94% | 72% | 1% | 0% | 596ms | 1.3s | 100.0% | $0.118 |
| 22 | `togethercomputer/tev1-4b-experimental` | **70.3** (63.8-75.8) | 93% | 91% | 75% | 1% | 0% | 778ms | 1.0s | 99.8% | $0.261 |
| 23 | `cloudflare/clef-omni` | **66.3** (59.9-72.2) | 93% | 92% | 68% | 1% | 0% | 379ms | 562ms | 100.0% | $0.802 |
| 24 | `jaredpalmer/kev-4b` | **58.5** (52.4-64.1) | 94% | 87% | 55% | 0% | 0% | 1.0s | 1.6s | 100.0% | $0.154 |
| 25 | `upstage/solar-decide-flash` § | **57.3** (49.5-64.8) | 84% | 89% | 85% | 4% | 0% | 1.3s | 1.6s | 100.0% | $0.575 |
| 26 | `baseten/joey-1` | **55.6** (48.4-62.0) | 85% | 89% | 68% | 0% | 0% | 360ms | 572ms | 100.0% | $0.044 |
| x | `google/gemini-decision-1` † | **80.3** (74.8-85.6) | 92% | 96% | 84% | 0% | 19% | 402ms | 643ms | 81.4% | $0.334 |
| x | `nace-ai/drex-v2.0` † | **64.7** (58.1-70.6) | 80% | 92% | 70% | 1% | 22% | 769ms | 1.6s | 77.9% | $0.175 |
| x | `inception/mercury-decide` § | **4.6** (2.4-7.5) | 4% | 97% | 100% | 0% | 96% | 3.9s | 13.3s | 1.2% | $0.004 |

† ran at a different concurrency than the rest (2): `nace-ai/drex-max` (1), `nace-ai/drex-v1.5` (1), `google/gemini-decision-1` (1), `nace-ai/drex-v2.0` (1). Their latency and under-2.5s share were measured under a different load; accuracy is unaffected.

§ cost averaged over the run's identical repeats, not measured as a production turn (`perplexity/pplx-decider-v1.1-27b`, `inception/mercury-decide:free`, `liquid/d1-ultra`, `typesafe/jev-1.14`, `microsoft/microsoft-decision-1`, `typesafe/jev-1.13`, `openai/gpt-6-luna-decisions`, `liquid/d1`, `cloudflare/clef`, `scaledown/classify-1`, `cloudflare/clef-flash`, `upstage/solar-decide-flash`, `inception/mercury-decide`). A provider prefix cache makes repeats 2..n cheaper, so such a row can understate a cache-capable model by a few percent; re-run it to replace this with the fresh-state figure.

x = disqualified, `google/gemini-decision-1`: error/refusal rate above 5%.

x = disqualified, `nace-ai/drex-v2.0`: error/refusal rate above 5%.

x = disqualified, `inception/mercury-decide`: error/refusal rate above 5%.
