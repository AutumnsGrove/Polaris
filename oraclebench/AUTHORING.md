# Authoring Oracle-bench cases

Rules for writing `oraclebench/cases/<track>/<id>.yaml`. Design context:
`docs/plans/oracle-benchmark.md`. Schema + validator: `case.go`. Read a few seeds in
`cases/` first (e.g. `high_stakes/hs-novel-ibuprofen-wine.yaml`,
`intent/in-followup-thinkpad-after-framework.yaml`).

## What a label means

Each case is a message (plus optionally the previous one) and the answers a good decision model
should give to *specific* Oracle questions. The questions and their options live in
`prompts.yaml` under `oracle.checks` / `oracle.chips` — **read the option descriptions there; label
against them, not against your own idea of the category.** The validator rejects any check or
option that doesn't exist.

- `expect.<check>.answer` — the single best option. `accept` widens it to a defensible set and
  must include `answer`. Only widen when reasonable people would split; a wide `accept` makes the
  label cheap to pass.
- `quiet` — checks that must **not deliver a nudge** (chips: must not be offered). A check
  "delivers a nudge" when the winning option has `inject` text in `prompts.yaml`. Winning a quiet
  option (`high_stakes=none`, `emotional=no`, `intent=general`) is not a nudge. List only checks a
  human would confidently call quiet for *this* message; unlisted checks are unscored.
  - Never list `focus` in `quiet` — label `expect.focus.answer: "off"` instead.
  - `research` is the odd one: its `no` option is the nudge ("skip the search"). A price or
    current-events question should `expect research: "yes"`; list `research` in `quiet` only when a
    false "no research" would be harmful.
- `disputed_label: true` — set when informed people would split on the right answer. These are
  scored but reported separately and always judged. Explain the split in `rationale`.
- `rationale` (required) — why these labels are right: the trap or the clean case, and which
  option descriptions it rests on. A reviewer and the LLM judge both read it.
- Always **quote** answers (`"no"`, `"yes"`, `"off"`, `"none"`): bare `no`/`off` are YAML 1.1
  booleans in other tools. Option names like `steps` or `medical` don't need quotes.

## Mechanics that constrain what you can label

The bench runs the real `gateway.RunOracle`, so production's rules apply:

- `clarify` is only asked on a thread's **first** message. Never label it when `state.previous`
  is set (validator enforces this).
- `emotional=yes` **suppresses** `format`, `depth`, `source_type`, `task`, `clarify`, `ui`. On a
  case that expects `emotional: "yes"`, don't label those — they can't be scored.
- `ui` and `depth` are skipped when focus resolves to `brief` or `safari`; `format`, `clarify`
  and `task` are skipped under `safari`. Don't pair a `focus: brief|safari` label with `ui`/`depth`
  labels (and `safari` with `format`/`clarify`/`task`).
- `focus: brief` is never picked on a high-stakes turn (`never_with_high_stakes`). Don't label
  `focus: brief` together with a non-`none` `high_stakes`.
- `ui` firing suppresses `format`. Labeling both is fine (raw answers are scored either way) but
  keep them consistent (`ui: steps` with `format: steps`).
- Sticky focus: `state.prior_oracle_focus` sets a mode Oracle chose earlier (needs
  `state.previous`). `safari` is sticky: it is only left when another mode wins at the
  `switch_threshold` (0.85). Use this for sticky/switch focus cases.
- Chips: keys are `chip_pulsar`, `chip_daily`, `chip_safari`, `chip_field`. `chip_field`'s answers
  are the fixture fields in `case.go` (`Home Lab`, `Cooking`, `Trip Planning`, `Health`) or
  `"none"`. `thread_source: pulsar` withholds every chip (use for "no chip" cases).
- `deep_research` (yes/no) is an **action**, not a nudge: a `yes` that clears 0.85 promotes the
  turn to Deep Research (expensive sub-agents), so a false `yes` costs real money (fp 2.5) while a
  missed one just gives a normal answer (fn 1.0). It has no inject text; the bench counts the
  promotion itself. It is held back by `emotional=yes` and `clarify=yes` (don't label
  `deep_research` alongside either) and skipped under focus `brief`/`safari` (don't pair them).
  The bench environment offers it on every case (`OffersDeepResearch`).
- Each message is truncated to 2000 runes; keep pasted-document cases under ~1500 characters.
- The bench runs with the Prism dial at `normal`, so `ui` is always asked.
- `has_url` is a regex, not a model question — never labeled. A message containing a URL is still
  a fine input for other checks.

## Writing good prompts

The operator uses this mostly from a phone. Mix voices roughly **60% clean / 40% real-phone-typing**:
lowercase, no punctuation, fragments, a typo or two, dropped words, abbreviations, run-ons. Vary
length (a few words to a few sentences; a handful of longer pasted-text cases). Don't make every
message a neat self-contained question.

Mix of case types within each track, roughly: **40% clear positives**, **30% clear negatives**
(the label is the quiet option — these catch false positives, the failure that matters most), and
**30% traps** (false-friend words, wrong-frame content, near-misses, follow-ups that only resolve
via `state.previous`). Don't make every answer "yes".

Each case should stress 1–3 checks plus a quiet list. Add bonus labels (`intent`, `task`,
`research`, ...) only where they are unambiguous, so option coverage grows without label noise.
Don't use real private individuals; invent names.

## Naming and layout

- Directory = track name. File `<prefix>-<slug>.yaml`, `id` identical to the filename stem,
  lowercase kebab-case.
- Prefixes: focus `fo`, high_stakes `hs`, emotional `em`, premise `pr`, private_person `pp`,
  research `rs`, intent `in`, clarify `cl`, format `fm`, ui `ui`, depth `dp`, recency `re`,
  source_type `st`, contested `co`, claim_check `cc`, locale `lo`, task `tk`, recall `rc`,
  deep_research `dr`, chips `ch`.
- Ids are unique across the whole bank. Read the existing files in your track dir first and
  don't duplicate a seed's scenario.

## Validate

```
ORACLEBENCH_CASES=cases/<track> go test ./oraclebench/ -run RealBank
```

(from the repo root; `ORACLEBENCH_CASES` is relative to `oraclebench/`; comma-separate several
dirs). The full bank is `go test ./oraclebench/`.
