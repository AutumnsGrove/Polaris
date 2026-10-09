# Oracle mode

Oracle reads each message before Polaris answers and quietly adjusts how the answer is produced:
which answering style to use, which tool to reach for, what kind of source to trust, how long or
how structured the reply should be, and when to slow down. It's opt-in (Settings → Oracle mode) and
every decision it makes is visible on the reply and undoable.

Design history and rationale live in [plans/oracle-mode.md](plans/oracle-mode.md) and
[plans/oracle-checks-expansion.md](plans/oracle-checks-expansion.md); this page is the reference for
what it checks today.

## How it works

1. When you send a message, Polaris asks a small classifier (Jev, through your existing OpenRouter
   key) a set of multiple-choice questions about it. All of them go out in **one** request, so adding
   a check costs a few tokens, not another round trip. Typical latency is a few hundred milliseconds.
2. Each answer comes back with a probability. A check **fires** only if its winning option clears that
   check's threshold — below it, Oracle stays quiet rather than guess.
3. What fires becomes short notes in an `## Oracle` section of the system prompt. They're framed to
   the model as hints from an automatic pre-read, not instructions from you, so a wrong guess gets
   ignored instead of obeyed.
4. The reply shows what happened: a small constellation animates while Oracle reads (one star per
   check), then folds into a margin note like *Read as a fix · answered as Researcher*. Tap it for the
   full breakdown — every check's winner and odds, which nudges fired, and **Rerun without Oracle**.

Guarantees worth knowing:

- **It never blocks a turn.** If Jev is unconfigured, errors, or takes longer than 2.5 seconds, the turn
  runs exactly as if Oracle were off.
- **Manual always wins.** A focus mode you pick for a message is never overridden; Oracle still reports
  what it would have picked.
- **It only nudges.** Oracle never removes tools from a turn, and it never turns on deep research —
  that stays a deliberate choice.
- **Ghost threads are your call.** By default a ghost (ephemeral) conversation skips Oracle entirely. Settings → Oracle mode has a second switch, **Also in ghost conversations**, that lets Oracle read and steer those turns too — but only the checks and focus mode: the offer chips that create something permanent (Pulsar, Daily, Field) stay hidden there, since a conversation meant to leave no trace shouldn't offer to file itself somewhere. Only the opening of long messages is sent to the classifier either way.

## Checks

Checks come in two kinds. **Nudge checks** add a short note to the system prompt when they fire.
The **focus** check picks an answering style instead. "Nudge — Yes" below means that option adds a
note; an option marked "—" is the quiet default and changes nothing.

### Answering style — `focus`

Picks a focus mode for the thread. The classifier is asked: Which answering style best fits this message? Pick "off" unless one style is clearly a better fit than a normal, balanced answer.

| Option | When it applies | Nudge |
|---|---|---|
| `off` | A normal balanced answer fits; no special style is clearly better. | Sets the mode |
| `brief` | The message itself asks for a short answer (quick question, tl;dr, one word) or is a single fact lookup. | Sets the mode |
| `researcher` | The question needs careful cross-checking of several sources, or has real consequences if wrong. | Sets the mode |
| `academic` | A scientific, medical, or technical question best answered from papers, journals, or official documentation. | Sets the mode |
| `news` | About a current or recent event, where fresh news coverage matters more than reference pages. | Sets the mode |
| `shopper` | The person wants to find, compare, or buy a product. | Sets the mode |
| `first_principles` | The person wants to understand why or how something works from the ground up. | Sets the mode |
| `socratic` | The person wants to be guided to work something out themselves, not handed the answer. | Sets the mode |
| `safari` | The person explicitly wants to explore a broad topic interactively, stop by stop, over several turns. | Sets the mode |

*fires at **70%+**; a mode Oracle already set changes only at 85%+; higher bars: `brief` 80%, `safari` 85%; sticky: `safari` (never switched away from once set); never picked when `high_stakes` fired: `brief`.*

## Trust and sourcing

How careful to be, and where to look.

### Needs a search — `research`

 Does answering this well require searching the web or reading current information?

