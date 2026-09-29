# Oracle checks expansion

**Added: 2026-09-29.** Extends [oracle-mode.md](oracle-mode.md).

**Status: built (2026-09-29), not yet merged.** Checked live against real Jev (33/35 expected
winners on 35 hand-written prompts, p50 ~250ms / max 1.5s with all 20 questions in one call, no
timeouts) and through the real UI. The Safari chip's first wording fired on debates, plans and
loaded questions; it now asks for a broad subject to *study* and had no false positives in the
same run (it also misses some real ones — deliberate, offers are cheap to skip and annoying to
see wrongly).

## The idea

Oracle today classifies *what a message is about* (`intent`) and *how risky it is* (`high_stakes`).
This adds the missing axes: how the answer should be **shaped**, which **sources** to favor, what
**task** the person is doing, and how to handle **sensitive** messages — plus richer `intent`
options and one new offer chip. Each is still one more question in the same single Jev call, so the
cost is tokens, not round trips. More checks also means more stars in the "reading" constellation.

## New checks

| Key | Options | Nudges toward |
|---|---|---|
| `format` | none, table, comparison, steps, list, prose, code, timeline | An answer shape. `comparison` = per-option layout ending in a pick; it does **not** call `compare_sources` (that tool checks whether sources contradict each other, a different job). |
| `depth` | standard, quick, thorough | Length. `thorough` never means `spawn_researchers` or deep research — those stay manual-only. |
| `recency` | evergreen, recent, breaking | Source freshness, as-of dates, the current year in queries. Prompt-only for now: `web_search` has no time filter (#135). |
| `source_type` | any, primary_docs, community, official, academic | Which kind of source to prefer. |
| `contested` | no, yes | Present sourced perspectives; separate fact from value judgment. |
| `claim_check` | no, yes | Verify a "heard that…" claim against the original source; say true / false / misleading / unverified. |
| `locale` | no, yes | The answer depends on region — use memory for location or state the assumed one. |
| `task` | answer, explain, decide, plan, troubleshoot, write, summarize, brainstorm, calculate | What the person is doing. `troubleshoot` asks for the exact error/version and searches it; `calculate` routes to `calculator`/`code_exec`. |
| `emotional` | no, yes | Lead with acknowledgment; skip the research-dump tone. |
| `private_person` | no, yes | A named private individual (not a public figure) — don't compile a profile. |
| `premise` | no, yes | A leading question or built-in assumption — check the premise first. |

`has_url` is **not** a Jev question: a regex in `RunOracle` finds pasted links and nudges toward
`web_read` / `youtube_transcript`. It is reported as an ordinary check outcome so it gets a star and
shows in the turn-info sheet.

## `intent` gets more options

`academic_paper` (`reference_lookup`), `image` (`image_search`), `person_org`, `recipe`, `travel`,
`sports`, `event`, `datetime` (`current_time`). Existing options unchanged.

## New chip: `safari`

Under a reply on a broad, learnable topic: "Go deeper as a **Safari**". Unlike Pulsar/Daily it doesn't
navigate anywhere — it sends the next turn itself, asking for an interactive, step-by-step Safari-style
walk-through, with Safari picked as a manual focus so it can't be missed by the high Safari bar.
Hidden when the turn is already in Safari.

## The field chip

Shipped inert with Oracle v1 (no Fields feature existed); wired up 2026-09-29. "Move to **<Field>**"
files the thread under the Field Jev matched.

- **Options.** `gateway/turn.go` lists the Fields per turn and `OracleFieldOptions` turns them into
  the chip's name → description options — only when the thread isn't already in a Field (a thread
  born inside one via the composer picker counts).
- **Names aren't unique, ids are.** Jev answers with a criteria key (the name), but a move needs the
  id, so `OracleInput.FieldIDs` maps name → id and the chip carries `field_id`. A duplicate name is
  left out (most recently touched wins), a Field literally named `none` is left out (it would
  overwrite the reserved "no field" option), and a winner with no id is dropped rather than offered
  with nothing to move to.
- **Cost bounds.** At most 25 Fields, descriptions cut to 500 runes, same reasoning as the message
  cap: it's billed per token and sent to a third party. An empty description gets a name-based
  stand-in so the criteria entry isn't blank.
- **Frontend.** The click calls `appState.moveCurrentThreadToField`; the chip is filtered against
  `appState.activeFieldId`, so it disappears once the thread is in a Field however that happened.

## Live findings

- Fired-nudge stacking was real on emotional messages: "laid off, worried about health insurance"
  injected 7 paragraphs. Fixed with a generic `suppresses` rule (config `oracle.checks.<key>.suppresses`):
  when `emotional` fires it holds back `format`, `depth`, `source_type`, `task` and `clarify` —
  tone over structure, and a "stop and ask first" nudge contradicts "acknowledge them first".
  `high_stakes`, `locale` and `recency` stay, since they carry facts. Live: emotional prompts went
  from 6–7 paragraphs to 4–5; a non-emotional informational prompt is unchanged at 7. A held-back
  check reports `suppressed` and the info sheet shows "held back" instead of a bare "Quiet".
  Only a *fired* answer suppresses ("no" at 100% doesn't).
- `comparison` vs `table` overlap: a laptop comparison came back `table`. Harmless (both nudge a
  table); merge the two options if it stays noisy.
- `dev/fakeopenrouter` now treats `standard`/`any`/`answer`/`evergreen` as quiet answers, otherwise
  an unscripted question picked its alphabetically-first option and fired a nudge in every test.

## Decisions

- **No cap on injections yet.** The system prompt is ~5–6k tokens against a 200k window, and the
  `## Oracle` section is rebuilt per turn, not accumulated. Revisit if non-emotional prompts keep stacking past ~6 (a pure how-to about health insurance still hits 7, two of them from `high_stakes`' own option + compare_sources text).
- **Focus interplay.** `format`/`depth` skip under Safari; `depth` also skips under Brief (already
  short by definition). `source_type: academic` skips under Academic focus.
- **Thresholds start conservative** (0.75–0.85) with a quiet "none" default on every check, so a wrong
  guess costs nothing. Tune from the turn-info sheet's odds bars after real use.
- **Deferred:** `expertise` (one message is weak evidence — wait for memory-informed signal) and
  `has_attachment` (attachments already reach `view_image`).

## Where it lives

`prompts.yaml` + `prompts/prompts.go` (wording, kept in sync by `TestDefaults_MatchRealPromptsYAML`),
`config/oracle.go` + `config.yaml.example` (thresholds), `gateway/oracle.go` (`has_url`, safari chip
guard), `web/src/lib/oracleLabels.ts` (display), `ChatTurnView.svelte` (safari chip, star count).
