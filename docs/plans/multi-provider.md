# Multi-provider — OpenRouter + OpenCode Go behind one switch

**Added 2026-11-04.**

**Status: plan only, no code written, and the swap is on hold. Policy decisions are settled and the
model scope is narrowed to `deepseek` (+ `mimo` as a follow-up) — see §12. The Nova prerequisite is
[AutumnsGrove/nova#59](https://github.com/AutumnsGrove/nova/issues/59); its fixes have not landed
yet, so Polaris is blocked on it. Polaris work starts once the `route` package and the `Usage`
allowance fields exist, and Phase 2 cannot *finish* until the one-shot seam and meter reading land
too (§9). Reviewed for internal consistency 2026-11-05.**

The operator holds both an OpenRouter account (card on file, credits) and an OpenCode Go
subscription (`$10/month`, includes DeepSeek V4.1 Flash, MiMo V2.6, Claude Haiku 5.5, GPT-6
Luna). Go is measured in *allowance dollars per model per month*, not per-call billing — which
makes it nearly free for routine work and effectively unusable for the settings panel as it
exists today. This document is the plan to run both at once, per model, with the subscription
preferred and OpenRouter as the paid safety net.

## 1. Goal

Every chat-shaped call Polaris makes — turns, Pulsar, Pulsar Daily, Comet, Weaver, research
sub-agents, titles, suggestions, wizard, memories, compaction — goes through one routing layer
that picks a provider from *policy*, not from a hardcoded `OpenRouter.BaseURL`. If a model is on
Go and has allowance left, the call is free at the margin. If Go refuses or the window is spent,
the same call continues on OpenRouter, visibly.

The intent is not "support two providers": it is that **adding or moving a provider later is a
config change**, because the plumbing lives in Nova (§4) and Polaris keeps only its model
registry and its money story.

## 2. Why now

- **Nearly all of this deployment's traffic is one model.** The operator has run ~1 month of heavy
  DeepSeek use through OpenCode Go from inside pi and has consumed **35% of the monthly quota**
  (read as 35% of DeepSeek's $100 limit; confirm against the meters in Phase 0, see §3),
  and essentially every turn Polaris makes is on `deepseek` too. So the payoff is not "move the
  app onto a subscription" — it is "the one model that already carries ~all of the spend starts
  billing against an allowance with $65 of headroom still in it".
- **Scope is deliberately two models, not seven**: `deepseek` first, `mimo` (the research-worker
  default) second and only once the first looks comfortable. Everything else stays on OpenRouter.
  A narrow `go_model` map is less surface to get wrong and less to keep in sync against two
  catalogs, and it keeps the cheap-cap models out of the picture entirely — no policy, no warning
  state, no way to route an afternoon of research into a `$15` monthly window.
- Per-model monthly limits, from the Go pricing table, for the two that matter: **DeepSeek V4.1
  Flash $100**, **MiMo V2.6 Flash $60**. (Haiku 5.5 and GPT-6 Luna are $15 each — a reason to keep
  them off the Go map, not a reason to route them carefully.)
- Go is not a replacement for OpenRouter here: it has no decision models (`jev`), no audio, and no
  embeddings. Those stay exactly where they are.

## 3. What was verified live (2026-11-04)

Rows marked **(unverified)** were read from docs/catalog output or inferred, not observed against a
real keyed call; Phase 0 (§9) exists to confirm them. Unmarked rows were observed directly.

| Fact | Detail |
|---|---|
| Base URL | `https://opencode.ai/zen/go/v1` — one root serving `/chat/completions`, `/responses`, `/messages`. Native split also answers: `/inference/go/openai/v1`, `/inference/go/anthropic/v1`. |
| Auth | `Authorization: Bearer <key>` |
| **Required header** | `x-opencode-session`. **Verified: a request without it is `400 MissingSessionID`, not a degraded success.** |
| Model ids | Bare slugs (`deepseek-v4.1-flash`, `claude-haiku-5-5`, `mimo-v2.6-flash`, `gpt-6-luna`) — never OpenRouter's vendor-prefixed form. |
| Catalog (public) | `GET https://opencode.ai/inference/go/v1/models` → 28 models with `capabilities` (tools, `reasoning.effort` tiers, `temperature`), `limit.context`, `cost` **and `cost.multipliers`** (DeepSeek bills ×2 on UTC 01:00–04:00 and 06:00–10:00, Mon–Fri). |
| Dialect translation | `claude-haiku-5-5` is accepted on `chat/completions` (it reaches credential validation, not a model rejection), so **one dialect is enough** — no Anthropic client needed. |
| Reasoning stream **(unverified for the two in scope)** | Many Go models send `delta.reasoning_content`; Polaris parses only `delta.reasoning`. |
| Billing metadata **(unverified)** | `usage.opencode.allowance` (dollars of the plan this call consumed), `usage.opencode.list_cost`, `usage.opencode.billing_source`. Not `usage.cost`. |
| Usage meters (undocumented, keyed) | `GET https://opencode.ai/console/api/go/status` → `access.meters.{fiveHour,week,month}` with `usedMicroCents`/`limitMicroCents`/`resetsAt`, plus `access.endsAt`. This removes the need to reconstruct allowance from our own ledger. **Open question:** the response shows one meter set, while limits are per-model — whether the meters break down per model is unknown until Phase 0. §2's headroom figure, §7's per-model "hot 5h window" tint, and §11 risk 4's headroom routing all assume they do; if they don't, those become a single account-wide meter. |
| Limit shape | Per-model monthly dollars; 5h = 20% of it, weekly = 50%, monthly = 100%. |
| `stream_options.include_usage` **(unverified)** | Believed required for usage to appear at all (§11 risk 2); confirm in Phase 0. |
| Terms caveat | Go's docs frame it as "for OpenCode and other coding agents"; Polaris is not a validated client. Use an honest `User-Agent`, and treat any refusal as a fallback, never an outage. |

## 4. Boundary — what moves to Nova

Nova v0.2.0 already ships `provider/opencodego` (per-model dialect dispatch, session header,
cache-marker policy), `mode/providers.Build` (routes a request to OpenRouter or Go by model), and
`auth/opencode` (`FetchUsage`/`ParseStatus` for the meters). None of it is reachable from a
library consumer: `mode/` is app shell, and `scripts/importcheck` forbids `examples/embed`-style
consumers from importing `mode`, `config`, `session`, `tui`, `bundled`. That is the one blocker,
and it is upstream — see nova#59.

| Stays Polaris | Moves to Nova (nova#59) |
|---|---|
| The model registry and its per-model tuning (`models/models.go`) | Provider construction, per-model provider choice, and failover |
| Which backend a model prefers (`go_model`, `model_backends`) | Wire dialects, headers, session id, cache marker, reasoning field |
| The money story: ledgers, `Stats`, thresholds, warnings | `Usage` carrying allowance / list cost / billing source |
| All UI, all prompts, all tools | Meter reading for a supplied key (no Nova credential store) |
| Model→model mapping between catalogs, and its defaults | A one-shot, non-agent completion seam (so Polaris's 16 `llm.NewClient` sites become one call) |

**Consequence to accept deliberately: Go support is Nova-loop-only.** `POLARIS_NOVA_LOOP=0` (the
kill switch kept for one release, #188) keeps working, but it pins **every** call to OpenRouter —
the bespoke loop and the one-shot sites alike (the router is forced to `openrouter` while the
switch is off). One rule, not two: `llm.Client` serves both the bespoke loop and the one-shot sites
until it is deleted, so splitting the kill switch's effect between them would mean a half-routed
app. Writing a second Go dialect for a loop that is being deleted is the kind of duplication
`docs/plans/nova-loop.md` §3 exists to avoid. Documented in the kill switch's comment
(`config/config.go:375`).

## 5. The switch

```yaml
providers:
  openrouter:
    api_key: "${OPENROUTER_API_KEY}"
    base_url: "https://openrouter.ai/api/v1"
  opencode_go:
    api_key: "${OPENCODE_API_KEY}"
    base_url: "https://opencode.ai/zen/go/v1"
    plan: go                 # go | go_plus — display and default-limit sourcing only

routing:
  # auto: Go when the model has a go_model, Go is enabled, and the model's live
  # meters have headroom; otherwise OpenRouter.
  default_backend: auto      # auto | openrouter | opencode_go
  # A Go call that hits a limit/refusal is retried once on OpenRouter. Never
  # OpenCode console balance — there are no credits there (see §12).
  failover: openrouter       # openrouter | none

model_backends:              # per-model override, mirrors model_overrides
  # Absent at first merge; adding this line is the deliberate flip (§9 Phase 2).
  deepseek: opencode_go
  # Only these two carry a go_model at all; every other model is OpenRouter by
  # construction, not by policy.
  mimo: openrouter           # flip to opencode_go once deepseek looks comfortable
```

- The top-level `openrouter:` block keeps working (copied into `providers` at load) so existing
  installs, `compose/polaris/config.yaml.example`, and `install.sh` don't break. `config.Load`'s
  fail-fast becomes "at least one provider configured", not "OpenRouter required". A Go-only config
  boots, but `decisions` (jev), embeddings and voice need OpenRouter (§8): at startup they log one
  loud warning each and report themselves disabled in `/api/providers/status` — never a silent
  no-op, and never a crash.
- Resolution is **global per model only** — no per-thread picker, no per-thread column. `go_model`
  means *eligible for Go*, never *selected for Go*. Precedence: `model_backends[model]` (explicit
  choice) → `default_backend` (`openrouter` always means OpenRouter; `auto` means Go if the model
  has a `go_model` and meters allow, else OpenRouter; `opencode_go` means Go if the model has a
  `go_model`, else OpenRouter). Naming a `go_model`-less model under `opencode_go` in
  `model_backends` is a config-load error, not a silent fallback. That is a deliberate
  simplification: the operator picks a model and does not want a second switch per conversation.
- `POLARIS_NOVA_LOOP=0` overrides all of the above to OpenRouter (§4).
- Everything hot-reloads through `liveConfig`, like `nova_loop` and `max_agent_turns`.

## 6. Money — two numbers, honestly

Three distinct quantities, and conflating any two of them is how this feature would start lying:

| Number | Meaning | Source |
|---|---|---|
| `list_cost_usd` | what the call would cost at list price (the number threads already show) | `usage.opencode.list_cost` on Go, `usage.cost` on OpenRouter |
| `allowance_usd` | dollars of the Go plan the call consumed | `usage.opencode.allowance` |
| `billed_source` | `subscription` (covered) or `balance` (real money) | `usage.opencode.billing_source` |

Decisions (settled, §12):

1. **`messages.cost_usd` keeps meaning list cost.** Thread cost, per-model comparison, benchmarks
   and every existing total stay comparable across both providers. Never zeroed, never rewritten.
2. **A new ledger records the Go side.** `provider_usage(id, provider, model, allowance_usd,
   list_cost_usd, billing_source, prompt_tokens, completion_tokens, cache_read_tokens,
   created_at)` — one row per Go call, the same shape and the same "record only what completed"
   rule as `decisions_usage` / `aux_usage`. OpenRouter calls get no row: their cost already lives
   in `messages.cost_usd` / `aux_usage`.
   Alongside it, **`messages.provider TEXT NOT NULL DEFAULT ''`** (append-only migration, per the
   store.go rule) records the backend that actually served the message's turn (`openrouter` or
   `opencode_go`, empty for pre-feature rows). Unlike `applied_model` (what was *requested*, see
   `store/schema.go`'s comment on it), this is the served backend, so a failover shows up here and
   drives the transcript chip (§7.3). It is still blind to OpenRouter's own within-model provider
   fallback, same as `applied_model`.
3. **`Stats` gains breakouts, never additive buckets**: `CoveredBySubscription` (sum of
   **`list_cost_usd`** where `billing_source = 'subscription'` — list price, so it is the same
   currency as the totals it sits inside) and `BilledToBalance`. Documented exactly like
   `VerificationCostUSD` — already counted once inside the normal totals, so adding them again
   would double-count. `allowance_usd` is a *different* quantity (plan dollars, including the peak
   multiplier); it feeds the Go panel's "Polaris's share" line (§7.1) and is never summed into
   those totals.
4. **The settings headline becomes two numbers**: `$3.10 real · $12.40 covered by Go (last 30d)`,
   where "real" is list cost minus `CoveredBySubscription` and "covered" is list price, so the two
   add up to the existing total. Plan-dollar consumption appears only in the Go panel.
5. **Remaining allowance comes from the meters** (`/api/providers/status`, §7.4), never from our own
   arithmetic — the meters include usage from other clients and `cost.multipliers` make local math
   wrong.
6. **A `billing_source` of `balance` is a loud warning**, not a silent line item: it means Console
   "Extra Usage" got enabled. If the balance holds money, past the cap every call spends it; if
   it is empty, the call fails instead (§11 risk 5). Either way it is a misconfiguration, never a
   routing outcome.

## 7. UI

1. **Settings → Usage → "OpenCode Go"**: plan name, the three meters as bars with reset times,
   `$used / $limit`, and "Polaris's share this month: $8.10 of $19.20" from `provider_usage`. Same
   visual language and shortcut-row pattern as `ConstellationUsageModal`/`PulsarUsageModal`.
2. **Model picker** (`ComposerMenu.svelte`, the `view === 'model'` list): a `Go` / `OpenRouter`
   badge per row. With two Go-backed models this is a row annotation, not a redesign — the badge
   that matters is on `deepseek`, the row selected essentially every time. A warning tint when its
   5h window is hot, since that is the only window that can interrupt a working session.
3. **Turn and thread**: "covered by Go" alongside the list cost in `TurnInfoSheet`; a transcript
   chip when a failover fired (same "never silent" convention as `[via Brave]`).
4. **Server surface**: `GET /api/providers/status` (both providers configured?, Go meters, current
   policy, last probe error), plus `polaris providers status|models` as a thin client over it, per
   the CLI convention in CLAUDE.md.

## 8. Model map

Registry entry gains one field — `go_model` — and nothing else changes about how models are
declared. Only two entries set it:

| Polaris id | OpenRouter model | Go model | On Go? | Go monthly limit |
|---|---|---|---|---|
| `deepseek` (default) | `deepseek/deepseek-v4.1-flash-1017` | `deepseek-v4.1-flash` | **phase 1** | $100 |
| `mimo` (research worker) | `xiaomi/mimo-v2.6-flash` | `mimo-v2.6-flash` | phase 2, opt-in | $60 |
| `mimo-pro` | `xiaomi/mimo-v2.6-pro` | — | no | — |
| `haiku` | `anthropic/claude-haiku-5.5` | — | no | — |
| `luna` | `openai/gpt-6-luna` | — | no | — |
| `muse-spark` | `meta/muse-spark-1.3-contributor` | — | no | — |
| `mercury` | `inception/mercury-3` | — | no | — |

That narrowness is the point, and it simplifies everything downstream: `deepseek` is the shipped
`default_model`, and turns, Pulsar, Pulsar Daily and Comet inherit it, so phase 1 covers the traffic
that matters while the map stays two lines long. The research worker is the exception: `mimo`
carries `ResearchWorker: true` (`models/models.go`), so research sub-agents stay on OpenRouter
until `mimo` flips in phase 2. `mimo` is a one-line addition
when the first window's numbers look right — its `$60` and others' `$100` are the same currency,
so the decision is about latency and quality on the research worker, not about money.

The other five `go_model`-less entries are not "policy: OpenRouter"; they have no Go route at all,
which is why `auto` needs no special case for the cheap-cap models. Adding one later is the same
one-line change, plus a deliberate look at its limit.

**Stays on OpenRouter regardless** (the explicit exceptions to §12's "every chat-shaped call"): `decisions` (Oracle, verification badge, `compare_sources`),
voice STT/TTS, embeddings, and `cmd/benchmark` / `cmd/eval` / `oraclebench` (numbers must stay
comparable, and results should record which backend produced them). Zen's `/zen/v1/systemone` +
`jev-1.13` is a possible later swap for decisions — `decisions.Client` already speaks that shape —
but it is out of scope here and is not needed for the subscription to pay off.

## 9. Phases

- **Phase 0 — spike (a few hours, nothing merged).** With the real key: capture a full SSE body
  for `deepseek-v4.1-flash` first (`usage.opencode.*` field names, `reasoning_content`, tool calls,
  parallel tool calls), then repeat for `mimo-v2.6-flash`; confirm `stream_options.include_usage`
  is required; dump `/console/api/go/status` and check whether the meters break down per model;
  produce a limit-reached error and record its status/body; confirm the slash-prefixed and native
  base URLs behave identically.
- **Phase 1 — Nova (nova#59).** Four deliverables: the router as a library package, `Usage`
  allowance fields, meter reading for a supplied key, and the one-shot completion seam. Two unblock
  Polaris *starting* ahead of the rest — the `route` package and the `Usage` allowance fields —
  because the config block and the ledger are written against them. **Phase 2 cannot finish on
  those two alone**: the `llm.Client` adapter needs the one-shot seam, and
  `/api/providers/status` needs meter reading. Polaris work that can proceed early is limited to
  the config block, `go_model`, and the ledger/schema.
- **Phase 2 — Polaris plumbing, `deepseek` only.** `providers` config block + `go_model` +
  `model_backends`; `llm.Client` becomes a thin adapter over Nova (then deleted once no caller
  remains); `provider_usage` + `messages.provider` + `Stats` breakouts; `/api/providers/status` and
  the CLI. **Also in this phase, because the flip must not happen without them:** basic failover
  (a Go limit/refusal continues on OpenRouter and says so, via Nova's policy hook) and the
  `dev/fakeopenai` Go-dialect `--limit` mode that §10 needs to verify it (the rest of
  `dev/fakeopenrouter`'s rename stays in Phase 5). Ship with `default_backend: openrouter` and no
  `model_backends.deepseek` line, so the merge is a no-op until deliberately flipped; flip
  `deepseek`, then watch one real week of windows before touching `mimo`.
- **Phase 3 — UI.** The Go panel, the two picker badges, turn attribution, the balance warning.
- **Phase 4 — policy.** `auto` with meter-aware headroom (pre-emptive routing, so failover stops
  being the first line of defence — §11 risk 9). Failover itself already shipped in Phase 2.
- **Phase 5 — cleanup.** `dev/fakeopenrouter` finishes becoming `dev/fakeopenai` (the Go dialect
  flag and `--limit` landed in Phase 2; this is the rename and the rest, needed for Playwright
  against the real app with no key); `agent/nova_parity_test.go` gains a Go-dialect row;
  `docs/FEATURES.md`, `SETUP.md`, `DEVELOPMENT.md` and `HelpModal.svelte`'s `TERMS` get their
  entries.

## 10. Live verification (this codebase's own standard)

Anything touching this must be exercised on the potato against the real Go account before it is
called done — `go test` and review have missed worse here (see CLAUDE.md's Docker-path list):
`polaris providers status` showing real meters; one turn on `deepseek` writing a `provider_usage`
row with a nonzero allowance; the settings panel agreeing with the OpenCode console; a forced
limit — point `opencode_go.base_url` at `dev/fakeopenai --limit` (phase 2) — producing a visible
failover to OpenRouter with the same thread continuing on the next turn; and `POLARIS_NOVA_LOOP=0`
degrading exactly as documented in §4.

## 11. Risks

1. Missing `x-opencode-session` is a hard 400 — the first request of a misconfigured client fails
   outright.
2. Omitting `stream_options.include_usage` is *believed* to zero usage silently (no cost, no
   allowance, no tokens) — unverified until Phase 0, but cheap to always send.
3. `delta.reasoning_content` ignored ⇒ the thinking UI and the reasoning-leak sniffer both go quiet
   with no error.
4. Never derive allowance from tokens; `cost.multipliers` make it wrong by 2x during peak hours.
5. `billing_source = balance` means Extra Usage is on. With an empty balance the call fails (not a
   cheaper call); with funds in it, the call spends real money. Both are misconfigurations (§6.6).
6. The meters are a workspace-level, undocumented endpoint; if it changes, the panel must degrade
   to "unavailable" and keep the ledger-derived number, not crash.
7. Go's terms describe coding-agent traffic; a refusal must be routeable, and the `User-Agent` must
   name Polaris anyway.
8. Two catalogs, two id schemes: a request carrying the wire slug instead of the catalog id is
   exactly the bug Nova already fixed once (their changelog, subagents falling back to
   OpenRouter). Polaris's mapping must be tested, not assumed.
9. Failover doubles the cost of a failed turn when it fires (a Go attempt plus a full OpenRouter
   retry). Acceptable once per limit window; unacceptable per turn — so pre-emptive routing on
   meter headroom matters as much as the fallback itself.

## 12. Decisions (settled 2026-11-04)

- **Billing display**: two-number honesty — show real list cost, plus what the subscription covered.
  No zeroing.
- **Failover**: automatic, Go → OpenRouter, on a limit/refusal. **Never** to OpenCode console
  balance (no credits there); that path is treated as a misconfiguration warning.
- **Scope**: every chat-shaped call goes through the routed layer, not just turns — except the
  explicit stays-on-OpenRouter list in §8 (`decisions`, voice, embeddings, benchmarks/eval).
- **Switch granularity**: global per-model policy only. No per-thread provider selection.
- **Model scope (2026-11-04)**: `deepseek` only for the first release, `mimo` as a one-line
  follow-up if it looks right. No other model gets a `go_model` — the narrow map is the feature,
  not a v1 compromise.

## 13. References

- nova#59 — the upstream work this depends on
- `llm/client.go` (dialect, headers, `usage.cost` parse at :771), `llm/settings.go`
- `agent/nova_provider.go` (`openrouter.New` hardcoded, `X-OpenRouter-Metadata` always injected)
- `config/config.go:31` (`OpenRouter` block), `config.ModelConfig`, `Models`/`ModelOverrides`
- `store/usage.go` (`decisions_usage`/`aux_usage` ledger shape), `store/stats.go:58` (breakout
  precedent), `store/schema.go:470` (`aux_usage` comment)
- `gateway/turn_thread.go:176` and the other 15 `llm.NewClient` call sites
- `docs/plans/nova-loop.md`, `docs/plans/embeddings.md` ("provider abstraction" precedent)
