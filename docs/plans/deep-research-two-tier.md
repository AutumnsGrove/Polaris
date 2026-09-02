# Deep Research — two-tier plan

Today's "Deep Research" toggle (`ctx.DeepResearch` in `tools/registry.go`) is a single-agent
turn with a longer leash: `gateway/turn.go`'s `loadSystemPrompt` appends a ~5-sentence prompt
fragment (`prompts.yaml`'s `agent.deep_research_instruction`) and `deepResearchTurnMultiplier`/
`deepResearchCheckInMultiplier` (both `2`, `gateway/turn.go`) double `defaultMaxTurns`,
`researchCheckInInterval`, and `staleStreakThreshold` (all `agent/driver.go`). Same tools, same
agent loop, same 100k-token compaction ceiling (`config.ContextWindowTokens`). It reads as "the
exact same interaction, just slower" because that's what it is.

This plan replaces it with two genuinely different modes, sized to two genuinely different needs:
a sharper single-agent mode for "actually double-check this" (which covers nearly everything used
today), and a real multi-agent fan-out for the rare case that's broad enough to justify 3-15x the
token cost. Background research (Anthropic's published multi-agent research-system architecture,
six open-source deep-research implementations, a Berkeley failure-mode study, and general
orchestration-pattern literature) is not reproduced here — this doc only records the decisions
that came out of it and where they plug into Polaris's actual code.

## Why two tiers, not one improved mode

Anthropic's own published numbers (`multi-agent-research-system` blog post): multi-agent systems
run **~15x the token cost of a plain chat turn** (a later post says 3-10x vs. single-agent) for a
90.2% quality gain *on their internal eval, with a frontier orchestrator model*. Their own stated
default is "start with a single agent" — multi-agent is explicitly gated behind "is this task's
value high enough to pay for it," not something to reach for by default. A Berkeley study (MAST,
arXiv:2503.13657) separately found multi-agent LLM systems fail 41-86% of the time in production,
79% of it traceable to bad task specification/coordination rather than model capability — the risk
is concentrated in exactly the "orchestrator decomposes and delegates" mechanics this plan has to
get right, not a reason to avoid the mode, but a reason to keep it a deliberately-invoked, planned,
and confirmed action rather than something that fires silently.

Given real usage patterns (deep multi-source research is rare; most "I want a better answer"
moments are single-question fact-checks), defaulting into the expensive tier would be paying the
15x multiplier for the 3-10x-poor-fit case most of the time. Splitting the tiers means the cheap
case stays cheap.

## Tier 1 — Researcher (focus mode)

**No new toggle, no new subsystem.** Added as a new entry in `prompts.yaml`'s
`agent.focus_modes` map (the same mechanism `gateway/turn.go:192`'s `loadSystemPrompt` already
uses for the composer's existing Focus Mode selector — `web/src/lib/components/ComposerMenu.svelte`,
bound via the `FocusMode` type in `web/src/lib/types.ts`). Selecting "Researcher" from the
existing focus-mode picker is the entire UI surface — no new component.

What actually changes when it's selected:
- A tailored prompt fragment (replacing today's generic `deep_research_instruction` for this
  specific mode) — cross-check claims against independent sources, follow up on vague/secondhand
  results, consider more than one angle before concluding. Largely the existing wording, tuned.
- `gateway/turn.go`'s turn/check-in multiplier logic gets keyed off `FocusMode == "researcher"`
  in addition to (not instead of) `ctx.DeepResearch` — Tier 1 gets the same widened
  `defaultMaxTurns`/`researchCheckInInterval`/`staleStreakThreshold` leash Deep Research grants
  today, just reached via the focus-mode selector instead of the dedicated toggle, and without
  spawning anything.
- Everything else — tools, model, agent loop, compaction — is completely unchanged.

This is the mode for "actually verify this before answering," which by the user's own account
covers nearly everything they currently reach for Deep Research to do. Ship this first: it's a
prompt/constant change, not new infrastructure, and it doesn't block Tier 2 on anything.

## Tier 2 — Deep Research (multi-agent)

Keeps the existing composer toggle (`ComposerMenu.svelte`'s `deepResearch` boolean,
`ctx.DeepResearch` in `tools/registry.go`) and its name — what changes is entirely on the backend.
This is the new subsystem.

### Flow