| Option | When it applies | Nudge |
|---|---|---|
| `yes` | Needs current facts, specifics, prices, news, anything that could have changed recently, or a specific checkable fact about a real person/date/event (ages, release dates, statistics) — those are easy to get subtly wrong from memory alone. | — |
| `no` | Casual conversation, writing help, brainstorming, or genuinely stable general knowledge. | Yes |

*fires at **85%+**.*

### High stakes — `high_stakes`

 Would acting on a wrong answer to this message risk someone's health, legal standing, money, or physical safety?

| Option | When it applies | Nudge |
|---|---|---|
| `none` | Low stakes; a wrong answer would be an inconvenience at most. | — |
| `medical` | Health, symptoms, medications, dosages, diagnoses, or treatment. | Yes |
| `legal` | Laws, rights, contracts, disputes, taxes as a legal matter, or legal procedure. | Yes |
| `financial` | Investing, debt, taxes, insurance, large purchases, or other money decisions. | Yes |
| `safety` | Physical danger — electrical, chemical, structural, vehicles, weapons, outdoor risks. | Yes |

*fires at **75%+**.*

### Freshness — `recency`

 How much does the age of the information matter for answering this well?

| Option | When it applies | Nudge |
|---|---|---|
| `evergreen` | Stable knowledge; older sources are fine. | — |
| `recent` | Things that change over months — software versions, prices, rankings, "best X", policies. | Yes |
| `breaking` | Something happening now or in the last few days — news, live events, scores, outages. | Yes |

*fires at **75%+**.*

### Source type — `source_type`

 What kind of source would answer this best? Pick "any" unless one kind is clearly better than the general web.

| Option | When it applies | Nudge |
|---|---|---|
| `any` | General sources are fine. | — |
| `primary_docs` | Official documentation, specifications, manuals, or a company's own published material. | Yes |
| `community` | Real-world experience — "is it worth it", "what's it actually like", reliability, common problems. | Yes |
| `official` | A government, regulator, standards body, or other institution is the authority. | Yes |
| `academic` | Peer-reviewed research, journals, or preprints. | Yes |

*fires at **75%+**; `academic` nudge skipped under Academic focus (it already carries its own guidance).*

### Contested topics — `contested`

 Is this a topic where informed people or credible sources genuinely disagree — politics, social or moral questions, or actively disputed science — rather than a matter of fact?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | A factual question, or one with broad agreement. | — |
| `yes` | A genuinely contested question where reasonable people or credible sources disagree. | Yes |

*fires at **80%+**.*

### Claim check — `claim_check`

 Is the person asking whether something they heard, read, or saw is true — a rumor, a viral claim, a screenshot, a "supposedly", or "is it true that"?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | Not a request to verify a claim. | — |
| `yes` | Asks whether a specific claim is true. | Yes |

*fires at **80%+**.*

### Location-dependent — `locale`

 Does the right answer depend on where the person is — a country, state, city, or region — such as laws, prices, availability, regulations, or local services?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | The answer is the same everywhere. | — |
| `yes` | The answer varies by place, and the message doesn't make the place fully clear. | Yes |

*fires at **75%+**.*

### Loaded premise — `premise`

 Does the message assume something as true that may not be — a loaded question, a one-sided framing, or a "why is X so much better than Y" that presupposes the answer?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | No questionable assumption built into the question. | — |
| `yes` | The question rests on an assumption that should be checked first. | Yes |

*fires at **85%+**.*

## What it's about and what you're doing

### Topic — `intent`

 What kind of thing is this message mainly asking about?

| Option | When it applies | Nudge |
|---|---|---|
| `general` | None of the other options clearly fits. | — |
| `place` | A place, business, restaurant, or something nearby or at a specific location. | Yes |
| `book` | A book, author, or what to read. | Yes |
| `film_tv` | A movie, TV show, actor, or what to watch. | Yes |
| `music` | A song, album, artist, or what to listen to. | Yes |
| `product` | A specific product or buying decision. | Yes |
| `weather` | Weather or a forecast. | Yes |
| `video` | A specific YouTube video or its contents. | Yes |
| `code` | A code repository, library, or programming project. | Yes |
| `definition` | The meaning, pronunciation, or origin of a word. | Yes |
| `academic_paper` | A specific research paper, study, or preprint, or what the research says on a narrow topic. | Yes |
| `image` | The person wants to see photos or pictures of something. | Yes |
| `person_org` | A specific person, company, or organization — who they are, what they did, background. | Yes |
| `recipe` | A recipe, cooking technique, ingredient substitution, or food question. | Yes |
| `travel` | Planning a trip — destinations, routes, itineraries, visas, or what to do somewhere. | Yes |
| `sports` | A sports score, schedule, standing, roster, or result. | Yes |
| `event` | When something happens — a release date, showtimes, an event, a deadline, a schedule. | Yes |
| `datetime` | The current time or date somewhere, a time zone conversion, or time until or since something. | Yes |

