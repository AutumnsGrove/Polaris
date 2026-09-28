# Oracle mode

**Added: 2026-09-28.**

**Status: planning — nothing built yet.** Graduated from `crazy-ideas.md`'s "Oracle mode" entry after
a design pass with the operator. Mockups are the next step (`mockups/oracle-mode.html`), then a live
Jev spike against real prompts before any Go gets written.

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
  project", "Add to Daily" under the reply; nothing happens unless tapped.

## How it works

### Where it runs

`gateway/turn.go`'s turn setup, before `agent.loadSystemPrompt` builds the system prompt. When Oracle
mode is on:

1. Build Jev's `state`: the current user message, plus the **previous user message** in this thread
   if there is one. A follow-up like "what about the second one?" is unclassifiable on its own; one
   prior message fixes most of that and stays far below Jev's 32k-token context.
2. Send every enabled check from `prompts.yaml`'s `oracle.checks` as one `AskChoice` request.
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

### Clarify only on the first message

The `clarify` check only runs on a thread's first message. Mid-thread, history usually resolves the
ambiguity already, and a clarifying question there reads as the model forgetting the conversation.

### Cost and budgets

Jev's claimed latency is 70–500 ms and its input price $0.042/MTok, output free (vendor figures, not
yet measured on the potato — see source-verification.md). A few hundred tokens of state × ~7
questions is a rounding error per turn. Oracle's spend should count against the existing Jev monthly
cap in `gateway/verification.go` (`jevMonthlyCapUSD`) rather than a separate one; if the cap is hit,
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
| `chip_project` | project names + `none` | "Move to *Project*" — only asked when the thread isn't already in a project, and only if projects exist. Option set is built at request time from the store, not from `prompts.yaml`. |

Chip checks run in the same Jev request as everything else, so the chips are ready well before the
LLM-generated follow-up suggestions — they render **above** those suggestions.

**`high_stakes` and focus interplay**: if `high_stakes` fires and neither the operator nor the `focus`
check picked a mode, focus becomes Researcher. If `focus` picked something else with confidence
(e.g. Brief), that pick stands — the high-stakes injection still applies, and it's written to hold up
on its own regardless of focus mode.

**Verification**: per-claim "found in source" verification already runs on every turn
(`gateway/turn.go`), so high-stakes doesn't need to switch it on. The high-stakes nudges instead push
the model toward sources worth verifying (primary, authoritative) and toward `compare_sources` when
sources disagree.

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
      instructions: >-
        Which answering style best fits this message? Pick "off" unless one style is clearly a
        better fit than a normal, balanced answer.
      options:
        off: A normal balanced answer fits; no special style is clearly better.
        brief: The person wants a quick fact or a short answer, not an explanation.
        researcher: The question needs careful cross-checking of several sources, or has real consequences if wrong.
        academic: A scientific, medical, or technical question best answered from papers, journals, or official documentation.
        news: About a current or recent event, where fresh news coverage matters more than reference pages.
        shopper: The person wants to find, compare, or buy a product.
        first_principles: The person wants to understand why or how something works from the ground up.
        socratic: The person wants to be guided to work something out themselves, not handed the answer.
        safari: The person wants to explore a broad topic interactively, stop by stop.

    research:
      threshold: 0.85
      instructions: >-
        Does answering this well require searching the web or reading current information?
      options:
        yes: Needs current facts, specifics, prices, news, or anything that could have changed recently.
        no: Casual conversation, writing help, brainstorming, or stable general knowledge.
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

    clarify:
      threshold: 0.85
      first_message_only: true
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
    project:
      threshold: 0.75
      instructions: >-
        Which of the person's projects, if any, does this message clearly belong to?
      # options are built at request time: every project name → its
      # description, plus none → "Doesn't clearly belong to any project."
```

## UI

To be explored in `mockups/oracle-mode.html`. Starting direction from the design discussion:

- **Settings**: one toggle, "Oracle mode", with a one-line explanation.
- **Composer**: when Oracle is on, the "More" trigger reads **Oracle** instead of a focus-mode name,
  so it's obvious the steering has been handed over. Manually picking a mode for one message still
  works and shows that mode instead.
- **"Reading the stars"**: while Jev runs, a few faint points of light (one per check) brighten as
  answers return and join into a small constellation — covering the classification latency with
  something that means something. Night sky, not sparkle-burst; no generic AI "✨".
- **The Oracle chip**: the constellation settles into a quiet chip at the top of the reply naming
  what fired — e.g. `✦ Researcher · Medical`. Nothing fired → a bare glyph or no chip at all (open
  question for the mockups).
- **"Why" sheet**: tapping the chip shows each check's winner and probability ("Researcher 82% ·
  off 11%") and the injected nudges, with any alternative tappable to re-run the turn with that
  choice instead.
- **Mid-thread change**: when Oracle changes the focus mode partway through a thread, the chip
  briefly glows so the flip is never silent.
- **Smart chips**: "Make this a Pulsar" / "Add to Daily" / "Move to *Project*", above the follow-up
  suggestions.
- **Reduced motion**: no animation, just the chip.

## Open questions

- **Nothing fired**: show a bare Oracle glyph (so it's clear Oracle ran and chose nothing), or no
  chip at all (calmer)? Mockups should show both.
- **Re-run from the "why" sheet**: replace the reply in place, or append a new turn? Replace is
  cleaner; check how existing regenerate behaves first.
- **Does Jev handle short, conversational prompts well?** Every live spike so far was
  claim-vs-source verification over real documents; classifying a 6-word question is a different
  shape. The spike needs to answer this before anything else.
- **Chip dismissal memory**: if the operator dismisses "Make this a Pulsar" on a thread, don't offer
  it again on that thread. Across threads is probably overkill.

## Next steps

1. **Mockups** (`mockups/oracle-mode.html`): reading-the-stars animation variants, chip variants,
   why sheet, smart chips, settings toggle, composer state.
2. **Live Jev spike**: run the draft checks above against ~50 real prompts pulled from the potato's
   thread history via `curl` against OpenRouter's `/systemone`, hand-grade the results, measure
   latency, and set real thresholds. Same "spike before implementing" pattern as
   source-verification.md.
3. **Build**: `prompts.Set` gets the `oracle` block; a new `gateway/oracle.go` runs the checks and
   returns a result struct; `agent.loadSystemPrompt`/`modeReinforcement` take the injected section;
   a new WS event + a persisted per-message column; frontend chip, why sheet, smart chips, settings
   toggle.
4. **v2 candidates**: model/effort routing; more `intent` options as real usage shows gaps.
