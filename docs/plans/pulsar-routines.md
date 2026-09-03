# Pulsar — recurring routines, v1 plan

Pulsar is a new primitive: a saved prompt that fires on a schedule instead of when you type
it, running through the exact same agent/turn pipeline as any other message. Named for the
astronomical object, not just for the celestial register Polaris/Atlas already sit in — a
pulsar's defining trait is a beam swept past on a fixed interval, which is the actual shape of
the feature (one routine, run repeatedly, each run a "pulse"), not just a themed label. Icon:
`Orbit` (`@lucide/svelte`) — a cycle drawn as a cycle, chosen over `Radar`/`RadioTower`/
`Antenna`/`Radio`/`Activity`/`Signal`/`SatelliteDish` specifically for staying in the
celestial-mechanics family `Compass` (Atlas) and `Telescope` (Polaris) already established,
rather than borrowing radio/comms iconography that reads closer to PRODUCT.md's "hacker tool"
anti-reference.

## Why this exists

Everything Polaris does today is reactive — you ask, it answers. There's no way to say "keep
checking this for me" without manually re-asking. Pulsar covers two shapes of that need with one
mechanism: a narrow watch ("daily updates on the openclaw repository") and a broad standing
digest ("weekly Guild Wars 3 news"). Both are the same primitive — prompt + schedule — so v1
ships one engine, not two features.

## Prerequisite: persisted turn config

Before Pulsar can be built, threads need to remember more about themselves than they do today.
Confirmed by reading the actual code, not assumed:

- `threads.model` (`store.go:24`) **is** already a persisted column — but it's a historical
  record written by `CreateThread`/each turn for cost-tracking, never read back into the live
  composer selector. Reopening a thread shows what model it *last* answered with; the selector
  itself stays on the global `appState.selectedModel`, unaffected by which thread you're looking
  at.
- `focus_mode` and `deep_research` have **no persistence at all** — both are purely local
  `ComposerMenu.svelte` `$bindable` state (`focusMode`, `deepResearch`), sent per-turn in
  `ClientMessage` (`gateway/protocol.go`), reset on reload or thread switch.

Net effect today: leave a thread with Luna selected, Researcher focus on, Deep Research on;
come back; none of it followed you. Fixing this is its own small, generally useful piece of
work — independent of Pulsar, but Pulsar depends on it — and the two should ship as one slice.

**Resolved: `threads.model` is repurposed, not shadowed.** It stops being a historical
"what this thread last answered with" record and becomes what the thread is configured as —
sticky by default, written through on every change, read back into the composer selector on
open. Nothing needs a parallel column for model. `focus_mode` and `deep_research` get equivalent
new columns on `threads` following the same sticky pattern (no precedent to repurpose, since
neither persists anywhere today).

- **The same three-field shape is Pulsar's own routine config** — not a parallel concept. A
  Pulsar routine stores exactly this triple, plus its prompt and schedule. This is also where
  per-routine model selection (e.g. running one routine on Luna while the thread default stays
  DeepSeek V4 Flash) falls out for free rather than needing separate design — it's just filling
  in the same three fields a routine already carries.

## Pulse execution model

A pulse is a real thread, not a second-class record needing its own viewer or citation handling:

- `threads.source` is already an open, precedented extension string — `"web"` and `"atlas"`
  today (`gateway/threads.go:94`). A pulse is `CreateThread(..., source: "pulsar")`.
- New nullable `pulsar_routine_id` column on `threads`, so a routine's pulse history is a plain
  `WHERE pulsar_routine_id = ?` query, not inference from title text.
- New `seen`/read boolean on pulsar-sourced threads, flipped when the pulse is actually opened —
  the entire mechanism behind the amber badges below.
- Firing a pulse: seed the thread's first message with the routine's saved prompt, apply its
  stored turn config, run it through the normal `agent.Run` turn pipeline — same cost tracking,
  same citations, same everything. No new rendering path, matching how Atlas's Quick Answer
  reuses the existing agent pipeline instead of a separate synthesis path.