*fires at **65%+**; `product` nudge skipped under Shopper focus (it already carries its own guidance).*

### Task — `task`

 What is the person trying to do? Pick "answer" for an ordinary question.

| Option | When it applies | Nudge |
|---|---|---|
| `answer` | An ordinary question wanting a fact or an answer. | — |
| `explain` | They want to understand something — how or why it works. | Yes |
| `decide` | They're weighing a choice and want help making it. | Yes |
| `plan` | They want a plan, schedule, itinerary, or sequence of steps to achieve something. | Yes |
| `troubleshoot` | Something is broken or misbehaving and they want it fixed, often with an error message. | Yes |
| `write` | They want something written or rewritten — an email, a message, a piece of text. | Yes |
| `summarize` | They want something condensed — a document, article, thread, or pasted text. | Yes |
| `brainstorm` | They want ideas, options, or angles, not a single correct answer. | Yes |
| `calculate` | They want a number worked out — math, a conversion, a split, a date span. | Yes |

*fires at **75%+**; skipped under Safari focus; `explain` nudge skipped under First Principles, Socratic focus (it already carries its own guidance).*

## Answer shape

### Format — `format`

 What shape of answer would serve this message best? Pick "none" unless one clearly beats ordinary well-organized prose for what was asked.

| Option | When it applies | Nudge |
|---|---|---|
| `none` | No particular shape is clearly better; the model's own judgment is fine. | — |
| `table` | Several items compared across the same attributes (specs, prices, pros/cons, options side by side). | Yes |
| `comparison` | The person is choosing between specific options and wants to know which to pick. | Yes |
| `steps` | A procedure or how-to where order matters. | Yes |
| `list` | A set of discrete items — recommendations, examples, ideas — with no strong ordering. | Yes |
| `prose` | An explanation, story, or reasoning that reads best as connected paragraphs, not fragments. | Yes |
| `code` | The answer is mostly code, a command, or a config snippet. | Yes |
| `timeline` | A sequence of events over time, a history, or a schedule. | Yes |

*fires at **75%+**; skipped under Safari focus.*

### Visual blocks (Prism) — `ui`

 Would a structured visual block serve this message clearly better than ordinary prose? Pick "none" unless the answer is really one of these shapes and prose would be harder to scan.

| Option | When it applies | Nudge |
|---|---|---|
| `none` | Prose, a short list, or code serves this best. | — |
| `compare` | Choosing between specific options across shared attributes. | Yes |
| `steps` | A procedure where order matters. | Yes |
| `choose` | The person asks which to pick or what to do, and the best answer depends on their own situation (usage, budget, location, goals), so a few if-then rules serve them better than a table of attributes. | Yes |
| `checklist` | Things to prepare, pack or tick off. | Yes |
| `timeline` | A sequence of dated events, a history, or a schedule. Not a price, number or trend over time. | Yes |
| `procon` | One thing weighed for and against. | Yes |
| `facts` | An at-a-glance summary of one named thing (a product, place, person or organization). | Yes |

*fires at **70%+**, or **85%+** when the Prism setting is Low; never asked when Prism is Off or in a voice call; skipped under Brief and Safari focus; when it fires, holds back: `format`.*

### Depth — `depth`

 How much detail does this message call for? Pick "standard" unless it clearly wants something noticeably shorter or more thorough than a normal answer.

| Option | When it applies | Nudge |
|---|---|---|
| `standard` | A normal-length answer fits. | — |
| `quick` | A casual, low-effort question where a couple of sentences is the right size. | Yes |
| `thorough` | A complex or open-ended question where a short answer would leave out what matters. | Yes |

