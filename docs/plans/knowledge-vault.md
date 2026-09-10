# Knowledge vault — very early brainstorm, not scoped yet

**Status: idea capture only, no design review, nothing built.** This came out of a live brainstorm
with Polaris itself (see the "Polaris Usage Trends and Recent Queries" thread on the potato,
2026-09-09/10 — ask to search past chats for it if this doc needs the full transcript again)
after using the newly-shipped `search_chats` tool to ask "what do I actually use you for?" Five
brainstorm passes (below, in order) resolved most of the open shape questions this doc originally
posed, but "resolved in a brainstorm" isn't the same as "designed and ready to build" — the next
real step is sketching a concrete schema and first narrow slice, not writing implementation code
straight from this doc.

## The core idea

Polaris auto-generates and maintains a personal knowledge library built out of everything asked
across every thread, organized by **topic, not by chat**. Ask about Cloudflare Workers four times
across four unrelated threads, and that collapses into *one* living chapter that grows over time,
not four scattered notes. Inspired by DOT (New Computer) — specifically the "it remembers you and
reflects it back" feel, not just a notes dump or a passive transcript log.

**"Vault" and "Obsidian" are inspiration/reference points, not the literal target.** This is its
own thing, native to SQLite — chapters live in the database (frontmatter-equivalent fields as real
columns: title, status, confidence, sources, tags, etc., not literal YAML) so the agents doing the
writing/merging can update them the way any other Polaris data gets updated, not by parsing and
rewriting Markdown files on every change. Obsidian-style Markdown-with-frontmatter is a target
*export format* — "assemble into `.md` on demand" — for whenever the user actually wants a real,
portable vault on disk, not the system's own storage. Same underlying principles (one note per
topic, linkable, frontmatter-carrying), different substrate. This also resolves the earlier "how
does this touch a vault on disk" question from the first pass — there's no vault-on-disk to touch
in v1 at all, just database reads/writes, same shape as every other Polaris subsystem.

## Shape that came out of the brainstorm (not decided, just sketched)

- **Hybrid save model**: obvious/factual/self-contained stuff (how Cloudflare Workers work, what
  Surveyor 1 was) auto-saves with no friction. Anything that's an inference *about the user*
  (reading tastes, location plans, mood) or otherwise low-confidence gets proposed instead —
  never auto-written, always a yes/no.
- **Structure sketch** (now a DB shape, not literal folders — see "SQLite, not a real vault"
  above; folder names below are really just status/category groupings a chapters table would
  filter on, and become real folders only at export time): chapters are either **auto** (written
  directly) or **proposed** (awaiting approval, the DB equivalent of an "Inbox"), plus some
  higher-level table-of-contents grouping ("Atlas"-equivalent) over the chapters themselves. Each
  chapter carries frontmatter-equivalent columns: `title`, `type`, `created`/`updated`,
  `status: auto|proposed`, `confidence`, `sources` (links back to the originating thread(s)),
  `tags`.
- **A thin "reflection layer"** — cross-links between chapters that surface a connection the user
  didn't ask for (Ender's Game → child psychology → ethics), and/or a weekly digest note
  ("this week you learned about X, Y, Z; you keep circling back to space and to where you might
  live"). This is the most DOT-flavored, most speculative part — explicitly optional/deferrable.
- **A private "you" layer** (deferred) — the loose personal inferences (reads literary sci-fi,
  part of Atlanta's queer community, weighing a move) stay local and are always proposed, never
  auto-written, given how sensitive that layer is.

## Decided so far (second brainstorm pass)

These firmed up enough to move out of "open questions" — still nothing built, but the shape is no
longer up for grabs on these specific points:

- **Not MCP. A totally separate agent, in a totally separate environment, with a totally
  different, narrow tool set.** This isn't Polaris's own `agent.Run` loop gaining a new tool —
  it's a distinct agent that never needs `web_search`, `calculator`, `visualize`, or anything else
  in the main catalog, because its whole job is reading/inferring from already-written chat
  content, not researching or computing anything new. Its tool set is closer to "read a thread,
  read/write the vault, maybe search the vault for an existing related chapter" — nothing else.
  Whatever this agent is called for real (working name so far is just "the DOT agent," a
  placeholder borrowed from the app that inspired this, not a real name — naming is an explicit
  bikeshed for later, not now).
- **Trigger model: an interval-based background poller, not live-per-message and not an
  unconditional nightly job.** Something like "check every hour: any new thread(s) since last
  check?" If yes, the agent goes through them. If no — **zero AI calls, full stop, silently
  skipped.** This is a hard requirement, not a nice-to-have: it directly guards against a real
  failure mode from a past project (`her-go`, a Go program simulating "Samantha" from *Her*) whose
  nightly "dream sequence" ran unconditionally every single night regardless of whether anything
  new had actually happened that day — burning real tokens for zero informational gain, every
  night, forever. This system is explicitly designed to never do that: no new input since last
  check means no LLM call is made at all, not even a cheap one.
- **A new, dedicated UI surface — not the existing thread/chat UI at all.** Described as wanting
  something "nice, slick, card-like," entirely fresh for this project, explicitly not reusing the
  thread-list/chat-transcript visual language. Framed as wanting this to feel like an actual
  second brain, a different kind of surface from "a chat you had."
- **A second input source beyond chat threads: manually saved links.** Not just things inferred
  from conversations — a way to directly hand the system a URL ("save this for later") that gets
  fetched and run through the same inference pipeline as "a thing the user is into," independent
  of whether it ever came up in a chat at all. This means the poller's "anything new to process?"
  check needs to cover saved-links-since-last-check too, not just new threads.

## Decided so far (third pass — dedup/merge)

- **Dedup/merge pipeline**: (1) one LLM call reads a new thread's effective content and proposes
  0–N candidate topics (title + short summary + confidence: obvious/factual vs. fuzzy/personal —
  zero candidates is a valid, common outcome, not an error); (2) for each candidate, a cheap FTS5
  retrieval prefilter over existing chapters' title/tags/summary (the same mechanism
  `search_chats` already built, applied to a `chapters` table instead of `messages`) pulls back
  the top handful of plausibly-related existing chapters; (3) one LLM call per candidate, given
  the new candidate plus those few existing chapter summaries, both decides *and* produces the
  merged result in the same call ("no match → new chapter" or "matches chapter X → here's X's
  updated content"), rather than a separate decide-then-merge round trip. Deliberately FTS5, not
  embeddings, for the retrieval step — same reasoning `search_chats`' own v2 (semantic search)
  was deferred for: one person's own vocabulary doesn't drift enough from itself to need it, and
  it avoids a new table/indexer/Ollama dependency for the MVP. Revisit only if real usage
  surfaces FTS5 actually missing real matches, not preemptively.
- **Ambiguous matches always route to the proposed/Inbox queue**, never an auto-merge or
  auto-new-chapter guess — "not confidently a match" is treated the same as "fuzzy/personal,"
  since a wrong auto-merge (polluting an unrelated chapter) or a silent duplicate chapter is worse
  than asking once.
- **No per-poll cap on backlog size.** Considered capping how many candidate topics a single poll
  processes (deferring the rest to the next interval) to bound worst-case cost after a long gap,
  but rejected: deferring doesn't reduce total cost, it only delays it, so there's no real benefit
  for a system with no shared/contended resource to protect — it would just elongate the process.
  A poll processes everything it finds, every time.
- **One isolated, fresh-context processing run per new thread, run sequentially — not one
  continuous session walking the whole backlog, and not concurrent runs either.** Mirrors
  `agent.SpawnResearchers`' own reasoning for isolated sub-agent contexts (narrower context per
  unit of work beats one shared blob accumulating unrelated topics), but sequential rather than
  concurrent: each run's writes commit to the DB before the next run starts, so run N's own FTS5
  retrieval step naturally sees whatever run N-1 just wrote — which is what actually prevents two
  threads in the same backlog from independently creating duplicate chapters for the same
  emerging topic, without needing a shared context or any extra coordination to get that
  self-correction. Running concurrently (Deep Research sub-agent style) would reintroduce exactly
  that race, for no benefit here since nothing needs cross-thread reasoning within one poll.

## Decided so far (fourth pass — where it lives, on/off, tone, reflection layer)

- **Working name for the execution unit: a "job."** Mirrors Pulsar's own naming (a routine fires a
  "pulse") — a placeholder until something better surfaces, same as "the DOT agent" itself, but
  good enough to write code and docs against instead of saying "the execution run" every time.