- **Title**: not the ordinary auto-title mechanism. Every pulse from the same routine starts
  from a near-identical prompt, so the existing first-message-derived title would produce a wall
  of near-duplicate titles in a routine's pulse history. Pulsar pulses get their own title-
  generation prompt, explicitly given the routine name and the run's date so titles actually
  differentiate down a list (e.g. "Daily news — Sept 4").
- **Failure handling**: reuses the existing incomplete-turn/retry treatment already in
  `ChatTurnView.svelte` (README's "Retry & edit" — an interrupted or errored turn already gets a
  retry affordance today) rather than inventing a Pulsar-specific error state. A failed pulse is
  just a thread that stopped mid-turn; opening it shows the same retry button any other
  incomplete turn would.
- Scheduling itself: a background goroutine in the Polaris process, same shape as `backup.go`'s
  daily snapshot job — no external cron, works identically bare-metal and Docker, zero extra
  host setup. **Catch-up on restart**: on process start (and thereafter on its normal tick), the
  scheduler checks each active routine's `last_run_at` against its schedule — if a scheduled
  fire time was missed (box was mid-update, mid-reboot, whatever), it fires immediately rather
  than waiting for the next scheduled slot. A daily 7am routine that the box slept through still
  shows up the moment Polaris is back, not silently a day later.

## Routine lifecycle

- **Edit**: opening a routine gets an edit button (top right) that reopens the same
  creation-screen form, pre-filled with its current name/prompt/model/focus/deep-research/
  schedule — one form doing double duty as both create and edit, not two separate UIs.
- **Delete is always soft.** No hard-delete path in v1. Deleting a routine from its edit screen
  moves it to an archived state rather than removing the row — its `pulsar_routines` record and
  every pulse (thread) it ever produced stay intact and readable, just out of the active list.
- **Archive lives at the bottom of `/pulsar`**, below the active routines list — a routine you
  archived is still one tap away, and its pulse history opens exactly like an active routine's
  would. Whether archived routines ever get purged for real is an open question for later, not
  designed here; archive is the durable v1 state.
- **Pause** (active but not currently firing, without archiving) isn't separately specified here
  — worth deciding during implementation whether it's a real third state or whether "archive,
  then unarchive when you want it back" already covers the same need.

## Schedule model (v1)

Daily / weekly-on-a-day / monthly-on-a-date, each with a time-of-day. Server-local time only —
no timezone handling, consistent with the single-operator framing the rest of the app already
assumes (see PRODUCT.md's Users section). Finer rules ("first Monday of the month") are
explicitly deferred — add only if daily/weekly/monthly turns out to not be enough in practice.

## Cost

Not a v1 concern. Real usage of comparable rundown-style prompts runs ~10-20 tool calls (mostly
`web_search`/`web_read`) at well under a tenth of a cent on the app's default model (DeepSeek V4
Flash). No cheaper worker-tier is needed the way Tier 2 Deep Research's sub-agents needed one —
a Pulsar routine just runs on whatever model its own stored config says, defaulting to the
thread default.

## UI structure

Not a sidebar list — a flat list of pulses with no grouping would be unusable once more than one
or two routines exist (which routine did this even come from?). Instead:

- **Sidebar**: one entry point (the `Orbit` icon), not a scrolling row list — closer to how the
  Settings gear opens a dedicated panel than to Atlas's sidebar-list-plus-content pattern.
  Positioned above the favorites/recents thread list. Carries the global amber indicator (below).
- **`/pulsar`** (routines list): a "New Pulsar" button in the top corner, same primary-action
  convention as "New thread"/"New search". Below it, every active routine as a row (e.g.
  "Daily news", "Guild Wars 3 weekly", "openclaw repo daily"), each carrying its own amber
  indicator scoped to that routine's unread pulses. Archived routines (see "Routine lifecycle")
  live in their own section at the bottom of this same screen, not hidden away in Settings.
- **Routine detail** (tap a routine): its pulse history — a thread-row-style list, unread pulses
  marked with the amber dot, read ones visually dimmed.
- **Pulse detail** (tap a pulse): the existing thread-detail view, unchanged, with a back button
  top-left next to the thread's auto-generated title returning to that routine's pulse list —
  not to the sidebar.

### Amber indicator semantics