1. **Invoke.** User turns on Deep Research, asks a question. Same entry point as today
   (`gateway/turn.go`'s `handleTurn`).
2. **Plan.** The orchestrator (the same agent, no new binary/process — see "Orchestrator" below)
   runs a first pass to decompose the query and decide fan-out width, using Anthropic's published
   complexity bands as prompt guidance (1 agent for simple fact-finding, 2-4 for comparisons,
   more for genuinely broad research) — capped lower than Anthropic's "10+" ceiling given a
   personal deployment's quota reality (see "Budget" below for the actual number).
3. **Confirm.** The plan is presented via the *existing* `ask_user_question` tool
   (`tools/ask_user_question.go`) — no new pause/resume mechanism needed. `ask_user_question`
   already ends the turn cleanly via `PendingQuestion` (persisted through
   `store.SetMessagePendingQuestion`, `store/store.go:1185`) and its `options` are documented as
   "never a restriction on what they can say" — free-text replies already work. The orchestrator
   calls it with the plan as the question text and options like `["Run it", "Cancel"]`. Whatever
   the user replies — an option, "go", or "actually drop the third one and focus on X" — becomes
   the next ordinary user turn. Because `ctx.DeepResearch` is still active and the plan is sitting
   in the transcript, the orchestrator (same model, same thread, no special-casing needed) just
   interprets it naturally: proceeds, replans, or drops back to a normal answer on cancel.
   - One real addition: extend `tools.PendingQuestion` (`tools/registry.go:281`) with an optional
     structured plan payload (sub-agent objectives + estimated search-call budget) purely so the
     frontend can render something better than a wall of text in a question bubble — consistent
     with Tier 2's report rendering (see below). Plain-text fallback for clients that don't render
     it specially.
4. **Fan-out.** Once accepted, the orchestrator spawns the confirmed sub-agents (see
   "Sub-agents" below) as goroutines, each running the normal `agent.Run` loop
   (`agent/driver.go`) against a scoped `tools.Context`, in a synchronous wave (matches Anthropic's
   own documented limitation — full cross-wave async is future work, not v1).
5. **Synthesize.** Sub-agents return structured findings (claim + citation, not free prose — see
   "Synthesis" below); the orchestrator assembles a sectioned report and runs one lightweight
   grounding pass over its own draft against the structured findings before finalizing. A fully
   separate CitationAgent model call (Anthropic's approach) is out of scope for v1 — see below.
6. **Render.** Output is not a plain chat bubble — see "Report rendering."

### Orchestrator

The orchestrator is not a separate model/process — it's the same agent loop Tier 1 uses, given a
new capability (a `spawn_researchers`-shaped tool, gated to fire only under `ctx.DeepResearch`)
and new prompt guidance for the planning step. It keeps the thread's normally-selected model
(no forced upgrade) — unlike Anthropic's Opus-lead/Sonnet-worker split, Polaris's default model
(DeepSeek V4 Flash) is already the cost/quality point Anthropic's split exists to approximate, so
there's no separate "expensive lead model" tier to add here. What *does* get a different,
cheaper model is the workers (next section) — mirroring the split, just starting from a cheaper
baseline than Anthropic's.

### Sub-agents

- **Model:** DeepSeek V4 Flash (the app's existing default), selected explicitly rather than
  inherited from the orchestrator's thread model — same pattern already used for vision
  (`config.MultimodalModel` / `ModelConfig.Multimodal`, `config/config.go:286`), just a new
  config knob (e.g. `config.ResearchWorkerModel`) rather than a new mechanism. Research comparing
  the full roster (MiMo v2.5/Pro, DeepSeek V4 Pro, GPT-5.6 Luna, Nemotron 3 Ultra) found nothing
  that clearly beats DSV4 Flash on cost + speed + confirmed tool-calling reliability — MiMo Pro is
  outright disqualified (its default OpenRouter route doesn't accept `tools` at all). Nemotron 3
  Ultra (paid tier) is the one candidate worth a live A/B later: pricier per-token but the
  fastest model surveyed and purpose-marketed for this workload — worth trying only once Tier 2
  is running against real sessions, not worth blocking v1 on.
- **Tool scoping:** extend `tools/catalog.go`'s `catalogEntry.offered(ctx)` (`tools/catalog.go:54`)
  to also gate on a new `ctx` field (e.g. `SubAgentRole`) — when set, only `web_search`,
  `web_read`, and `think` are offered, regardless of what other gating would normally allow.
  Justified three independent ways, not just cost: tool-selection accuracy degrades past ~15-20
  tools (Anthropic's own stated threshold), narrower tool-definition lists cost fewer tokens per
  call compounding across N parallel agents, and a sub-agent ingesting untrusted fetched web
  content (a prompt-injection surface) shouldn't simultaneously hold write-capable tools.
- **Task spec:** each sub-agent's spawn prompt gets an explicit objective, expected output format,
  tool/source guidance, and task boundaries — the single biggest lever in Anthropic's own
  postmortem for avoiding duplicated work or scope drift between siblings. Not enforced by
  infrastructure; this is a prompt-template concern the orchestrator's spawn call fills in per
  sub-agent.
- **Per-sub-agent budget:** no new mechanism — each sub-agent is a normal `agent.Run` call, so it
  inherits `researchCheckInInterval`/`staleStreakThreshold` for free. This is expected to do most
  of the actual call-count savings (a sub-agent that stops finding anything new stops on its own),
  with the session-level budget below as a backstop for the swarm-total case, not the primary
  lever. Watch real sub-agent transcripts before retuning the existing constants for the
  narrower-scope-per-sub-agent case — no evidence yet that they need different values.

### Budget (session-level)

- A shared counter of total search calls across every sub-agent in the session, tracked via a
  small struct passed into each sub-agent's `tools.Context` (not a global — scoped to one Deep
  Research session).
- **Soft threshold, ~50 total calls**, provider-aware: fires sooner/more insistently once the
  swarm is drawing on the paid Brave/Parallel/Tavily fallback (i.e. SearXNG has degraded) than
  while everything's routing through free SearXNG — the actual expensive scenario is fallback
  usage, not raw call count. On crossing it, inject a check-in message to the *orchestrator* (not
  a kill switch) summarizing calls-so-far and findings-so-far, same philosophy as the existing
  per-agent check-ins, just scoped one level up.
- **Hard ceiling as a circuit breaker only** — something on the order of 2-3x the soft mark
  (~150), enforced by the budget tracker itself refusing further search calls once hit. Not meant
  to be hit in normal operation; exists purely against a genuine runaway/bug case, not as the
  primary budget mechanism (the soft nudge + per-sub-agent check-ins are).
- **Concurrency + dedup:** a bounded semaphore sized to the confirmed fan-out width (not
  unbounded goroutines), plus `golang.org/x/sync/singleflight` (or an equivalent short-TTL cache
  keyed on normalized query text) at the search-dispatch layer in `tools/web_search.go`, so two
  sub-agents issuing the same or near-identical query in one session cost one real API call.
- **Layers in front of, not instead of**, the existing `api_usage` monthly caps
  (`brave.MonthlyCap`, `parallelMonthlyCap` in `tools/web_search.go`) — the session budget is an
  extra guardrail; the monthly ceiling enforcement is unchanged.
- Worth a live worst-case test per this repo's existing verification culture: force a
  SearXNG-degraded state and run a real wide-fan-out session against production to see actual
  Brave-credit burn, rather than reasoning from the design alone.

### Synthesis

Sub-agents return structured findings (claim + citation, ideally as JSON — DSV4 Flash's confirmed
JSON-mode support makes this practical) rather than free prose, specifically to avoid the
"aggregator invents a hallucinated middle ground between disagreeing sub-agents" failure mode
found in the research. The orchestrator assembles these into report sections, then does one
grounding pass over its own draft against the structured findings before finalizing. A fully
separate CitationAgent-style model call (Anthropic's approach) is explicitly deferred — for v1,
one combined synthesis-plus-grounding pass is the cheaper starting point; revisit only if citation
quality turns out to need the extra call in practice.

### Report rendering — descoped after live testing

Originally planned as a real data-model addition (`ReportSections []ReportSection{Heading,
Content, Sources}`, touching `gateway/protocol.go`'s `ServerEvent`, the messages table schema, and
a new collapsible-sections frontend component). Dropped after actually running Deep Research live
(a 9-sub-agent NASA-missions comparison, `spawn_researchers` fired correctly) and seeing the
orchestrator's plain final answer: markdown headers, a comparison table, bold labels, and inline
citation links, rendered by the *existing* chat-bubble markdown pipeline with no special handling
at all. It already read as clearly organized and well-sourced — the thing `ReportSections` existed
to provide. Building the dedicated version on top of that would have meant a heuristic (fragile)
URL-to-section source matcher for a UX gain that live evidence didn't support. If a future model or
question shape produces worse plain-markdown output, revisit rather than build this preemptively.

## Explicitly out of scope for v1

(Revisit later, not forgotten — matches this repo's existing scoping convention.)

- **Recursive/hierarchical fan-out** (sub-agents spawning their own sub-agents). Anthropic's own
  system doesn't do cross-wave async either; single-level fan-out is enough to start.
- **Dedicated CitationAgent** as a separate model call — start with the combined
  synthesis-plus-grounding pass above.
- **STORM-style perspective discovery** (survey existing coverage to pick angles before
  researching) — real design work beyond a fixed-role fan-out; best citation fidelity of anything
  surveyed, but not v1.
- **Adaptive/AIMD concurrency limiting** — not needed given known, fixed monthly quotas; a static
  semaphore is enough.
- **Hard sessions/month cap with its own UI** (ChatGPT's "25/month, visible countdown" pattern) —
  start by watching real `api_usage` numbers manually; add an enforced cap later only if usage
  patterns show it's actually needed.
- **Nemotron 3 Ultra or any other sub-agent model swap** — DSV4 Flash ships as the default; other
  models get evaluated against real sessions after Tier 2 exists, not before.
