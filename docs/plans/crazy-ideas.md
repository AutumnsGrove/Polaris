# Crazy ideas — brainstorm parking lot

Not a plan for any one feature — a running list of out-there directions surfaced by looking at
what Polaris already has lying around (mirrors how Constellation itself started: `search_chats`
turned up "what do I actually use you for," and the Weaver idea fell out of noticing `stars`+
`star_edges` could be more than a notes dump). Each entry below came from a live brainstorm
session (2026-09-19) and carries the operator's actual verdict, not just the pitch — treat a
"skip"/"shelved" line as a real decision, not an oversight, until it's explicitly revisited.

None of these have design docs yet. When one gets picked up for real, it gets its own
`docs/plans/<name>.md` the way Constellation did, and this entry gets a pointer added the way
`knowledge-vault.md`/`report-generator.md` point at what superseded them.

## Green-lit — moving to mockups next

### Tides — the monthly/yearly retrospective (working name "Perihelion" — rejected, see below)

A Wrapped-style retrospective mined entirely from data Polaris already has timestamped:
`threads`/`messages`, `search_history`, `stars` (formed/updated in the period), and `api_usage`
cost data. Rendered as one page in the night-sky visual language, not a text report.

**Verdict: love it, scope hard on output shape.** The real risk isn't build cost, it's becoming a
wall of generated prose — the opposite of "calm over clever." This has to read as a small number
of strong visual/numeric surfaces (a handful of stat tiles, maybe one chart, a short list of
what spiked and what quietly stopped), not a synthesized essay. Minimal by construction, not by
prompt-instruction alone — lean on `code_exec`+`matplotlib` and fixed layout slots rather than
open-ended generation, so there's a hard ceiling on how much text can come out regardless of what
the model decides to write.

**Naming: settled on Tides.** "Perihelion" is cool but tells you nothing (it's an orbital-mechanics
term — closest approach to the sun — which reads as *arbitrary* cosmic flavor rather than
*informative* cosmic flavor, unlike Polaris/Atlas/Pulsar/Constellation which are all doing real
conceptual work for their feature). Operator's stated ceiling: **Pulsar is about as far out as the
naming should go** — anything more obscure than that reads as reaching. First round of
alternatives (Almanac, Long Exposure, Logbook, Field Notes) didn't land either — operator wanted a
genuinely different angle rather than another word from the same well the app was already deep in.
Second round moved into the *nautical* half of the brand (README's own "you're lost at sea"
framing, which the astro naming had left unused) — **Tides** won: the ebb and flow of what you
cared about over a stretch of time, rhyming with Pulsar's own periodic-signal framing without
repeating it, plain word, zero jargon. Ruled out along the way: **Digest** — already means
something specific (Constellation's weekly digest banner, `docs/plans/constellation.md`'s "The
weekly digest" section) and reusing it here would collide.

**Cost note:** operator flagged this "would cost a bunch per run" — true of a naive full-history
synthesis pass, but this only needs to run monthly (or yearly) like Pulsar Daily's daily cadence,
not per-message, so absolute per-run cost matters less than keeping any one run from ballooning
(no full-thread re-reads — aggregate from `search_history`/`stars`/`api_usage` the way `Stats`
already does, not raw transcript synthesis).

## Liked, needs a real design pass before it's buildable

### Talk to your Constellation

A focus mode that answers only from `stars` (no web search) — self-reflection grounded strictly
in facts Polaris already extracted about you, the mirror image of the normal triangulate-against-
the-world mode.

**Verdict: like the premise, open problem on injection.** Memory's approach (auto-inject
everything into the system prompt) doesn't scale here the way it does for `memories` — operator
is at **~115 stars after ~2 months of real use**, and that number only grows. Full-inject would
regularly out-cost the actual question being asked. Needs a retrieval step instead of a dump:
something closer to `search_stars`' existing FTS5 lookup (`stars_fts`) feeding only the
relevant stars into context per-turn, or a tool the model calls on demand
(`tools/search_stars.go` already exists for Weaver's own internal use — the open question is
whether the *main* assistant gets a comparable tool, which is exactly the shelved question
`docs/plans/stars-tool.md` already parked pending a quality check on the personal-star reprocess).
Worth re-reading that doc before designing this, since it's the same underlying tradeoff.

### Radio Daily

Script Pulsar Daily's edition as one continuous narrated audio show (Kokoro TTS, already used
for read-aloud) instead of a page to scroll — a local, sourced "morning show" for a commute,
playing to "mobile is the primary surface, sessions are often on the move."

**Verdict: big fan, needs prompt-level scoping.** Two concrete constraints before this is
buildable: (1) narration has to be genuinely tight, not a read-aloud of the written edition
verbatim — over-length audio is worse than over-length text since you can't skim it; (2) needs an
explicit filter step to strip anything link-shaped or citation-shaped from the narration script
(a URL or a bare source chip read aloud is dead air), separate from whatever drives the citations
shown in the text edition alongside it.

### Finance / shoebox mode

Attach a bank CSV, get real categorized spend analysis via `code_exec`'s pandas/numpy sandbox
(network-less, so no concerns about financial data leaving the box), optionally promoted to a
recurring Pulsar routine for a monthly check-in.

**Verdict: real logistical opportunity, operator had already been circling this independently.**
`code_exec` is the actual unlock here — this needs closer scoping than the others got (categorization
approach, what "monthly check-in" pulls forward from the previous run so it doesn't re-litigate
already-categorized spend) but no open design objection surfaced. Next real step, once picked up:
its own `docs/plans/` doc.

## Shelved

### Travel log (nearby_search + location broker → auto travel diary)

**Verdict: skip for now, not rejected outright.** Operator rarely asks for location data in
practice, so the premise (a travel diary falling out of normal usage) doesn't have enough signal
to build against yet. Revisit if usage patterns change.

## Rejected

### Stars as an MCP server (expose `stars`/`star_edges` read-only to other agents)

**Verdict: skip.** No further notes — didn't land.
