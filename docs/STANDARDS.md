# Coding standards

This is Polaris's own coding standards doc — not a generic Go/Svelte style guide copy-pasted in,
but the conventions this codebase already follows in its best code, written down so they survive
a single-operator project's biggest risk: nothing enforcing consistency except one person's memory
across sessions. Where a rule below cites a file, that file is the reference example — when in
doubt, go read it rather than trusting this doc's paraphrase.

This exists because of a live audit (2026-09) that went looking for "sloppy code" in the
Constellation subsystem and mostly didn't find any — the real gap was that the standards it was
quietly already meeting were never written down anywhere a new session (or a new contributor)
could check against. See `docs/plans/constellation.md` for the subsystem that prompted this and
this doc's own git history for what the audit actually found and fixed.

CLAUDE.md still wins on anything specific to this repo's deployment model (Docker-only
install/production, bare-metal dev, the dual-support checklist). This doc is about code shape —
how a function, a query, or a component should look — not about where things run.

## Go

### Error handling

- Every returned error gets wrapped with `fmt.Errorf("<short context>: %w", err)`, where the
  context is the function's own job in a few lowercase words — not the package name, not the
  parameter values. See `store/constellation.go`'s `GetStar`/`ListStars`/etc.: every wrap reads
  `"get star: %w"`, `"list stars: %w"`, consistently lowercase, consistently terse. A caller that
  wraps again gets a readable chain (`"shooting star: get messages: sql: no rows"`), not a wall of
  repeated context.
- Sentinel errors (`var ErrStarNotFound = errors.New(...)`) for a condition callers need to branch
  on, checked with `errors.Is`. Never compare error strings, never return a bare
  `errors.New("not found")` from more than one place when a caller needs to tell them apart.
- Never silently discard an error with `_ = someCall()`. If a failure genuinely isn't worth
  aborting the caller over (a best-effort side write, an observability log), still log it — see
  `gateway/constellation_weaver.go`'s `warnOnErr` helper, added specifically to replace a prior
  `_ = db.RecordX(...)` shape that made a dropped trace row invisible. A silent drop and a logged,
  intentionally-ignored failure look identical in a diff; only one of them is debuggable later.
- `defer rows.Close()` immediately after a successful `Query`, and always check `rows.Err()` after
  the `for rows.Next()` loop, not just each `Scan` call's own error.

### Comments explain why, not what

- Comment density should track how non-obvious the *reason* is, not how much a block of code does.
  A one-line SQL update gets no comment; a compare-and-set UPDATE with a `WHERE ... OR <stale
  timeout>` clause gets a paragraph, because the reader can't derive "why is there a staleness
  fallback here" from the SQL itself. See `store/constellation.go`'s
  `SetConstellationBackfillStarted` for the shape to match.
- Prefer citing the concrete failure a piece of code prevents over describing the code in the
  abstract — "live-observed: ~74 of 163 threads double-processed" (`constellation_backfill.go`)
  tells a future reader why the guard has to stay; "prevents double processing" doesn't.
- Doc comments on exported functions start with the function's own name (`// GetStar returns...`),
  per Go convention — `go vet`/`golint` will flag a miss, but more importantly it's what makes
  `go doc` output and hover-tooltips readable.

### Reuse over repetition — the three-strikes rule