*fires at **80%+**; skipped under Safari, Brief focus.*

## The conversation

### Clarify first — `clarify`

 Is this message ambiguous enough that the answer would be substantially different depending on something the person didn't say — so that asking one question first would clearly save wasted research?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | Clear enough to answer well, or any ambiguity has an obvious default. | — |
| `yes` | Two or more very different readings, or a missing detail that changes everything. | Yes |

*fires at **85%+**; first message of a thread only; skipped under Safari focus.*

### Past chats — `recall`

 Does this message refer back to an earlier conversation the person had with the assistant (for example "like we talked about", "that thing from last week", "remember when")?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | No reference to a past conversation. | — |
| `yes` | Explicitly or clearly refers to something discussed before. | Yes |

*fires at **80%+**.*

## Sensitivity

These change tone more than research, so they have high bars.

### Distress — `emotional`

 Is the person going through something painful or stressful — grief, fear, a health scare, a breakup, a job loss, feeling overwhelmed — and not only asking for information?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | A plain informational or practical message. | — |
| `yes` | Clearly distressed or dealing with something hard, beyond a factual question. | Yes |

*fires at **85%+**; when it fires, holds back: `format`, `depth`, `source_type`, `task`, `clarify`, `ui`.*

### Private individuals — `private_person`

 Is the message asking for information about a specific named individual who is a private person rather than a public figure — a neighbor, coworker, ex, classmate, or stranger?

| Option | When it applies | Nudge |
|---|---|---|
| `no` | No private individual, or the person is a public figure or is discussed in general. | — |
| `yes` | Asks about a named private individual. | Yes |

*fires at **85%+**.*

## Link detector — `has_url`

Not a classifier question: a pattern match on the message itself. If it contains a link, Oracle
nudges the model to read the page with `web_read` (or `youtube_transcript` for a YouTube video)
instead of guessing from the URL. It always fires when a link is present, is skipped under Safari
focus, and shows up in the info sheet like any other check.

## Offers

Offer chips don't change the answer — they appear under the reply when a follow-up is worth
suggesting.

| Chip | Offered when | What tapping it does |
|---|---|---|
| `pulsar` (80%+) | The answer is something that changes and would be worth re-checking on a schedule. | Set up a Pulsar routine (opens `/pulsar` seeded with the question). |
| `daily` (80%+) | The topic is worth a daily glance. | Add it to Pulsar Daily (opens `/daily` seeded with the topic). |
| `safari` (85%+) | A broad subject to study in depth — a field, era, system or concept — not a decision, plan, debate or one-reply question. | Sends the next message itself, asking for an interactive Safari-style walk-through, with Safari picked as the focus. Hidden when the turn is already in Safari. |
| `field` (75%+) | The message clearly belongs to one of your Fields, and this thread isn't in one yet. | Files the thread under that Field, with a confirmation toast. Jev is shown each Field's name and description (up to 25 of the most recently used). |

In a ghost conversation (with **Also in ghost conversations** on), only `safari` is offered — `pulsar`, `daily`, and `field` all create or move something permanent, which contradicts a thread that vanishes when it ends.

## Tuning

- **Thresholds and rules** live in the `oracle:` block of `config.yaml` (see `config.yaml.example` for
  every default, commented). Anything you leave out uses the shipped value, and edits apply on the
  next message without a restart. Besides thresholds, a check can be skipped under certain focus
  modes (`skip_for_focus`), have one option's nudge skipped under a mode (`skip_option_for_focus`),
  run only on a thread's first message (`first_message_only`), or hold other checks back when it
  fires (`suppresses` — how a distressed message avoids also getting a table and a step plan).
- **Wording** — the questions asked and the notes injected — lives in `prompts.yaml` under `oracle:`,
  also hot-reloaded. Every check named there needs a matching rule in config, or it stays off.
- **Cost** — each turn's classifier cost is logged and counted toward Oracle's monthly budget; past it,
  Oracle sits out until the month rolls over.
- **Adding a check** means an entry in `prompts.yaml` and `prompts/prompts.go`, a rule in
  `config/oracle.go` and both example configs, and a display row in `web/src/lib/oracleLabels.ts`.
  Tests fail if any of those is missing, and if this page doesn't mention the check.
