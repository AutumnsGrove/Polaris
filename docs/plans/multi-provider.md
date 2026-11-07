# Multi-provider — OpenRouter + OpenCode Go behind one switch

**Added 2026-11-04.**

**Status: flipped live on the potato 2026-11-07 — `deepseek` serves from OpenCode Go
(`default_backend: auto`), verified same day: `polaris providers status` shows real
meters (5h $1.73/$12, week $5.28/$30, month $24.77/$60), a live turn wrote
`provider_usage` rows with nonzero allowance and `messages.provider = 'opencode-go'`,
`CoveredBySubscription` splits correctly, and the earlier fake-double proof covered
the spill path. Watch one real week before touching `mimo`. Phase 2 was built on
branch `multi-provider` (PR #199, merged) —
config, router, ledger, status endpoint/CLI, failover proof, and all
product one-shot ports are implemented and green in both loop modes, still
dark by default (no behavior change until the operator flips). Live potato
verification, the settings Go panel, and per-model meter headroom are still
open — see the branch's review notes. Nova prerequisite landed and Polaris
is pinned at v0.3.4 (2026-11-06) —
`go build`/`go vet` clean, `go test ./...` green both with the Nova loop and with
`POLARIS_NOVA_LOOP=0`, parity harness green. No routing code written yet; the swap itself is
still on hold. Policy decisions are settled and the model scope is narrowed to `deepseek`
(+ `mimo` as a follow-up) — see §12. The Nova prerequisite,
[AutumnsGrove/nova#59](https://github.com/AutumnsGrove/nova/issues/59), **landed in
Nova v0.3.4 (2026-11-05)**, so the blocker is gone. Re-checked against the shipped Nova API on 2026-11-05 — the wire, routing,
money, meter, and catalog assumptions all moved at once. Sections carrying an "adjusted for
v0.3.4" note are the ones that changed; §3's third column is the map.**

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
  (the meter is account-wide, so this is 35% of the plan-level window that DeepSeek's $100 dominates —
  there is no per-model reading to confirm it against, §3),
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

## 3. What was verified live (2026-11-04; third column added 2026-11-05)

The single remaining **(unverified)** row was read from docs, not observed against a real keyed
call. Unmarked rows were observed directly. The third column says what Nova v0.3.4 already
settles, which retires most of the spike: these no
longer need answering before Phase 2, only one live confirmation on our own account (§10).

| Fact | Detail | Settled by Nova v0.3.4? |
|---|---|---|
| Base URL | `https://opencode.ai/zen/go/v1` — one root serving `/chat/completions`, `/responses`, `/messages`. Native split also answers: `/inference/go/openai/v1`, `/inference/go/anthropic/v1`. | yes — `route.ProviderDecl{Kind, BaseURL}`; the preset owns the paths |
| Auth | `Authorization: Bearer <key>` | yes |
| **Required header** | `x-opencode-session`. **Verified: a request without it is `400 MissingSessionID`, not a degraded success.** | yes — `route` sets it from `Config.SessionID`, and `testkit`'s Go double refuses a request without it |
| Model ids | Bare slugs (`deepseek-v4.1-flash`, `claude-haiku-5-5`, `mimo-v2.6-flash`, `gpt-6-luna`) — never OpenRouter's vendor-prefixed form. | yes — catalog ids are `opencode-go/<slug>`; `Attempt.Model` always carries the bare slug |
| Catalog (public) | `GET https://opencode.ai/inference/go/v1/models` → 28 models with `capabilities` (tools, `reasoning.effort` tiers, `temperature`), `limit.context`, `cost` **and `cost.multipliers`** (DeepSeek bills ×2 on UTC 01:00–04:00 and 06:00–10:00, Mon–Fri). | partly — `provider.Model` carries `ReasoningEfforts`, `ToolChoice`, `PriceTiers`, `CostMultipliers`, `MonthlyLimitUSD` |
| Dialect translation | `claude-haiku-5-5` is accepted on `chat/completions` (it reaches credential validation, not a model rejection), so **one dialect is enough** — no Anthropic client needed. | yes, and further: Nova dispatches on the catalog id across all three dialects, so Polaris never learns which one ran |
| Reasoning stream | Many Go models send `delta.reasoning_content`; Polaris's `llm.Client` parses only `delta.reasoning`. | yes — `openaicompat` reads both, so the hazard dies with the client (§9 Phase 2) |
| Billing metadata | `usage.opencode.allowance` (dollars of the plan this call consumed), `usage.opencode.list_cost`, `usage.opencode.billing_source`. Not `usage.cost`. | **yes, and it changes §6**: the vocabulary is `go`/`go-plus`/`balance`/`free`/`byok`, and it lands on `Usage.CostUSD` (allowance) plus `Usage.ListCostUSD` (retail), not on a field named after the wire |
| Usage meters (undocumented, keyed) | `GET https://opencode.ai/console/api/go/status` → `access.meters.{fiveHour,week,month}` with `usedMicroCents`/`limitMicroCents`/`resetsAt`, plus `access.endsAt`. **Answered: there is no per-model breakdown.** Both meter endpoints report account-wide windows; the per-model limits are published but remaining allowance per model is not. §2's headroom figure is therefore account-wide, and §7.2's per-model tint has to be derived locally. | yes — `opencode.FetchMeters(client, server, key, orgID)` and `opencode.PeriodEnd(meters)`, with no Nova credential store required |
| Limit shape | Per-model monthly dollars; 5h = 20% of it, weekly = 50%, monthly = 100%. | partly — the monthly figure per model is `provider.Model.MonthlyLimitUSD`; the derived windows are Polaris's to compute |
| `stream_options.include_usage` | Assumed required for usage to appear at all. | moot — `openaicompat` always sends it |
| Terms caveat | Go's docs frame it as "for OpenCode and other coding agents"; Polaris is not a validated client. Use an honest `User-Agent`, and treat any refusal as a fallback, never an outage. | yes — `route.Config.UserAgent` sends it; plan refusals arrive as classified HTTP errors (`EntitlementError` keeps its `Code`), and `OnLimitOrRefusal` spills on the refusal statuses (429/402/401/403/408/5xx). A `400 MissingSessionID` correctly does *not* spill — it is our own misconfiguration, and failing over would hide it |

## 4. Boundary — what moved to Nova (landed in v0.3.4)

Nova v0.2.0 shipped `provider/opencodego`, `mode/providers.Build` and `auth/opencode`, but the
routing lived in `mode/`, which `scripts/importcheck`'s layer rules forbid a library consumer from
importing. That was the blocker, and [nova#59](https://github.com/AutumnsGrove/nova/issues/59)
closed it on 2026-11-05. What Polaris can now build on:

| Nova API | What it settles |
|---|---|
| `provider/route` — `New(Config)`, `Resolve`, `Primary`, `Config.ForceProvider`, `Config.Unknown`, `Config.SessionID`, `Config.UserAgent` | Provider construction, per-catalog-id attempt chains, the session header, the honest UA, the kill switch |
| `Route.FallbackOn` + `route.OnLimitOrRefusal`/`OnAnyError`, `Route.OnFallback` | The spill predicate and its visible event. `OnLimitOrRefusal` covers 429/402/401/403/408/5xx and in-band stream failures; a `bad_request` or a cancellation never spills |
| `provider.Usage{AllowanceUSD, ListCostUSD, BillingSource}`, `CostSource: "allowance"` | The money split (§6) |
| `provider.Response.Provider` | Which backend served the call — the transcript chip and `messages.provider` |
| `provider/chat.Complete` | The one-shot seam: the 16 `llm.NewClient` sites collapse onto one call that routes exactly like a loop turn |
| `opencode.FetchMeters` / `opencode.PeriodEnd` | The Go panel, with our key and no Nova config |
| `provider.Model.{ReasoningEfforts, ToolChoice, PriceTiers, CostMultipliers, MonthlyLimitUSD}` | Picker badges and the per-model limit, with no Polaris-side table |
| `usage.Ledger.Covered()` / `Real()` | The covered-vs-real split that Polaris's own ledger mirrors |
| `testkit` Go double (requires the session header, emits `usage.opencode`, serves `/api/go/status`) | Failover and allowance tests with no key and no network |

`mode/providers.Build` was refactored onto `provider/route`, so Nova's own TUI/`serve` path and
Polaris run the same routing code and the same tests (upstream: RT01–RT14 for routing, and the
`OAI72`/`MSG32`/`RSP32` allowance rows in the three dialect decoders — there are no `ALW` rows; the
concern was absorbed into the per-dialect matrices). Its
`Name()` became `"route"`; Polaris does not depend on it.

| Stays Polaris | Moved to Nova |
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
    plan: go                 # go | go_plus — display only; the limit comes from the catalog (§8)

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
- `POLARIS_NOVA_LOOP=0` overrides all of the above via `ForceProvider = "openrouter"` (§4) — the
  pin is the kill switch, not a routing mode.
- Everything hot-reloads through `liveConfig`, like `nova_loop` and `max_agent_turns`.

**Adjusted for v0.3.4 — the config above is a projection of `route.Config`, not a parallel policy
layer.** The mapping is one-to-one and should be written once, in one function:

| Polaris config | `route.Config` |
|---|---|
| `providers.*` (`api_key`, `base_url`) | `Providers: []ProviderDecl{{Name, Kind, BaseURL, APIKeyFunc}}` |
| `go_model` present, `auto` | a two-attempt `Routes[<polaris model id>]` — `{opencode-go, go_model}` then `{openrouter, model}`, `FallbackOn: route.OnLimitOrRefusal`, `OnFallback` wired to the transcript chip |
| `default_backend: openrouter` | build no Go routes at all (every id falls through to `DefaultProvider = "openrouter"` unrewritten) |
| `default_backend: opencode_go` | **not** `ForceProvider`. A pin would send `go_model`-less ids to Go carrying an OpenRouter slug (`forced()` passes unknown ids through unrewritten). Instead: Go single-attempt routes for ids *with* a `go_model`, OpenRouter single-attempt routes for the rest |
| `model_backends[m]: openrouter` | that id gets a single-attempt `{openrouter, model}` route regardless of `default_backend` |
| `model_backends[m]: opencode_go` | that id gets a Go-first route; a config-load error if the id has no `go_model` (never a silent fallback) |
| `failover: none` | omit the second attempt (there is no separate flag) |
| `POLARIS_NOVA_LOOP=0` | `ForceProvider = "openrouter"` — the only use of the pin: the kill switch forces every call to OpenRouter (§4) |
| the thread id | `Config.SessionID` — read fresh per call, exactly the header requirement in §3 |
| the app version | `Config.UserAgent` |
| (unused) | `Config.Unknown` stays nil: an unconfigured id falls through to `DefaultProvider` (`"openrouter"`) unrewritten, which is the RT08 behaviour the plan relies on |

Two consequences that are implementation rules, not preferences:

1. **`route.New` panics on a misconfigured policy** — no providers, an unknown kind, a duplicate
   name, an attempt naming no provider or no model, a default or pin naming no provider. That is
   the right behaviour from Nova (a silently misrouting router is worse than one that refuses to
   build) but it means **`config.Load` must validate `providers`/`model_backends` itself**, or a
   typo in `config.yaml` takes the server down at startup instead of reporting a config error.
2. **Build the router per config generation, not per request.** `liveConfig` re-reads
   `config.yaml` on every request; constructing a router inside a handler would both waste work and
   turn a panic into a request-time crash. Cache it alongside the parsed config and rebuild only
   when the config actually changes.

## 6. Money — two numbers, honestly

Three distinct quantities, and conflating any two of them is how this feature would start lying:

| Number | Meaning | Nova field (v0.3.4) |
|---|---|---|
| `list_cost_usd` | what the call would cost at list price (the number threads already show) | `Usage.ListCostUSD` on Go; on OpenRouter `Usage.CostUSD` already *is* list cost |
| `allowance_usd` | dollars of the Go plan the call consumed | `Usage.AllowanceUSD`, and also `Usage.CostUSD` when `CostSource == "allowance"` |
| `billed_source` | who paid: `go` / `go-plus` (covered), `balance` (real money), `free`, `byok` | `Usage.BillingSource` |

**Adjusted for v0.3.4 — read this before writing the ledger code.** Nova inverts the field
Polaris's earlier draft assumed: on a Go call, `Usage.CostUSD` is **allowance dollars**, not list
price (`CostSource` is `"allowance"`), and the retail figure is `Usage.ListCostUSD`. So:

- **`messages.cost_usd` ← `Usage.ListCostUSD + (Usage.CostUSD - Usage.AllowanceUSD)`.** That one
  expression is correct in all three cases and is worth a named helper with a test, because the
  failure mode of getting it wrong is a number that looks plausible:
  - a single Go attempt: `CostUSD == AllowanceUSD`, so it reduces to `ListCostUSD` (retail);
  - an OpenRouter attempt: both Go fields are zero, so it reduces to `CostUSD` (retail);
  - **a spilled call: `Usage.Add` sums `CostUSD` across attempts**, so `CostUSD` is
    allowance *plus* retail while `ListCostUSD` only covers the Go attempt — the subtraction is what
    removes the allowance half. A plain `ctx.AddCost(resp.Usage.CostUSD)` records allowance as list
    cost, **overstates by the peak multiplier (×2 in DeepSeek's weekday windows)**, and records `$0`
    for a `billing_source: "free"` call that still has a real list price.
- `provider_usage.allowance_usd` ← `Usage.AllowanceUSD`; `provider_usage.list_cost_usd` ← the same
  list-cost expression above, so the panel's numbers are the same currency as the totals.
- "covered" is `BillingSource` of `go`/`go-plus`, never a literal `"subscription"` — that value does
  not exist. `free` is covered at $0 allowance; `balance` is real money (§6.6).
- `Response.Provider` is the attempt that answered, so "this call spilled" is
  `Response.Provider != route.Primary(modelID)` — the cheapest reliable signal for the transcript
  chip. Ledger caveat: `Stream` returns **combined** usage across attempts (`spent.Add`), so on a
  spilled call the failed Go attempt's tokens are folded into the row written under the serving
  provider. That misattributes a few hundred tokens at most, only on the rare spill — acceptable,
  but the row should carry `spilled = 1` so the panel can footnote it instead of presenting the
  split as exact.
- `Usage.Add` already sums everything across a spill, and `usage.Ledger.Covered()/Real()` is the
  split to mirror rather than re-derive.

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
   **`list_cost_usd`** where `billing_source IN ('go','go-plus')` — list price, so it is the same
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

1. **Settings → Usage → "OpenCode Go"**: plan name, the three meters as bars with reset times
   (`opencode.PeriodEnd` for the monthly reset), `$used / $limit`, and "Polaris's share this month:
   $8.10 of $19.20" from `provider_usage`. Same visual language and shortcut-row pattern as
   `ConstellationUsageModal`/`PulsarUsageModal`. **The meters are account-wide** (§3) — the panel
   must say so, because a number that silently includes pi and OpenCode usage looks wrong next to
   Polaris's own ledger.
2. **Model picker** (`ComposerMenu.svelte`, the `view === 'model'` list): a `Go` / `OpenRouter`
   badge per row, from `route.Primary(modelID)`. With two Go-backed models this is a row
   annotation, not a redesign — the badge that matters is on `deepseek`, the row selected
   essentially every time. A warning tint when a model is close to its monthly limit, derived
   **locally** (`SUM(allowance_usd)` this period against `MonthlyLimitUSD`) and labelled as
   Polaris-only: it undercounts the account, so it is a hint about our share, never a claim about
   what is actually left. The authoritative "can I keep working" signal is the 5h meter, which is
   account-wide and therefore conservative.
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

**Adjusted for v0.3.4 — where the limit comes from, and how the id reaches the router.**

- The `$100`/`$60` column above is no longer a table Polaris maintains: it is
  `provider.Model.MonthlyLimitUSD`, populated from the Go baseline in Nova's catalog. Two
  consequences. `deepseek` is covered (`100`, with the peak multiplier and its effort tiers
  alongside). **`mimo` is not in Nova's baseline** — it arrives through the `models.dev` discovery
  overlay, which carries no allowance — so its `MonthlyLimitUSD` reads `0`. Either Polaris carries
  the one number for `mimo` in config, or that is a tiny upstream follow-up when `mimo` flips. It
  does not block phase 1, which is `deepseek`-only.
- **The route is keyed by Polaris's own model id**, and two things must carry it or the router
  silently falls through to OpenRouter:
  1. `agent.Config.ModelID` must be set. Today `agent/nova_provider.go` passes
     `novaModel{Name: s.Model}` (the wire slug) and nothing else, so a router would key on
     `deepseek/deepseek-v4.1-flash-1017`, find no route, and use the default provider — the exact
     bug Nova's changelog records for subagents. `llm.Settings` (or the replacement for it) must
     carry the Polaris model id through to `agent.Config.ModelID` and `chat.Request.Model`.
  2. `provider.RegisterModel` must be called for each registry entry (`ID: "deepseek"`,
     `Slug: "deepseek/deepseek-v4.1-flash-1017"`), because `chat.Complete` resolves an id to its
     wire slug through Nova's catalog and would otherwise send `"deepseek"` to the wire.
  Doing both once at config load is cheap; discovering 1 the hard way costs a week of "why is
  everything still billing OpenRouter".
- **Two seams, not one.** `provider/chat.Complete` is for the non-streaming one-shot sites (titles,
  suggestions, extraction — it returns the finalized response with no stream events). The turn
  loop keeps streaming, so the `llm.Client` adapter must call `Router.Stream` directly and forward
  `text_delta`/`thinking_delta` to the existing `onChunk`/`onReasoning` callbacks. An implementer
  reaching for `Complete` in the turn path loses token streaming with no error.

**Stays on OpenRouter regardless** (the explicit exceptions to §12's "every chat-shaped call"): `decisions` (Oracle, verification badge, `compare_sources`),
voice STT/TTS, embeddings, and `cmd/benchmark` / `cmd/eval` / `oraclebench` (numbers must stay
comparable, and results should record which backend produced them). Zen's `/zen/v1/systemone` +
`jev-1.13` is a possible later swap for decisions — `decisions.Client` already speaks that shape —
but it is out of scope here and is not needed for the subscription to pay off.

## 9. Phases

- **Phase 0 — spike (a few hours, nothing merged).** Nova's own tests already pin the wire
  (`OAI72`/`MSG32`/`RSP32` for `usage.opencode`, the `testkit` double for the session header and
  `/api/go/status`, `RT03`/`RT04` for limit-spill vs bad-request). What is left is confirmation on
  *our* account, not discovery: one keyed `deepseek-v4.1-flash` call showing real
  `allowance`/`list_cost`/`billing_source` values; one `/api/go/status` dump (expect account-wide
  meters, §3); one look at the console to confirm DeepSeek's plan window. `mimo` repeats the call
  only when it flips. Nothing here blocks Phase 2 starting.
- **Spike results (2026-11-07, live Go key, `deepseek-v4.1-flash`).** All three payload
  shapes Polaris sends returned HTTP 200 on Go: the OpenRouter `provider` routing object
  (accepted and ignored — it was not in the original spike list, but Nova merges
  `ProviderOptions` verbatim on every attempt, so it rides along on the Go leg), the
  `reasoning` object, and an `image_url` part (221 prompt tokens against 36 for text only, so
  the image is processed, not stripped). `usage.opencode` came back with
  `billing_source: "go"`, `allowance` = 0.6 × `list_cost`. End to end through a Go-only
  Polaris instance: a plain turn, a streamed tool-call turn (memory tool, 8,832 cached
  tokens reported on the follow-up call), a title one-shot, and the `provider_usage` /
  `messages.provider` rows all landed correctly. Failover was also proven live against the
  `dev/fakeopenrouter` doubles (`-go -limit` → OpenRouter, `messages.provider = openrouter`,
  no ledger row for the failed leg). So the image-bearing-turn pin described below is **not**
  needed for deepseek on Go; keep it only if a future `go_model` turns out text-only.
- **Vision spike (original framing, now answered above).** Two unknowns gated any image traffic on Go,
  and one of them is already on the routed path: (1) a keyed Go `deepseek-v4.1-flash` call
  carrying an `image_url` part — Nova's Go baseline catalog marks DeepSeek text-only, so a
  400 here is the expected outcome until proven otherwise; (2) the `reasoning` object on a
  Go leg (sent verbatim by design — parity with the OpenRouter wire body). A 400 on either
  never spills, so both must be confirmed before the flip, not after. Note the asymmetry:
  `DescribeImage` (llm/vision.go, always runs on `mimo` via OpenRouter — no `go_model`,
  no route) is safe wherever it runs, but the turn-level *see* path (real image messages
  inside a flipped deepseek turn) already flows through the router: if the spike fails,
  image-bearing turns need a content-aware OpenRouter pin, not just a direct describe
  client — and `Multimodal` (currently per-model) would need to become per-(model,backend).
- **Phase 1 — Nova (nova#59).** **Done, 2026-11-05, shipped in v0.3.4.** `provider/route`,
  `provider/chat.Complete`, the `Usage` allowance fields, `opencode.FetchMeters`/`PeriodEnd`, the
  catalog picker fields, and the `testkit` Go double all exist — see §4.
- **Phase 2 — Polaris plumbing, `deepseek` only.** ~~Starts with `go get
  github.com/AutumnsGrove/nova@v0.3.4`~~ **Done 2026-11-06**: the bump was low-risk as
  predicted — the v0.3.4 upgrade notes are additive (`Usage`/`Response`/`Model` fields), and
  v0.3.0's breaking notes are `bundled`/`tui` only (`bundled/ask` and the `ui_request` id are
  TUI/protocol concerns Polaris does not consume) — Polaris imports `agent`, `message`,
  `proto`, `provider`, `provider/openrouter`, `tool`, and the v0.3.0 changes there are additive
  (`agent.Config.Embed`, `tool.Context.Embed`). `go build`/`go vet` clean with no code
  changes required; `agent/nova_parity_test.go` green, so the wire really is byte-identical.
  Status 2026-11-07 (branch `multi-provider`, green in both loop modes):
  the `providers`/`routing`/`model_backends` blocks (validated in `config.Load`),
  `go_model` on `deepseek`, the `ModelID` + `RegisterModel` wiring, a per-turn
  `Router` with list-cost accounting, `provider_usage` + `messages.provider` +
  `Stats` breakouts, `/api/providers/status` + `polaris providers status|models`,
  failover (`OnLimitOrRefusal` + `OnFallback` chip event, pinned by testkit spill
  tests), and the `dev/fakeopenrouter` Go-dialect `--go`/`--limit` modes are all
  implemented. Every product one-shot runs through `agent.CompleteOneShot`
  (titles, suggestions, memory loop, pulsar suggest, pulsar daily pick/vet/elect/
  diff/elaborate/research, comet approach, weaver, wizard); benchmarks/eval/
  oraclebench stay on OpenRouter by design, as do vision describe (no Go vision
  route) and the research-worker client (pending the `mimo` flip, #191 territory).
  Tools' own side calls (`web_read` filter, `view_image` describe via `ctx.LLM`)
  stay direct — routing them is #189's `Context.Complete` work, not this branch's.
  Ships dark (`default_backend: openrouter`, no Go routes built, `ForceProvider`
  only under the kill switch); flip `deepseek`, then watch one real week of
  windows before touching `mimo`. Still open: live potato verification (§10),
  the settings Go panel and picker badges (Phase 3), meter-aware `auto` headroom
  (Phase 4), `dev/fakeopenrouter`'s rename (Phase 5).
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
`polaris providers status` showing real meters plus the per-model `Primary` table (the config
wiring in §8 is visible before any turn runs); one turn on `deepseek` writing a `provider_usage`
row with a nonzero allowance and `messages.provider = 'opencode_go'`; the settings panel agreeing
with the OpenCode console; a forced limit — point `opencode_go.base_url` at `dev/fakeopenai
--limit` (phase 2) — producing a visible failover to OpenRouter with the same thread continuing on
the next turn; and `POLARIS_NOVA_LOOP=0` degrading exactly as documented in §4.

## 11. Risks

1. Missing `x-opencode-session` is a hard 400 — the first request of a misconfigured client fails
   outright.
2. ~~Omitting `stream_options.include_usage` zeroes usage silently.~~ Moot as of v0.3.4 —
   `openaicompat` always sends it. It stays on the list because it is the reason not to hand-roll a
   request body for a one-off call.
3. ~~`delta.reasoning_content` is ignored, so the thinking UI goes quiet.~~ Moot once `llm.Client`
   is not the transport; `openaicompat` reads both fields.
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
10. **`CostUSD` is allowance on a Go call, not list price** (§6). A `ctx.AddCost(resp.Usage.CostUSD)`
    that is right for OpenRouter silently overstates list cost by the peak multiplier, and records
    `$0` for a covered free-model call. This is the single most likely mistake in the whole change,
    and no test catches it unless one asserts the two fields differ.
11. **A missing `ModelID` looks like success.** The router falls through to `DefaultProvider`, so
    every call bills OpenRouter, every thread says "OpenRouter", and nothing errors. Defence in
    depth: `/api/providers/status` exposes `Primary` per model id (a missing `ModelID` shows up as
    "default" before any money moves), plus the §10 live check that a real `deepseek` turn lands on
    `opencode_go`.
12. **`route.New` panics on a bad policy.** Called per request it turns a config typo into a crash
    loop; it must be built per config generation and validated in `config.Load` (§5).

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
- **Upstream landed (2026-11-05)**: nova#59 closed in v0.3.4. Nothing in §4 is Polaris's to build.
- **Catalogue fields, not a Polaris table (2026-11-05)**: per-model limits come from
  `provider.Model.MonthlyLimitUSD`; `mimo` is the known gap (discovery-only, reads `0`) and gets a
  config value or a tiny upstream fix when it flips.
- **Meters are account-wide (2026-11-05)**: plan headroom is authoritative from
  `opencode.FetchMeters`; per-model headroom is a Polaris-side estimate from `provider_usage` and
  is presented as such. No UI claims a per-model remaining balance is exact.
- **Pin is the kill switch, nothing else (2026-11-05)**: `ForceProvider` is only ever
  `"openrouter"` under `POLARIS_NOVA_LOOP=0`. All other policy is route-building (§5).
- **Two seams (2026-11-05)**: turns stream through `Router.Stream`; `chat.Complete` is one-shots
  only (§8).

## 13. References

- nova#59 (closed 2026-11-05) and Nova's `CHANGELOG.md` [0.3.4] upgrade notes — the contract this
  plan is written against
- Nova: `provider/route/route.go`, `provider/chat/chat.go`, `provider/provider.go:59` (`Usage`),
  `provider/provider.go:155` (`Response.Provider`), `provider/model.go` (`MonthlyLimitUSD` etc.),
  `provider/opencodego/catalog.go`, `auth/opencode/usage.go` (`FetchMeters`/`PeriodEnd`),
  `usage/ledger.go`, `docs/INTEGRATION.md`'s "Two providers behind one switch"
- `llm/client.go` (the OpenRouter-shaped client this replaces, `usage.cost` at :771),
  `llm/settings.go` (gains the Polaris model id for `ModelID`, §8)
- `agent/nova_provider.go` (`openrouter.New` hardcoded, `X-OpenRouter-Metadata` always injected,
  and — the §8 trap — `novaModel{Name: s.Model}` with no `ModelID`)
- `config/config.go:31` (`OpenRouter` block), `config.ModelConfig`, `Models`/`ModelOverrides`,
  `config/config.go:375` (the `nova_loop` kill-switch comment)
- `store/usage.go` (`decisions_usage`/`aux_usage` ledger shape), `store/stats.go:58` (breakout
  precedent), `store/schema.go:470` (`aux_usage` comment)
- `gateway/turn_thread.go:176` and the other 15 `llm.NewClient` call sites
- `docs/plans/nova-loop.md`, `docs/plans/embeddings.md` ("provider abstraction" precedent)