- Don't abstract on the first or second occurrence of similar code — a bit of duplication reads
  better than a premature helper (see CLAUDE.md's own "don't design for hypothetical future
  requirements"). Once the *same* shape shows up a third time, extract it.
- This repo already has the precedent: `store/events.go`'s `scanEvents(rows *sql.Rows)` exists
  because `ListEvents`/`ListRecentEvents` both needed the same scan-into-slice loop. Any new
  store method returning a list of some type should look for whether that type already has (or
  now needs) the same kind of shared scan helper before writing a fourth copy of a `for rows.Next()
  { ... }` block. (This is exactly what the Constellation audit found and fixed: `GetStar`,
  `ListStars`, `SearchLibraryStars`, and `StarsByThread` had each hand-rolled the same
  scan-then-decode-tags block — see `scanStar`/`scanStars` in `store/constellation.go`.)
- Prefer a small unexported helper (`placeholders`, `toAnySlice` in `store/constellation.go`) over
  reaching for a generics-heavy abstraction — this codebase's Go is deliberately plain.

### Naming and structure

- One tool = one file under `tools/` (`create_star.go`, `read_star.go`, ...), one subsystem's
  routes = one file under `gateway/` (`constellation_routes.go`). Don't split a single tool's
  definition/handler across files, and don't merge two unrelated tools into one file for brevity.
- A package-level doc comment (the comment directly above `package X`) states what the *file*
  is responsible for and points at the design doc, if one exists — every `constellation_*.go` file
  does this; match it in new files.
- Constants that exist to make a magic number legible get a name and a doc comment explaining the
  number's provenance if it's not self-evident (`weaverMaxTurns = 25`, `shootingStarStaleAfter =
  30 * time.Minute`) — a bare `25` or `1800` three call sites away from its own reasoning is a
  future bug waiting for someone to "simplify" it back out.
- `context.Context` is always the first parameter, named `ctx` or a more specific `reqCtx` when a
  function also takes an internal loop/goroutine context that needs disambiguating (see
  `RunShootingStar`'s `reqCtx` vs. `agent.Run`'s own internal context handling).

### Concurrency and background work

- Any code that can panic outside an HTTP handler's own recover (a scheduler tick, a background
  goroutine) needs its own recovery wrapper — see `RunShootingStarRecovered`'s doc comment for why
  this specifically matters for anything not reached through `net/http`.
- A background loop that runs on a timer must make zero externally-visible calls (AI completions,
  paid API calls) when there's nothing to do — never "check anyway, just in case," even on a cheap
  schedule. This is a hard rule in this codebase, not a preference — see
  `docs/plans/constellation.md`'s design principles for the specific past incident (`her-go`'s
  unconditional nightly job) this guards against.
- Prefer a DB-column flag with compare-and-set semantics (`WHERE x IS NULL OR x <= <stale
  cutoff>`) over an in-process mutex for any lock that needs to survive across process boundaries
  (bare-metal CLI vs. the long-running server) — see `SetConstellationBackfillStarted`'s doc
  comment for the exact bug (two processes racing an in-memory flag neither could see) this
  pattern fixes. Always pair it with a staleness timeout; an un-expiring lock wedges forever the
  first time the process holding it crashes before its own cleanup runs.

## Svelte / SvelteKit / TypeScript

### State management

- A feature with its own multi-screen surface (Constellation, Pulsar Daily) gets its own
  `$state`-based class in `lib/`, instantiated once as a module-level singleton
  (`export const constellationState = new ConstellationState()`), not folded into the global
  `AppState`. Keep a feature's loading/error flags per-section (`libraryLoaded`/`libraryError`,
  `inboxLoaded`/`inboxError`, ...) rather than one shared flag, so one section's slow fetch doesn't
  show every other section spinning too.
- Distinguish three states, not two, for any fetched section: not-yet-loaded, loaded-but-empty,
  and failed-to-load. A `*Loaded` flag alone conflates "still fetching" with "fetched, genuinely
  empty"; add a paired `*Error` flag so a network failure renders a distinct, actionable message
  instead of silently looking like an empty state (see `ConstellationState`'s doc comment on this
  exact bug: the Map tab and Usage modal used to hang on "Loading…" forever on a failed fetch).
- Guard any route/component that can be re-triggered by a param change while a previous fetch is
  still in flight with a sequence-number check (`loadSeq`), not a boolean `loading` flag alone — a
  boolean can't tell an old, slow response apart from the current one. See
  `star/[id]/+page.svelte`'s `load()` for the pattern; reuse it rather than re-deriving it per
  component.
