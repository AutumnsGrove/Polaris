# Pulsar Daily — v1 plan (living doc, mid-design)

**Status: still in active design/mockup discussion — this doc captures what's been decided so
far and gets expanded as more of it firms up. Not ready to build from yet.** See
`mockups/pulsar-daily.html` for the current visual mockup (open it in a browser; resize the
window to see the mobile behavior).

## Why this exists

Pulsar routines (`docs/plans/pulsar-routines.md`) already prove the "recurring, agent-driven,
compares against yesterday" model works well — a real pulse from today's dev DB (Guild Wars 3
routine) correctly identified "nothing major changed since Sept 3, just some details firmed up."
Pulsar Daily is not a new routine type competing with that — it's a different *assembly and
presentation* of the same underlying capability: instead of N separate chat-thread reports you
have to remember to check, one page pulls together ~10 short blocks (weather, word of the day,
news, etc.) into a single masonry-style "morning newspaper" you open once. The individual pieces
(weather range chart, dictionary tool, visualize, image_search) already exist; this is about
composition, not new fetch/search capability.

## Relationship to existing Pulsar infrastructure

- **Not one big agent turn.** Today's single-routine model (`gateway/pulsar_scheduler.go`'s
  `firePulse`) runs one prompt through one `agent.Run` call. Daily blocks are **N independent
  mini-generations** instead — each block is its own small agent run (or, for simple
  non-research blocks like weather, a direct tool call with no LLM at all), fired concurrently
  (same pattern the scheduler already uses to fire multiple due routines at once). Results
  assemble server-side into one "edition" record. A slow or failing block degrades to a skipped
  card, not a lost day.
- **Reuses the wizard, reshaped.** Filling in each block's prompt can reuse
  `gateway/pulsar_wizard.go`'s interview flow as-is, but with a special injected context telling
  the model what circumstance it's actually writing for — something like: *"This prompt isn't for
  a chat thread — it's one block in a larger daily digest page. Keep the generated output short
  and skimmable; a separate flow lets the reader drill into a full thread if they want more."*
  Same wizard mechanism, different framing prompt.
- **New surface, not folded into the routines list.** Lives at its own route (working name
  `/daily`), linked directly below the Pulsar icon in the sidebar for one-tap access — not a
  `routine.kind == 'daily'` special case bolted onto the existing routines UI.

## The "second pass" — turning good reasoning into a structured signal

