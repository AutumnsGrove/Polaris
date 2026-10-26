Run 2026-10-16T01:49:50Z: 295 cases x 3 repeats, concurrency 2, weights `a6cbc140bd53`.

| # | Model | Score (95% CI) | Verdict | Trigger | Focus | Danger FP | Errors | p50 | p95 | Under 2.5s | $/1k turns |
|--:|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 1 | `perplexity/pplx-decider-v1.1-27b` | **91.4** (87.7-94.4) | 97% | 98% | 95% | 0% | 1% | 2.0s | 3.7s | 88.9% | $0.115 |
| 2 | `baseten/mammoth-1:free` | **90.1** (87.1-92.9) | 97% | 97% | 92% | 0% | 0% | 621ms | 1.1s | 99.4% | $0.000 |
| 3 | `inception/mercury-decide:free` | **89.0** (84.0-93.3) | 97% | 97% | 95% | 1% | 0% | 618ms | 4.3s | 87.8% | $0.000 |
| 4 | `liquid/d1-ultra` | **87.9** (84.2-91.1) | 96% | 96% | 90% | 0% | 0% | 236ms | 340ms | 100.0% | $0.186 |
| 5 | `typesafe/jev-1.14` | **87.8** (83.2-92.0) | 96% | 96% | 98% | 1% | 0% | 152ms | 189ms | 100.0% | $0.212 |
| 6 | `microsoft/microsoft-decision-1` | **87.5** (82.9-91.5) | 95% | 96% | 95% | 1% | 0% | 347ms | 424ms | 100.0% | $0.157 |
| 7 | `typesafe/jev-1.13` | **86.5** (81.4-90.9) | 96% | 96% | 95% | 1% | 0% | 184ms | 272ms | 99.8% | $0.211 |
| 8 | `perplexity/pplx-decider-v1.2` | **86.5** (82.6-90.0) | 96% | 95% | 90% | 1% | 0% | 871ms | 3.7s | 93.0% | $0.081 |
| 9 | `respan/span-02` | **86.4** (81.5-90.4) | 95% | 96% | 98% | 1% | 0% | 2.3s | 2.6s | 85.9% | $0.060 |
| 10 | `openai/gpt-6-luna-decisions` | **82.3** (76.3-87.5) | 94% | 96% | 95% | 1% | 2% | 243ms | 409ms | 98.3% | $0.555 |
| 11 | `baseten/elephant-1:free` | **81.2** (75.8-86.2) | 95% | 96% | 80% | 0% | 0% | 445ms | 765ms | 98.4% | $0.000 |
| 12 | `liquid/d1` | **78.5** (72.7-83.5) | 95% | 95% | 68% | 1% | 0% | 260ms | 411ms | 100.0% | $0.089 |
| 13 | `cloudflare/clef` | **76.6** (71.2-81.3) | 95% | 94% | 75% | 1% | 0% | 820ms | 1.9s | 98.9% | $0.286 |
| 14 | `scaledown/classify-1` | **75.4** (70.6-80.0) | 93% | 92% | 82% | 0% | 0% | 579ms | 1.4s | 97.3% | $0.484 |
| 15 | `cloudflare/clef-flash` | **73.4** (68.2-78.5) | 94% | 94% | 72% | 1% | 0% | 596ms | 1.3s | 100.0% | $0.118 |
| 16 | `upstage/solar-decide-flash` | **57.3** (49.5-64.8) | 84% | 89% | 85% | 4% | 0% | 1.3s | 1.6s | 100.0% | $0.575 |
| x | `inception/mercury-decide` | **4.6** (2.4-7.5) | 4% | 97% | 100% | 0% | 96% | 3.9s | 13.3s | 1.2% | $0.004 |
| x | `perplexity/pplx-decider-flash-v1.2` | **0.0** (0.0-0.0) | 0% | 0% | 0% | 0% | 100% | - | - | - | $0.000 |

x = disqualified, `inception/mercury-decide`: error/refusal rate above 5%.

x = disqualified, `perplexity/pplx-decider-flash-v1.2`: error/refusal rate above 5%.