- Use `$derived`/`$derived.by` for anything computed from state, never a manually-managed
  duplicate `$state` kept in sync by hand in multiple places.

### Styling

- Use the shared CSS custom properties in `app.css`'s `:root` (`--z-*`, `--radius-*`, `--space-*`,
  color tokens) instead of a new raw literal — already stated in CLAUDE.md, repeated here because
  it's the single most-violated rule in a rushed pass.
- The same three-strikes rule from Go applies to component CSS: a style block repeated
  near-verbatim across three or more `.svelte` files belongs in `app.css` as a shared class
  instead. This repo already does this for shared modal/usage-stats treatments (`.usage-big-cost`,
  `.usage-stat-group`, `.usage-stat-row` — see `ConstellationUsageModal.svelte`'s own comment:
  "shared with SettingsPanel.svelte's own Usage section, one visual treatment ... instead of two
  copies to keep in sync by hand"). Apply the same test to plain layout/empty-state boilerplate,
  not just modal-specific styling — a `.empty { ... }` block copy-pasted across five route files
  is exactly this rule being skipped.
- Don't extract a shared *component* (as opposed to a shared CSS class) until three real call
  sites exist, matching the Go/CSS rule above — two similar-looking screens (e.g. the Star detail
  header and the Review screen's header) sharing some markup shape isn't automatically premature
  to leave alone; a forced shared component for two call sites often costs more in prop-plumbing
  than it saves.

### Data loading and API calls

- Every `fetch` call against the backend goes through a typed wrapper function/method (see
  `constellation.svelte.ts`'s `fetchSection`/`fetchStats`/etc.), never an inline `fetch` scattered
  through a `.svelte` file's script block, so the response shape is asserted once, in one place.
  Response types live in `lib/types.ts`, matching the Go struct they deserialize.
- On a fetch failure, prefer leaving previously-loaded data in place over clearing it — a
  transient network blip shouldn't visibly empty out a screen the person was just looking at. Set
  the paired `*Error` flag (above) instead, and let the UI decide how to surface it.

### Accessibility

- Every non-text-labeled icon button gets `aria-label`. A click handler on a non-interactive
  element (a backdrop dismiss surface, not a real control) gets an explicit
  `<!-- svelte-ignore a11y_click_events_have_key_events -->` / `a11y_no_static_element_interactions`
  pair with a comment explaining why it's a dismiss surface and not a control that needs keyboard
  support — never a silent ignore with no reasoning, and never suppressing the warning on an
  element that's actually a real interactive control in disguise.

## Testing

- Test function names state the behavior under test, not the function name plus a number:
  `TestRunShootingStar_HitsTurnCap_SetsNeedsRetryAndError`, not `TestRunShootingStar2`. A failing
  test's name should tell you what broke before you open the file.
- Cover the failure/edge path with its own named test, not just the happy path folded into one big
  test function — `TestHandleRestoreConstellationStar_NotFound` and
  `_RejectsNonRejectedStar` sit next to the happy-path `TestHandleRestoreConstellationStar` as
  siblings, not asserted inline in the same function.
- A concurrency/race fix (a compare-and-set lock, a stale-timeout sweep) needs a test that actually
  exercises the race condition it closes, not just the happy path — see
  `TestConstellationConfig_BackfillStartedRoundTrips`'s explicit "a second concurrent call must not
  silently win" case.
- When reverting a fix to confirm its regression test actually fails without the fix (CLAUDE.md's
  own instruction) isn't practical to do for real inside a session, at minimum reason through
  what the test would have caught before trusting it as coverage.

## When this doc and the code disagree

Treat the code as authoritative for anything this doc doesn't cover, and raise the gap rather than
silently picking one — this doc gets updated when a real, deliberate pattern change happens, not
kept "accurate" by quietly ignoring it.
