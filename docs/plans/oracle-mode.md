# Oracle mode

**Added: 2026-09-28.**

**Status: built (2026-09-28, issue #122).** Mockups (`mockups/oracle-mode.html`) and the live Jev
spike (below) both done; the engine, UI, and settings toggle shipped behind the opt-in
`oracle_enabled` setting. The `field` offer chip shipped inert (the Fields feature, issue #119,
didn't exist yet, so nothing populated `OracleInput.FieldOptions`); it was wired up on 2026-09-29
once Fields landed — see "The field chip" in [oracle-checks-expansion.md](oracle-checks-expansion.md).
A second opt-in, `oracle_ghost_enabled` (Settings → Oracle mode → "Also in ghost conversations"),
lets Oracle run in ghost turns too — minus the permanent offer chips (pulsar/daily/field), which
`gateway/oracle.go`'s `oracleGhostChips` withholds there.

## The idea

An opt-in settings toggle that lets Jev read each prompt before the turn starts and steer it: pick a
focus mode, decide whether research is needed, and — the part that makes it more than a switch-flipper
— inject short, targeted guidance into a dedicated `## Oracle` section of the system prompt. "This
looks like a medical question: prefer primary clinical sources." "This is asking about a place:
`nearby_search` is usually the right tool." "This is ambiguous enough to ask one clarifying question
before researching."

The goal is for Polaris to feel like it already knew how to approach the question — without the
operator ever opening the composer's "More" sheet — while staying legible: every Oracle decision is
visible and reversible from the reply itself (see "UI" below). Sourcing is the product; Oracle's own
decisions get sourced too.

**Why Jev, not another LLM call**: every decision here is classification over a small, fixed option
set — exactly Jev's shape (`jev/jev.go`, already live in `gateway/verification.go`). One Jev request
carries a whole map of questions, each evaluated in parallel and in isolation against the same
`state`, so adding a question costs almost nothing in latency and very little in money. See
[source-verification.md](source-verification.md) for Jev's API shape and general caveats.

## Decisions already made (2026-09-28)

- **Deep research is never turned on automatically.** The ceiling for Oracle-driven depth is the
  **Researcher** focus mode, which the model already adheres to well and which cites deeper sources.
  Deep research (cost, sub-agent fan-out) stays a deliberate, manual choice.
- **No tool disabling.** Oracle never removes tools from a turn's offered set. It only *nudges*
  toward tools via injected prompt text; the model still decides. (Pulsar Daily's
  `DisabledTools` lock-down is the right call for a narrow, fixed pipeline; it's the wrong call for
  open-ended chat where a misclassification would silently cripple the turn.)
- **Research-off is a soft hint, never a hard switch.** Manual chat mode (`tools.Context.NoResearch`)
  removes research tools because the operator chose that on purpose. Oracle didn't have that
  choice made for it, so a "no research needed" verdict injects a lighter nudge ("this can likely be
  answered without searching") and leaves every tool available.
- **Heavy recall is out.** Memory summaries are already auto-injected and the model follows those
  breadcrumbs well. Summarizing every thread into something Jev could select from would cost a lot
  of tokens for little gain. Only a cheap text-cue check survives (see `recall` below).
- **Model / reasoning-effort routing is v2 at the earliest.** Plausible ("hard prompt → strongest
  model, 90% of prompts → default model at medium effort"), but it's the only check that materially
  changes per-turn cost, and a wrong pick is a worse failure than a wrong nudge. Revisit once v1's
  classification accuracy has been observed on real prompts.
- **Smart chips are offers, never actions.** Oracle can suggest "Make this a Pulsar", "Move to a
  field", "Add to Daily" under the reply; nothing happens unless tapped.

## How it works

### Where it runs

`gateway/turn.go`'s turn setup, before `agent.loadSystemPrompt` builds the system prompt. When Oracle
mode is on:

1. Build Jev's `state`: the current user message, plus the **previous user message** in this thread
   if there is one. A follow-up like "what about the second one?" is unclassifiable on its own; one
   prior message fixes most of that and stays far below Jev's 32k-token context.
2. Send every enabled check from `prompts.yaml`'s `oracle.checks` as one `AskChoice` request. (Post-launch: thresholds and focus-mode rules moved out of `prompts.yaml` into `config.yaml`'s `oracle:` block — `config/oracle.go`, defaults mirrored in `config.yaml.example` — so prompts.yaml holds wording only.)
3. For each answer whose winning option clears that check's threshold, apply it: set the turn's
   focus mode / soft-no-research flag, and collect the option's `inject` text.
4. Build `## Oracle` from the collected injections and add it to the system prompt — **and** to
   `modeReinforcement`'s recency re-injection near the end of the message list, for the same reason
   focus modes are re-injected there (a standing instruction buried at position 0 drifts out of the
   model's attention as history grows).
5. Emit a WS event with the full result (every check's winner, probabilities, whether it fired) so
   the frontend can render the Oracle chip and its "why" sheet, and persist it with the assistant
   message so reloading a thread shows the same thing.

If Jev errors, times out, or no Jev client is configured, the turn proceeds exactly as it would with
Oracle off. Oracle must never be a way for a turn to fail.

### Manual always wins

If the operator set a focus mode or toggled research by hand for this message, Oracle doesn't
override that field — it still runs its other checks (high-stakes, intent, clarify) and injects
their guidance.

**The Settings default focus mode is not a manual pick.** Today `settings.defaultFocusMode`
pre-fills the composer, so by the time a message is sent, the server can't tell "the operator
chose Brief for this message" from "Brief was the standing default." With Oracle on, the default
becomes Oracle's fallback — used when the `focus` check isn't confident — and only a pick made in
the composer for this message counts as manual. That needs the ask/WS request to carry where the
focus mode came from (e.g. `focus_mode_source: "manual" | "default"`), not just its value.

### Yes/no questions

`jev.go` only implements `AskChoice` today. Every yes/no check below is a two-option Choice
(`yes`/`no`), so v1 needs no new client code. Noul can replace them later if a live comparison shows
it's better calibrated.

### Thresholds

Each check has its own confidence threshold, read from the winning option's probability. Starting
values in the draft below are guesses — the live spike (see "Next steps") is what should set them.
Rough intent: cheap, harmless nudges (intent → tool hint) can fire at lower confidence; anything
that asks the model to interrupt the operator (clarify) or changes the turn's shape (focus mode)
needs high confidence.

### Stickiness across a thread

Oracle re-classifies every message (it's cheap), but a **focus-mode change mid-thread** only happens
when the new winner clears a higher `switch_threshold` than the initial pick needed. Injections
(high-stakes, intent) don't need this — they're per-message guidance, not thread state — so they
fire whenever their own threshold clears.

**A mode Oracle set can be retracted by Oracle.** (Added 2026-09-28 after a live session.) The
original design only ever *changed* a mode, so once Oracle picked one the thread kept it until a new
winner cleared `switch_threshold` — including on messages the old mode had nothing to do with (a
"shopper" thread asked a background-research question; `shopper` scored 0.00 and the thread stayed in
shopper mode). Now, when the focus check's winner can't clear its own plain `threshold` — an explicit
`off`, or a winner below the base bar — and the mode in effect is one Oracle itself set earlier
(`PriorOracleFocusMode`), Oracle **clears** it: the turn runs with no mode and `threads.focus_mode`
is written back to `""` (surfaced as `OracleResult.FocusCleared`). Two things it deliberately does
*not* clear: a mode the operator or the Settings default supplied (Oracle doesn't own those), and a
near-miss on a new mode that clears the base bar but not `switch_threshold` — clearing there would
drop the thread every time Oracle leaned another way without being confident enough to switch, which
is exactly what `switch_threshold` exists to prevent. Sticky modes (`safari`) are never cleared.

### Clarify only on the first message

The `clarify` check only runs on a thread's first message. Mid-thread, history usually resolves the
ambiguity already, and a clarifying question there reads as the model forgetting the conversation.

### Cost and budgets

Jev's claimed latency is 70–500 ms and its input price $0.042/MTok, output free (vendor figures, not
yet measured on the potato — see source-verification.md). A few hundred tokens of state × ~7
questions is a rounding error per turn. Oracle's spend counts against the existing Jev monthly
cap in `gateway/verification.go` (`jevMonthlyCapUSD`) rather than a separate one — it's written to
the shared `jev_usage` ledger tagged `source = 'oracle'` (issue #125), so the cap sums it while
Settings' usage stats break it out from verification; if the cap is hit,
Oracle silently behaves as if it were off.

Latency matters more than money: this call sits in front of the first token. It can run concurrently
with the rest of turn setup (history load, memory injection), and the UI's "reading the stars" moment
covers it visually — but the spike should measure real p50/p95 from the potato before committing.

### What gets recorded

Every turn with Oracle on stores which checks ran, each winner and its probability, and which
injections fired. That powers the "why" sheet, and it's the dataset for tuning thresholds and nudge
wording later (a natural fit for the hill-climbing work in
[hill-climbing-objectives.md](hill-climbing-objectives.md)).

## The checks (v1)

| Check | Options | When it fires |
|---|---|---|
| `focus` | the 8 focus modes + `off` | Sets the focus mode. Never picks deep research (not an option). |
| `research` | `yes` / `no` | `no` → soft "probably no search needed" nudge. Tools stay available. |
| `high_stakes` | `medical` / `legal` / `financial` / `safety` / `none` | Injects a domain-specific sourcing nudge; also nudges focus to Researcher if nothing else set one. |
| `intent` | `place` / `book` / `film_tv` / `music` / `product` / `weather` / `video` / `code` / `definition` / `general` | Injects a tool-and-shape hint for that intent. `general` injects nothing. |
| `clarify` | `yes` / `no` | First message only. `yes` → nudge toward one `ask_user_question` before researching. |
| `recall` | `yes` / `no` | Prompt refers back to a past conversation ("like we discussed…") → nudge toward `search_chats`. Text-cue only; no chat database sent to Jev. |

Plus the chip checks, which don't inject anything and only surface offers under the reply:

| Chip check | Options | Chip |
|---|---|---|
| `chip_pulsar` | `yes` / `no` | "Make this a Pulsar" — recurring information (prices, scores, an ongoing story, a release date). |
| `chip_daily` | `yes` / `no` | "Add to Daily" — something to keep an eye on each morning, as a Pulsar Daily custom block. |
| `chip_field` | field names + `none` | "Move to *Field*" — only asked when the thread isn't already in a field, and only if fields exist. Option set is built at request time from the store, not from `prompts.yaml`. |

Chip checks run in the same Jev request as everything else, so the chips are ready well before the
LLM-generated follow-up suggestions — they render **above** those suggestions.

**`high_stakes` and focus interplay**: if `high_stakes` fires and neither the operator nor the `focus`
check picked a mode, focus becomes Researcher. If a mode is already set, it stands — but every
injection is checked against it first; see "Focus modes, one by one" below, Brief especially.

**Verification**: per-claim "found in source" verification already runs on every turn
(`gateway/turn.go`), so high-stakes doesn't need to switch it on. The high-stakes nudges instead push
the model toward sources worth verifying (primary, authoritative) and toward `compare_sources` when
sources disagree.

## Focus modes, one by one

Oracle's nudges were first written assuming a normal answer. Every focus mode reshapes the answer
differently (see `prompts.yaml`'s `agent.focus_modes`), so each injection has to be checked against
each mode — whether Oracle picked that mode, the operator picked it, or it's the Settings default.
The mechanism: any `inject` text can carry per-focus-mode variants (`by_focus` in the draft below),
and a check can be skipped outright for a mode (`skip_for_focus`). A variant replaces the default
text; it doesn't stack on top of it.

| Mode | Oracle picks it when | Note wording | Rules |
|---|---|---|---|
| `off` | No mode is clearly better | *(focus not mentioned)* | Baseline. |
| `brief` | The message itself asks for brevity ("quick question", "tl;dr", "one word") or is a single fact lookup | "kept **brief**" | Higher bar to pick (0.80). **High-stakes never makes Oracle pick Brief**, and when Brief is already on, high-stakes and intent nudges switch to short variants (below). |
| `researcher` | Careful cross-checking matters | "answered as **Researcher**" | The ceiling for Oracle-chosen depth, and high-stakes' fallback when no mode is set. |
| `academic` | Scientific/medical/technical, best from papers or official docs | "answered as **Academic**" | Complements high-stakes medical; no conflict. |
| `news` | Current events | "answered as **News**" | Likely to trigger the Pulsar/Daily offers; nothing to suppress. |
| `shopper` | Find/compare/buy a product | "answered as **Shopper**" | Skip the `intent: product` nudge — Shopper's own instructions already cover it, and two versions of the same guidance fight each other. |
| `first_principles` | Wants the why/how from fundamentals | "answered from **First Principles**" | No conflicts. |
| `socratic` | Wants to reason it through step by step | "answered as **Socratic**" | Polaris's Socratic is a step-by-step walk-through, not a question loop, so clarify still applies. |
| `safari` | Explicitly wants an interactive, multi-stop exploration | "answered as **Safari**" | Highest bar to pick (0.85). Skip `clarify` (Safari's Embark step already asks). **Sticky for the whole thread**: Oracle never switches out of Safari mid-thread, since that would abandon the stop-by-stop loop halfway. |

### Brief, specifically

Brief says "a few sentences or a tight paragraph, no filler." Several nudges ask for more than that
(high-stakes wants caveats and exact figures; book/film/music want a comparison of each title), so
when Brief is on those nudges swap to short variants that keep the one thing that matters:

- **High stakes + Brief**: stay short, but never drop the single caveat that changes what the user
  should do, keep exact figures exact, and still cite a primary source. Brevity trims explanation,
  not safety.
- **Book / film / music + Brief**: at most one line per title on how it differs, or just the one
  best pick.
- **Product + Brief**: the one best option and its price, not a comparison table.
- **Clarify + Brief**: unchanged — asking one short question is compatible with a short answer.
- **Research-off + Brief**: unchanged — Brief never limits research, only the written answer.

And Oracle itself won't choose Brief when `high_stakes` fired: a short, confident answer to a
medical or legal question is exactly the failure this whole feature is meant to prevent. The
operator can still pick Brief by hand (or have it as the Settings default); then the short
high-stakes variant applies.

### Settings default and Shopper/Safari

`gateway/settings.go`'s `validFocusModes` (the Settings default) was missing `shopper` and `safari` —
added to `agent.FocusMode` after that set was written — so picking either as a default 400'd. Fixed
alongside this plan, with a regression test that checks the set against `prompts.yaml`'s
`focus_modes` keys. Either can now be a standing default, which with Oracle on means Oracle's
fallback — another reason both keep high pick thresholds when Oracle chooses them itself.

## Draft `prompts.yaml` section

A new top-level `oracle:` block, loaded through `prompts.Set` like everything else (compiled-in
defaults as fallback, hot-reloaded). Adding a new nudge is a YAML edit — no Go change, no rebuild —
as long as it's a new option on an existing check or a new check whose only effect is injection.
Checks with side effects (`focus`, `research`, the chips) are wired by `key` in Go.

Option descriptions are what Jev sees (Choice `criteria`); `inject` is what the main model sees.
Wording below is a first draft for the operator to react to, not final.

```yaml
oracle:
  # Heading + preamble for the injected section. {items} is replaced with
  # every fired check's inject text, each as its own paragraph. The preamble
  # tells the model these are hints from a classifier, not user instructions —
  # so a wrong guess gets ignored rather than obeyed.
  section: |-
    ## Oracle

    These notes come from an automatic pre-read of the user's message, not from the user. Treat
    them as hints about what probably helps here — follow them when they fit, and ignore any that
    turn out not to match what the user actually asked.

    {items}

  # Jev's shared framing for every check, prepended to each check's own
  # instructions. {state} is the message(s) being classified.
  question_preamble: >-
    You are reading a message someone sent to a research assistant that searches the web and cites
    sources. Answer about the latest message; an earlier message, if present, is only context.

  checks:
    focus:
      threshold: 0.70
      switch_threshold: 0.85   # needed to change an already-set mode mid-thread
      # Per-option pick bars, above the check's own threshold. Brief is the
      # one mode that can make an answer *worse* if picked wrongly; Safari
      # takes over the whole thread.
      option_thresholds:
        brief: 0.80
        safari: 0.85
      # Oracle never switches out of these once they're set for a thread.
      sticky: [safari]
      # Oracle won't pick brief if high_stakes fired (see the plan's
      # "Brief, specifically").
      never_with_high_stakes: [brief]
      instructions: >-
        Which answering style best fits this message? Pick "off" unless one style is clearly a
        better fit than a normal, balanced answer.
      options:
        off: A normal balanced answer fits; no special style is clearly better.
        brief: The message itself asks for a short answer (quick question, tl;dr, one word) or is a single fact lookup.
        researcher: The question needs careful cross-checking of several sources, or has real consequences if wrong.
        academic: A scientific, medical, or technical question best answered from papers, journals, or official documentation.
        news: About a current or recent event, where fresh news coverage matters more than reference pages.
        shopper: The person wants to find, compare, or buy a product.
        first_principles: The person wants to understand why or how something works from the ground up.
        socratic: The person wants to be guided to work something out themselves, not handed the answer.
        safari: The person explicitly wants to explore a broad topic interactively, stop by stop, over several turns.

    research:
      threshold: 0.85
      instructions: >-
        Does answering this well require searching the web or reading current information?
      options:
        yes: >-
          Needs current facts, specifics, prices, news, anything that could have changed recently,
          or a specific checkable fact about a real person/date/event (ages, release dates,
          statistics) — those are easy to get subtly wrong from memory alone.
        no: Casual conversation, writing help, brainstorming, or genuinely stable general knowledge.
      inject:
        no: >-
          This message probably doesn't need a web search — it looks answerable from general
          knowledge or the conversation so far. Answer directly unless you find you're unsure of a
          specific fact, in which case search as normal.

    high_stakes:
      threshold: 0.75
      instructions: >-
        Would acting on a wrong answer to this message risk someone's health, legal standing,
        money, or physical safety?
      options:
        none: Low stakes; a wrong answer would be an inconvenience at most.
        medical: Health, symptoms, medications, dosages, diagnoses, or treatment.
        legal: Laws, rights, contracts, disputes, taxes as a legal matter, or legal procedure.
        financial: Investing, debt, taxes, insurance, large purchases, or other money decisions.
        safety: Physical danger — electrical, chemical, structural, vehicles, weapons, outdoor risks.
      inject:
        medical: >-
          This looks like a medical question. Prefer primary clinical sources — government health
          agencies, peer-reviewed research, professional society guidance, drug labels — over
          health blogs or forums. Be exact about dosages, thresholds, and who a finding applies to.
          Say plainly where evidence is weak or mixed, and when something warrants seeing a
          clinician, say so once, clearly, without burying the answer in disclaimers.
        legal: >-
          This looks like a legal question. Law depends on jurisdiction — if the user's isn't clear
          from context or memory, say which one your answer assumes. Prefer statute text, court or
          government sources, and bar-association guidance over general-audience summaries. Note
          when an answer turns on specific facts a lawyer would need to see.
        financial: >-
          This looks like a financial decision. Prefer primary sources (regulators, official rate
          and tax tables, fund prospectuses, company filings) over promotional content, and check
          that numbers are current. Separate facts from opinion, and name the assumptions any
          recommendation depends on.
        safety: >-
          This involves physical safety. Prefer manufacturer documentation, official codes and
          standards, and safety agencies. State the specific hazard and the specific precaution
          rather than a generic warning, and don't give confident instructions for anything you
          couldn't source.
        # Applied in addition to whichever option fired above.
        any: >-
          When sources disagree on something that matters here, use compare_sources rather than
          picking one.
      # by_focus keys are focus-mode names; each inner map is keyed by the
      # option that fired, or "any" for a single override applied
      # regardless of which option won (same sentinel `inject.any` already
      # uses above) — same shape every check's by_focus uses, so Go's
      # ByFocus field type (map[string]map[string]string) doesn't need a
      # different shape per check.
      by_focus:
        brief:
          any: >-
            This looks like a {option} question. Keep the answer short as asked, but brevity trims
            explanation, not safety: keep any figure exact, never drop the one caveat that changes
            what the user should do, and still cite a primary source for it.

    intent:
      threshold: 0.65
      instructions: >-
        What kind of thing is this message mainly asking about?
      options:
        general: None of the other options clearly fits.
        place: A place, business, restaurant, or something nearby or at a specific location.
        book: A book, author, or what to read.
        film_tv: A movie, TV show, actor, or what to watch.
        music: A song, album, artist, or what to listen to.
        product: A specific product or buying decision.
        weather: Weather or a forecast.
        video: A specific YouTube video or its contents.
        code: A code repository, library, or programming project.
        definition: The meaning, pronunciation, or origin of a word.
      inject:
        place: >-
          This is about a place. nearby_search gives real listings with addresses, hours, and
          ratings — use it rather than relying on web_search alone, and include those specifics.
        book: >-
          This is about books. Use the books tool for real bibliographic data. If more than one
          title fits, briefly say how they differ (focus, tone, audience, depth) so the choice is
          easy, rather than just listing them.
        film_tv: >-
          This is about film or TV. Use the movies tool for real details (year, cast, runtime,
          where it's streaming if available). If recommending several, say what distinguishes each.
        music: >-
          This is about music. Use the music tool for real release and artist data. If recommending
          several, say what distinguishes each.
        product: >-
          This is about a product. Compare real, currently available options with actual prices
          and the tradeoffs that matter for this use — not a generic feature list.
        weather: >-
          This is about weather. Use the weather tool rather than a web search.
        video: >-
          This refers to a video. If there's a YouTube link or an identifiable video, read its
          transcript with youtube_transcript rather than guessing at its contents.
        code: >-
          This is about a code project. github_repo and github_activity give real, current repo
          data — prefer them over search results about the project.
        definition: >-
          This is about a word. Use the dictionary tool for the definition, and mention usage or
          origin if it's interesting.
      # Shopper mode already carries its own product guidance; don't send two.
      skip_option_for_focus:
        product: [shopper]
      # by_focus keys are focus-mode names (never option names — weather/
      # video/code/definition above are options, and their inject text
      # doesn't change under Brief, so they only exist in the map above,
      # not here).
      by_focus:
        brief:
          book: This is about books. Use the books tool; give the one best pick, or one line per title on how they differ.
          film_tv: This is about film or TV. Use the movies tool; give the one best pick, or one line per title on how they differ.
          music: This is about music. Use the music tool; give the one best pick, or one line per title on how they differ.
          product: This is about a product. Give the single best current option with its real price.

    clarify:
      threshold: 0.85
      first_message_only: true
      skip_for_focus: [safari]   # Safari's Embark step already asks
      instructions: >-
        Is this message ambiguous enough that the answer would be substantially different depending
        on something the person didn't say — so that asking one question first would clearly save
        wasted research?
      options:
        no: Clear enough to answer well, or any ambiguity has an obvious default.
        yes: Two or more very different readings, or a missing detail that changes everything.
      inject:
        yes: >-
          This message looks ambiguous in a way that matters. Before researching, ask one short
          clarifying question with ask_user_question, offering the two or three likeliest readings
          as options. Skip this if, on reflection, the conversation or memory already answers it.

    recall:
      threshold: 0.80
      instructions: >-
        Does this message refer back to an earlier conversation the person had with the assistant
        (for example "like we talked about", "that thing from last week", "remember when")?
      options:
        no: No reference to a past conversation.
        yes: Explicitly or clearly refers to something discussed before.
      inject:
        yes: >-
          The user seems to be referring to an earlier conversation. If memory doesn't already
          cover it, use search_chats to find it before answering rather than asking them to repeat
          themselves.

  # Offer-only checks — no injection; each yes renders a chip under the reply.
  chips:
    pulsar:
      threshold: 0.80
      instructions: >-
        Is this the kind of thing someone would want updated regularly — a price, a score, an
        ongoing story, a release date, a changing number?
      options:
        no: A one-time question.
        yes: Information that changes and would be worth checking on a schedule.
    daily:
      threshold: 0.80
      instructions: >-
        Is this something the person might want to keep an eye on each morning as part of a daily
        briefing?
      options:
        no: Not something to follow day to day.
        yes: A topic, story, or situation worth a daily glance.
    field:
      threshold: 0.75
      instructions: >-
        Which of the person's fields, if any, does this message clearly belong to?
      # options are built at request time: every field name → its
      # description, plus none → "Doesn't clearly belong to any field."
```

## UI

Explored in `mockups/oracle-mode.html` (round 2 frames copy the real app's chrome, captured by
driving a scripted turn through `dev/stack.sh --fake-llm`). **Decided with the operator
(2026-09-28, rounds 1–2):**

- **The moment — constellation (A1).** One faint star per enabled check appears under the user's
  message, lights in order, a line joins them, and the constellation folds into the margin note.
  Adding checks just adds stars. Jev answers every check in one request, so the lighting is a fixed
  ~1s choreography cut short if the first tool call arrives, not one star per real answer.
- **The result — margin note (B2).** A line of dim text above the tool calls and prose, e.g.
  "Read as **medical** · answered as **Researcher**". No note when nothing fired. Tapping it opens
  the info sheet.
- **The "why" — an ⓘ turn-info sheet (C1).** A new ⓘ button at the end of every turn's footer
  (same icon as Settings' usage stats, shown even with Oracle off) opens a sheet with the answer's
  own stats (model, time to first token, tokens/s, tokens in/out, total time, tool calls, cost)
  and then a section per Oracle check: checks that changed something get a card with runner-up
  odds, the exact nudge text, and a rerun button; checks that ran but did nothing are one
  collapsed list. Time-to-first-token and tokens/s aren't recorded today — need adding.
- **Cost moves into the sheet, in three tiers.** The per-turn cost leaves the turn footer (duration
  and the action icons stay). The sheet shows the total plus a split: **Answer** (model + paid
  tools), **Verification** (the "found in source" Jev call) and **Oracle** (its own Jev call).
  Today `gateway/turn.go` folds verification's Jev spend into the turn's single total via
  `AddTurnCost`, so the split needs separate per-message cost fields (answer / verification /
  oracle) rather than one running sum; the total stays their sum.
- **Composer — icon only, in the prism ring.** No text badge: the Oracle icon sits inside a thin
  full-hue-wheel ring (every hue at one lightness/chroma, so starlight-through-a-prism rather than
  RGB neon) inside the More button, turning while Jev reads. A manual per-message pick
  shows today's text badge instead, and the ring returns on the next message.
- **Mid-thread change (F1).** The note reads "Switched ~~Shopper~~ → **First Principles**" and
  glows once; tapping it undoes the switch for that message.
- **Offers — separate lines under the footer (7a).** Full-width lines above the follow-up
  suggestions, each with its destination's icon (Sunrise for Daily, Orbit for Pulsar, a folder for
  fields) and a verb on the right ("Add", "Set up", "Move"). The Pulsar line first derives a
  standalone recurring prompt from the whole conversation (`POST /api/pulsar/suggest`,
  `gateway/pulsar_suggest.go`) instead of seeding the form with the preceding message verbatim — a
  follow-up like "what about the second one?" is unanswerable once the routine fires with no thread
  to refer back to (found live). It reads "Writing…" while that call runs and falls back to the raw
  message if it can't produce a prompt. The Daily line does the same with `kind: "daily"` (issue
  #126), deriving a custom block's title and standing instructions instead of a routine prompt.
- **Icon — Asterism (custom).** Four stars joined by lines, drawn on Lucide's 24px grid so it sits
  next to the stock icons: it's the constellation animation's final frame, so the icon itself
  means "Oracle read this". SVG source is in `mockups/oracle-mode.html` (`#i-asterism`); it'll
  need to become a small Svelte component, since `@lucide/svelte` doesn't ship it.
- **Reduced motion**: no constellation or ring spin, just the note.
- **Sheet order**: the answer's own stats sit above the Oracle sections.
- **Note wording** ("Read as **medical** · answered as **Researcher**"): kept as is; to be judged in
  real use. Per-mode wording is in "Focus modes, one by one".

## Live spike results (2026-09-28)

Ran `dev/oracle_spike` (throwaway tool, `go run ./dev/oracle_spike`) against 50 real user messages
sampled from the potato's thread history (18 thread-openers, 32 with real prior-user-message
context), one `AskChoice` call per message across `focus`/`research`/`high_stakes`/`intent`/
`recall` (+`clarify` when first-in-thread). Full results: `/tmp/oracle_spike_results.jsonl` (not
committed — throwaway).

- **Latency badly missed the vendor figure.** p50 1876ms, p95 10164ms, max 19011ms — nowhere near
  the claimed 70–500ms this plan's "Cost and budgets" section leaned on to justify running Oracle
  unbounded and concurrent with turn setup. 6/50 calls (12%) hit `jev.Client`'s 20s timeout
  outright, plus one HTTP 520 from OpenRouter's edge — a 14% hard-failure rate in a single run, not
  a rare edge case. (Possibly a transient Jev/OpenRouter capacity issue — OpenRouter's own reported
  volume through Jev has grown sharply recently — but Oracle's design has to defend against this
  regardless of cause, since there's no lever to fix Jev's infra from here.) **Action for
  Milestone B**: give `RunOracle` its own short `context.WithTimeout` well under `jev.Client`'s 20s
  (something in the 2–3s range, tuned against more data over time), not just relying on the
  client's existing timeout — a turn's first token should not routinely wait 10+ seconds on
  Oracle. The "Jev errors/times out → proceed as Oracle-off" fallback isn't just defensive
  boilerplate; this data shows it fires often enough to matter.
- **Calibration looks solid where it matters most.** `high_stakes` correctly fired `financial`
  (conf 0.90, 0.92) on real cost-of-living/wage questions and `medical` (conf 1.00) on a
  diabetes-risk question. `focus`/`clarify` stayed appropriately quiet on ordinary thread
  continuations — `clarify` never cleared conf 0.45 across 18 first-message samples, and `focus`'s
  weak `brief` signals (0.49–0.70) correctly stayed under the (then 0.85) firing bar. The draft thresholds
  in this doc's `prompts.yaml` block all look directionally right against this sample; no changes
  made to them.
- **One real miscalibration**: `research` gave high-confidence "no" (conf 0.01, 0.44) to specific
  factual lookups like "how old was [artist] when they released [album]" — exactly the kind of
  verifiable-but-easy-to-hallucinate fact that should lean toward search, not away from it. Low
  risk today since a "no" is only a soft nudge and every tool stays available regardless, but the
  `research` check's `no` criteria description should be tightened in Milestone B to explicitly
  exclude "specific, checkable facts about people/dates/events" from "stable general knowledge."
- **`clarify` has weak true-positive coverage from this sample** — only 18 first-message prompts,
  none genuinely ambiguous, so its 0.85 threshold is unvalidated against a real positive case.
  Worth a small supplementary batch of intentionally ambiguous synthetic prompts before treating
  0.85 as final, rather than blocking the whole build on it.
- **Cost was trivial**: $0.0026 total for 50 messages × ~6 questions — confirms the plan's
  "rounding error" cost assumption, unaffected by the latency finding.

## Open questions

- **Re-run from the "why" sheet**: replace the reply in place, or append a new turn? Replace is
  cleaner; check how existing regenerate behaves first.
- **Chip dismissal memory**: if the operator dismisses "Make this a Pulsar" on a thread, don't offer
  it again on that thread. Across threads is probably overkill.

## Next steps

1. ~~**Mockups**~~ — done, `mockups/oracle-mode.html`.
2. ~~**Live Jev spike**~~ — done, see "Live spike results" above. Latency assumptions revised;
   thresholds validated as-is; one criteria-wording fix flagged for `research`.
3. **Build**: `prompts.Set` gets the `oracle` block; a new `gateway/oracle.go` runs the checks
   under its own short timeout (see spike results) and returns a result struct;
   `agent.loadSystemPrompt`/`modeReinforcement` take the injected section; a new WS event + a
   persisted per-message column; frontend chip, why sheet, smart chips, settings toggle.
4. **v2 candidates**: model/effort routing; more `intent` options as real usage shows gaps.