- **Lives entirely inside Polaris — same repo, same binary, same database.** Not a separate
  project or sidecar process; an extension of the search agent side of the app, using the existing
  SQLite store. Explicitly **not** expected to make the main chat agent itself any better at
  anything — this is a user-facing feature (a browsable library), not a capability upgrade for
  `agent.Run`. Worth remembering when judging whether a piece of it is "worth the engineering,"
  since the payoff is entirely in what the user gets to look at, not in smarter answers.
  Context for scale: the potato's real instance already has 160 real (non-disabled, opened)
  threads as of 2026-09-10 — a year of usage at anything like that pace is a genuinely large
  dataset for this to organize, which is the whole point of building it.
- **On/off: a dedicated settings panel section**, same shape as Pulsar's and Pulsar Daily's own
  settings sections — a plain global switch, not a per-tool toggle buried elsewhere.
- **Tone: factual, not a journal-with-a-take.** This is a library, and libraries state facts.
  Deliberately not verbose either — glanceable, not something to sit and read for a while, since
  the user already read the original thread once; a chapter should always link back to that
  original thread for anyone who wants the full context again.
- **A concrete new interaction, worth designing for even this early**: a "Continue in chat" button
  on a chapter card. Reading a Cloudflare Workers chapter and suddenly have a follow-up ("how do
  Durable Objects work")? One tap starts a fresh chat pre-loaded with a reference back to that
  chapter (an attachment-ID-style reference dropped into the omnibox, not a fully retyped
  question) so the question can just be typed and sent immediately, without re-establishing
  context by hand.
- **The reflection layer is fully wanted, not a maybe.** Explicitly not being scoped down to "just
  a clean library" — the proactive, DOT-flavored cross-linking and weekly-digest behavior is
  something to actually build, not a stretch goal to quietly drop. Doesn't have to land in the
  very first narrow slice (see below), but it's a real target, not speculative padding.

## Decided so far (fifth pass — export)

- **Export turned out not to be a real design problem.** Frontmatter is a direct, mechanical
  rendering of already-structured DB columns (title, tags, chapter/type, etc. straight into a
  `---` block) — nothing to design there. The chapter *body* is already well-formatted Markdown
  the moment it's written, since the model authors it with real headers and wikilinks as part of
  producing the chapter content in the first place, not as a separate post-processing pass over
  plain text. Export is therefore pure assembly: take the stored structured fields, take the
  stored body, concatenate, done. No remaining open questions from this doc's brainstorm passes —
  the next real step is sketching a concrete schema/first-slice, not more open-ended ideation.

## Where a start might look like, eventually (not a commitment)

Floated during the brainstorm, worth keeping attached even though nothing here is decided: don't
build the whole thing — pick one narrow slice (just books, or just tech), get auto-save + dedupe
working for that slice alone, live with it for a week, then decide whether to expand. The
reflection layer and the "you" layer are reasonable things to leave out of that first slice
specifically to keep it small — not because either is in doubt (see above, the reflection layer
is a real target), just sequencing.