Confirmed by reading real pulse content in the dev DB: the model already does solid diffing
reasoning today (*"Since the last report (Sept 3), there's been no major new wave... but a few
details have solidified"*). The problem isn't the judgment quality — it's that the judgment is
buried inside flowing prose, which is useless as a layout signal (nothing can decide "drop this
card" by parsing a sentence).

Fix: after a block generates its normal prose (unchanged from how pulses write today), a second,
narrow follow-up call is handed *yesterday's stored block content* + *today's freshly generated
content* and returns **only a structured verdict via tool call** (same pattern as
`tools/finalize_pulsar_prompt.go` forcing structured output instead of parseable prose):

- `unchanged` → block is dropped from the edition entirely (silent — see the "N routines had
  nothing new today" footnote in the mockup, so a drop doesn't read as a bug).
- `notable` → flagged for the Top Story ranking pass (below).
- anything else → renders as a normal block.

## Top Story: LLM-elected, not a fixed slot

Per feedback, "special" means **more substance**, not a highlight border. Mechanism: after every
block generates, a lightweight ranking pass is handed just a one-line gist per block (not full
text — keeps it cheap) and returns, via tool call, which one is today's lead. That block alone
gets a second, deeper elaboration generation (more research, more paragraphs, a pulled quote,
maybe an image) before final assembly. This is what keeps the same topic from always winning by
virtue of routine list order.

## Expand-to-chat (on-demand depth)

Every card is a tap target. Tapping seeds a **real Polaris thread** through the exact same
`handleTurn` entry point `firePulse` already uses — no new rendering path. The seeded first
message is synthesized from the block's title + its generated content + an instruction to dig
deeper (and, per the newest discussion, to reach for `visualize`/`web_search`/`image_search` if
it fits). This is deliberately on-demand: the Daily's own generation stays cheap and shallow every
day; the expensive deep-research turn only fires the moment it's actually tapped.

**Open**: the seeded prompt template most likely needs to vary per block kind (an etymology
follow-up reads differently than "expand this news story and consider visualizing it") — probably
a `prompts.yaml` entry per block kind rather than one generic template.

## Frontend layout

CSS multi-column masonry (`column-count`, `break-inside: avoid`) — not a fixed-row-span CSS grid.
Confirmed via live mockup testing: a fixed-row-span grid clipped real content once column width
narrowed (text needed more vertical room than its frozen row-span allowed). Column layout sizes
each card to its own natural content height, which matters specifically because real daily
content varies in length day to day and can't be hand-tuned like a static mockup can.
`column-count` steps down at the same breakpoints already used elsewhere (900px → 2 columns,
620px → 1 column / mobile stack).

## Default block set (confirmed, still expanding)

Configurable per-user from day one (not a fixed template) — but the **default** template should
stay broad/general-purpose, since a new user filling this in for the first time doesn't know yet
what they don't want. Confirmed so far:

- Top Story (LLM-elected lead, deep pass)
- Word of the Day (`tools/dictionary.go`)
- Weather (existing range-chart tool)
- On This Day
- Top Headlines
- Trending Now
- Tech & Science digest
- Sports (default-on for the general template; explicitly a "some users won't want this" block —
  first concrete proof the per-user block config matters)
- Picture of the Day (`image_search` tool + the just-shipped gallery/lightbox)
- Quote of the Day

- **Local** — location resolved via the exact same fallback weather already uses for unattended
  runs: `tools.Context.ResolveLocation` → `DefaultLocation` (`tools/registry.go:436`), the static
  `config.yaml` value used specifically because a scheduled generation has no live browser session
  to request a GPS fix from (`gateway/location_broker.go`'s live path only works mid-turn over an
  active WebSocket). No new location config needed — piggybacks on infrastructure weather already
  required for the same reason.
- **Visualize is a capability, not a block.** Resolved as *optional per-generation* on any
  content-generating block (Top Story, Trending, Tech & Science, Local) — each gets `visualize`
  available during its own mini-generation with guidance like "include a chart only if the data's
  genuinely chart-worthy," same optionality `visualize` already has in a normal chat turn.
  Deliberately **not** a dedicated named block (an earlier pass of this mockup had a standalone
  "AI Rollout Watch" card — wrong, because a fixed named slot for "whatever's chart-shaped today"
  recreates the exact always-on-slot problem Top Story's ranking pass exists to avoid). Mocked up
  by folding a `timeline` chart into the Tech & Science card on a day its topic happens to be
  staged/sequential (e.g. a datacenter buildout), not by giving charts their own slot.
- Markets was dropped from the *personal* default per this session's feedback, but stays a
  strong candidate for the general/default template (same reasoning as Sports).

## Generation pipeline

Staged so cross-block dependencies (Top Story needs to see every other block first) don't force
the whole thing sequential — reuses `firePulseRecovered`'s panic-recovery-per-goroutine shape
throughout, so one block's failure degrades to "silently dropped" rather than sinking the run:

- **Stage A (parallel).** Every enabled block fires as its own goroutine — a direct tool call for
  deterministic ones (Weather, Word of the Day, On This Day, Quote), a small restricted-toolset
  `agent.Run` for research ones (Headlines, Trending, Tech & Science, Local — same "narrow
  toolset" shape the wizard already uses), each with `visualize`/`image_search` available but
  optional. Immediately followed, per block, by the diff-judge call — **full content both sides**
  (yesterday's stored block content + today's freshly generated content, not summarized). Resolved
  deliberately: DSV4 Pro is cheap enough, and every block is already a short blurb by design, so
  there's no real cost pressure to trim, and full content lets the architect model actually weigh
  subtle wording changes instead of judging off a lossy summary. Output per block:
  `(content, one-line gist, verdict)`. `unchanged` verdicts *and* hard generation failures both
  drop out here — same bucket, since both mean "nothing to show."
- **Barrier** — wait for every Stage A goroutine before Stage B; this is the one point
  parallelism has to stop, since ranking needs every surviving candidate at once.
- **Stage B.** One call, given only the surviving blocks' one-line gists — **still trimmed here**,
  but for a different reason than cost: this is a *cross-block* comparison (which of ~8-10
  unrelated topics is biggest), not a pairwise one, so stuffing full paragraphs for every block
  into one ranking prompt adds noise the model has to wade through rather than saving money. A
  well-chosen one-liner per block is a cleaner comparison substrate for "which one wins," even
  though the diff-judge's pairwise "did X change" call is better served by full content. Returns
  which block is today's lead via a tool call — same forced-structured-output pattern as the
  diff-judge and `finalize_pulsar_prompt`.
- **Stage C.** The elected block alone gets a second, deeper generation pass (more research
  budget, `visualize`/`image_search` available, more paragraphs). The only stage allowed to be
  slow, since it only ever runs once per day.
- **Stage D.** Surviving blocks (elaborated one flagged/positioned first) get written as one
  `pulsar_daily_editions` row for today's date — what tomorrow's Stage A diffs against, and what
  makes `← Yesterday` real history instead of a dead button.

**First-ever day** has no "yesterday" to diff against — same trick `isRoutineDue` already uses
(falling back to `CreatedAt` when `LastRunAt` is nil): every block's verdict defaults to `notable`
rather than the diff-judge call being skipped or erroring.

**Trigger**: reuses `isRoutineDue`'s time-of-day/baseline logic against a singleton config's
`last_generated_at`, piggybacked on the scheduler's existing once-a-minute tick rather than a
second ticker. Storage is deliberately *not* routine-shaped — Polaris is single-operator (one
Daily, once a day, ever), so this needs one config row (enabled blocks, per-block settings,
time-of-day, model choices) and one edition row per calendar date, not a routines-style table
built for arbitrarily many independent schedules.

## Model tiering — architect vs. writer

Mirrors the lead/worker split `docs/plans/deep-research-two-tier.md` already established for Deep
Research, but resolves it differently because the task shape differs: Deep Research's orchestrator
stays on the thread's own model because its job (planning/dispatching) doesn't clearly benefit
from a stronger model than the existing DSV4 Flash default. Pulsar Daily's "architect" role is
different in kind — Stage A's diff-verdict and Stage B's ranking are genuine judgment calls across
noisy, cross-topic signal, which *does* justify a stronger model:

- **Architect — DeepSeek V4 Pro.** Every *decision*: Stage A diff-verdicts, Stage B ranking.
- **Writer — DeepSeek V4 Flash.** Every *prose* generation: Stage A block content, Stage C
  elaboration — the same model already proven at this exact job (it's what's writing today's real
  pulses in the dev DB).

## Configuration UI

**Confirmed: reuses `PulsarRoutineForm.svelte`'s actual shape**, not a new visual design pass —
that component is already a modal-panel `<dialog>` with name/prompt fields, a model `<select>`,
schedule-type + time-of-day inputs. The Daily setup modal is the same shell with different fields,
not a newspaper-styled artifact like `mockups/pulsar-daily.html` — this piece doesn't need its own
mockup the way the reading surface did.

Field mapping from the existing form to what Daily needs:

| Routine form field | Daily setup equivalent |
|---|---|
| Name | (n/a — singleton, no name needed) |
| Prompt (textarea) | Per-block prompt customization, likely via the wizard rather than free text |
| Model (single select) | Two selects: **Architect** (default DeepSeek V4 Pro) and **Writer** (default DeepSeek V4 Flash) |
| Focus mode / Deep research toggle | (n/a — Daily has its own Stage A/B/C shape, not focus modes) |
| Schedule type + weekly/monthly params | (n/a — always daily, one edition per calendar day) |
| Time of day | Same field, same meaning: when the pipeline fires |
| *(new)* | Enabled-blocks list — toggle each of the v1 blocks on/off, per-block settings (e.g. an explicit location override for Weather/Local instead of falling through to `DefaultLocation`) |

Editable after initial setup, same as a routine's config today — not a one-time onboarding wizard
that locks in choices.

## v2+ candidates (deliberately out of scope for v1)

Considered and shelved, specifically to avoid shipping this bloated on day one:

- **What to Watch** (`tools/movies.go`), **Now Playing** (`tools/music.go`), **Reading Pick**
  (`tools/books.go`) — all reuse existing recommendation tools cleanly, no blocker, just held back
  for scope.
- **Repo Watch** (`tools/github_repo.go`) — **blocked**, not just deprioritized: the tool today
  only answers overview/stats questions for a repo (stars, commit summary, README), with no way to
  explore into recent releases, PRs, or issue activity. Filed as
  [#29](https://github.com/AutumnsGrove/Polaris/issues/29) — a real prerequisite, not a v1/v2
  scheduling choice.
- **Nearby / Worth Checking Out** (`tools/nearby_search.go`) — plausible but acknowledged as hard
  to actually pull off well; needs more thought before it's a real candidate.
- **Trivia / Brain Teaser → daily mini-game** — liked, sparked a bigger idea (something
  Wordle/sudoku-shaped, one puzzle a day) worth exploring on its own later. Explicitly not a v1
  concern.
- **On Your Radar** (`tools/memory.go`) — **dropped, not deferred**: Polaris's memory system exists
  to steer how Polaris itself behaves in a turn, not to be narrated back at the user as content.
  Surfacing "here's something from your memory" as a Daily block would repurpose an internal
  steering mechanism as user-facing copy, which is a structural mismatch, not a scope call — this
  one doesn't get revisited unless the memory system itself changes shape.

## Per-block settings UI

Resolved by checking each block's actual need rather than assuming all ~10 need configuration:

- **Location (Weather + Local)** — no new UI needed at all. Both already fall back to the existing
  global `DefaultLocation` (`config.yaml`), the same value `weather`/`nearby_search` use
  everywhere else in Polaris — adding a Daily-specific override now would duplicate a setting for
  a need nobody's actually hit. A per-block override is a cheap, isolated v2 addition later if it
  ever matters (e.g. wanting Local news for home but Weather for a travel destination), using the
  same conditional-reveal pattern below — not a reason to build it preemptively.
- **Sports — the one real exception.** "Sports" with no team/league preference is meaningless (no
  sane default exists — unlike location, there's no existing global setting anywhere in Polaris
  this could fall back to), so this is a genuine required field, not a nice-to-have.
- **Every other block** (Headlines, Trending, Tech & Science, On This Day, Quote, Word of the Day,
  Picture of the Day, Top Story) — no per-block setting earns its keep for v1; sane defaults work.

**Control shape**: reuses `PulsarRoutineForm.svelte`'s existing conditional-reveal pattern
(`scheduleType`'s select conditionally showing `weeklyParam`/`monthlyParam`, lines ~188-207) rather
than a new mechanism — a block-toggle list where enabling Sports specifically reveals one inline
text field ("Which teams/leagues?"), same interaction shape the form already has, applied to
exactly one block instead of a schedule type. No generic "per-block settings schema" needed for
what's really just one block needing one field.

## Open questions

- Edition retention — editions are confirmed stored (Stage D), but nothing yet decides whether
  they're kept forever or pruned after some window (same category of question `backup.go`'s
  snapshot retention already answers for a different table).

## Expand-to-chat prompt templates

Resolved: not one bespoke template per block (8-10 to maintain), but **three families**, because
the same watch-vs-fresh-pick split already used for diffing turns out to predict which kind of
follow-up makes sense too — not a coincidence, both distinctions come from the same underlying
property (is this an evolving situation, or a fixed daily pick):

- **`research_followup`** — watch blocks (Top Story, Headlines, Trending, Tech & Science, Local)
  plus Weather and Sports (both real-time data even though undiffed). Instructs fresh research,
  explicitly *not* restating what the block already said, with `visualize`/`image_search`
  available if warranted.
- **`curiosity_followup`** — fixed daily picks with no "what's new" angle at all (Word of the Day,
  On This Day, Quote). Instructs depth/etymology/context/trivia over search — these lean on the
  model's own knowledge more than fresh retrieval.
- **`media_followup`** — Picture of the Day specifically, since the input is an image, not a text
  summary — asks about the subject/significance of what's shown, `image_search` available for
  more images on the same subject.

All three share one injected framing prefix (same "special circumstance" idea originally proposed
for the wizard's own Daily-aware framing), so the model never mistakes this for an ordinary typed
message:

> *"The user tapped an expand affordance on a Pulsar Daily block titled '{{title}}' with this
> content: {{content}}. This wasn't typed by them — it's a request to go deeper on exactly this.
> Don't re-greet or re-summarize what the block already said; begin from where it left off."*

Draft shape for `prompts.yaml` (new top-level `pulsar_daily` key, matching `pulsar_wizard`'s
existing structure of one `system`-style prefix plus named sub-prompts):

```yaml
pulsar_daily:
  expand_prefix: >-
    The user tapped an expand affordance on a Pulsar Daily block titled "%s" with this
    content: %s. This wasn't typed by them — it's a request to go deeper on exactly this.
    Don't re-greet or re-summarize what the block already said; begin from where it left off.
  research_followup: >-
    Do fresh research and expand on this — don't just restate what's already shown. Use
    visualize if you find genuinely chart-worthy quantitative data, or image_search if a
    relevant image would help. Cite sources the way you normally would.
  curiosity_followup: >-
    Go deeper on this for its own sake — etymology, context, related trivia, why it's
    interesting — rather than searching for "updates." Lean on what you already know first.
  media_followup: >-
    Tell me more about what's shown in this image — its subject, significance, and context.
    Use image_search if more images would help illustrate the answer.
```

## Sidebar/nav treatment

**Resolved: icon is `Sunrise` (`@lucide/svelte`)**, placed as its own nav entry directly below the
existing `.pulsar-entry` button in `Sidebar.svelte:185-193`, copying that button's exact
padding/hover/active styling rather than inventing new nav chrome. Chosen specifically to extend
the celestial-mechanics icon family the app already established (`Compass` for Atlas, `Telescope`
for Polaris, `Orbit` for Pulsar — see `docs/plans/pulsar-routines.md`'s icon note) along a new
axis: those three are all *instruments*, `Sunrise` is *time-of-day* — fits "a thing opened once
each morning" without importing an unrelated visual language the way `Newspaper`/`CalendarDays`
would have (a newsroom/productivity-app register the rest of the sidebar doesn't share).

**Indicator is a plain dot, not a numeric badge** — unlike `PulsarUnreadBadge`'s per-routine count
(meaningful because there can be many routines with independent unread state), the Daily is a
singleton: there's only ever "a new edition exists" or not, never a count to display. Mocked up in
`mockups/pulsar-daily.html`'s `.sidebar-demo` block, built from the real Lucide `orbit`/`sunrise`
SVG source (not hand-approximated) so the icon choice can be judged accurately.

## Empty-day floor

Resolved by a distinction that wasn't explicit before: **not every block is diffable.**

- **Watch blocks** — Headlines, Trending, Tech & Science, Local, and whatever's nominated for Top
  Story — go through Stage A's diff-judge and *can* legitimately come back `unchanged` on a quiet
  day.
- **Fresh-pick blocks** — Weather, Word of the Day, On This Day, Quote, Picture of the Day —
  aren't watching an evolving situation, they're a new pick every day by construction. Running
  these through the diff-judge at all would pay for a comparison call whose answer is always
  effectively "new" — real Stage A cost with zero decision value. **These skip the diff-judge call
  entirely and always render.** Sports sits in between: fresh data (today's scores), but "no games
  today" is a legitimate, occasional empty state for that one block specifically.

This gives a guaranteed floor with no new mechanism: worst realistic slow-news day, every watch
block drops and the page still has five blocks (Weather, Word of the Day, On This Day, Quote,
Picture of the Day) — reads as "a lighter day," not "broken." Because the masonry layout already
works by not emitting a dropped block's markup at all (rather than an empty placeholder), a day
where five blocks drop at once degrades exactly like a day where one block drops — the single-drop
mechanism already generalizes to the whole-day case.

**Resolved: no Top Story fallback.** If every watch block is `unchanged`, there's no Top Story
candidate, and the slot simply doesn't render — the page leads with whichever fresh-pick block
falls first in the masonry flow. Re-surfacing yesterday's lead was considered and rejected: it
risks making a quiet day look falsely eventful, and would need its own new "has this re-surfaced
story itself gone stale" logic — complexity in service of avoiding a genuinely fine outcome (a
lighter page on a quiet day).

**Resolved: a minimum-block-count guard exists,** distinct from the normal drop mechanism above.
The floor described above assumes fresh-pick blocks succeed — they're simple, mostly single tool
calls, not multi-step research, so a mass failure is unrealistic in the ordinary case. But if
rendered-block count falls under a threshold (e.g. 3-4) anyway, that's not "a quiet day," it's
"something's actually broken" (infra outage, config error), and the page shows a plain degraded
message instead of a suspiciously sparse edition — so a real outage stays visibly distinguishable
from an intentionally light one.