One small component, reused at two scopes rather than two separate indicator concepts:
- 1 unread → dot only.
- >1 unread → dot plus a count badge (inbox-style).
- Sidebar `Orbit` icon: count across every routine combined.
- Each routine row inside `/pulsar`: count scoped to that one routine.

## v1 scope

- Turn-config persistence infra (threads become sticky; also fixes the existing chat-thread gap,
  independent value beyond Pulsar).
- Routine create/edit (one shared form) + soft-delete-to-archive, per "Routine lifecycle".
- Scheduler goroutine with restart catch-up; pulse execution as a normal thread run tagged
  `source: "pulsar"` + `pulsar_routine_id`, with its own date-aware title-generation prompt.
- Pulse failure reuses the existing incomplete-turn/retry UI — no new error state.
- `/pulsar` route: active routines → archived routines → pulse history → pulse detail, per "UI
  structure" above.
- Amber dot/count unread indicators, global and per-routine.
- `Orbit` icon in the sidebar.
- **No CLI surface.** Unlike most new Polaris features, Pulsar is inherently background/
  scheduled rather than something to trigger by hand over SSH — a conscious "not v1" rather than
  an oversight against CLAUDE.md's usual bare-metal/Docker-parity checklist. Testing a pulse
  firing during development means inserting a due routine row directly or driving it through the
  existing WebSocket test harness, not a `polaris pulsar` command.

## Explicitly out of scope for v1

(Revisit later, not forgotten — matches this repo's existing scoping convention.)

- **v1.2 — "Help me write the prompt" wizard.** An ephemeral, non-persisted chat session, forced
  into no-research/chat mode (`ctx.NoResearch`, already exists), where Polaris uses
  `ask_user_question` (already exists, already interview-shaped — a single focused question with
  tappable options, not an interrogation) to turn something broad ("news every day on
  technology") into a tuned prompt, handed back into the routine form. Everything else in this
  plan is reuse of existing plumbing; this is the one piece that needs genuinely new
  infrastructure — a chat session that never becomes a real thread or sidebar entry, which
  doesn't exist anywhere in the app today. Worth its own slice specifically because it can slip
  without blocking core Pulsar.
- **v1.5 — the newspaper edition.** City/state/country regions as dateline'd sections, a
  masthead-style visual treatment distinct from both Polaris's chat surface and Atlas's results
  list, optionally pulling in other existing tools per region (weather, github_repo watches,
  dictionary word-of-the-day) since Pulsar itself adds no new external integrations — it's a
  scheduling and compilation layer over tools that already exist. Regions resolve through the
  same `places.Geocode` (Nominatim) → display-name → folded-into-query-text pattern
  `nearby_search`/`weather` already use, so no new geo-plumbing is needed when this is built.
  Deliberately not designed for in v1, so v1's data model doesn't get bent toward one specific
  routine's presentation.
- **v2+ (parked) — newsletter/RSS sourcing.** "Pull from Morning Brew, this RSS feed, etc." as an
  additional sourcing input alongside web search. Explicitly flagged as a real ingestion
  pipeline (parsing arbitrary feeds/emails), not a small add — correctly shelved rather than
  folded into v1's definition of done.

## Next steps

1. Design and land the turn-config persistence schema/plumbing — `threads.model` repurposed as
   the sticky field, new `focus_mode`/`deep_research` columns alongside it — independent of
   Pulsar's own tables.
2. `pulsar_routines` table (name, prompt, model, focus_mode, deep_research, schedule_type,
   schedule_params, time_of_day, created_at, last_run_at, archived_at) + `pulsar_routine_id`/
   `seen` columns on `threads`.
3. Scheduler goroutine, mirroring `backup.go`'s daily-job shape, plus the restart catch-up check
   against `last_run_at`.
4. Pulse firing: seed + run through the existing turn pipeline, tagged appropriately, with its
   own date-aware title-generation prompt; failures fall through to the existing retry UI.
5. Routine create/edit form (shared for both) with a soft-delete/archive action.
6. `/pulsar` route tree (active routines, archived routines, pulse history, pulse detail reusing
   existing thread view) + sidebar `Orbit` entry point.
7. Amber dot/count component, wired to both scopes.
