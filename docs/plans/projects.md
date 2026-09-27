# Projects — shared workspace + custom instructions across threads, v1 plan

**Added:** 2026-09-27.
**Status:** planned, not yet implemented. Tracked as issue
[#119](https://github.com/AutumnsGrove/Polaris/issues/119).

## Why this exists

Custom instructions (`tools.Context.CustomInstructions`, substituted into `{custom_instructions}` —
see `agent/driver.go`'s `applyCustomInstructionsPlaceholder`) are a single global, operator-wide
field today, and file workspaces (`<CodeExecWorkspaceDir>/<ThreadID>/`) are strictly per-thread.
There's no way to say "these several threads are all about the same body of reference
material/instructions" without repeating yourself in every new chat. Threads also have zero
grouping today beyond the `favorite` boolean (`Sidebar.svelte` splits into a flat Favorites +
Recents list) — Projects would be the first real container primitive for threads.

Inspiration is explicitly claude.ai's Projects feature (a named container with its own
instructions, a shared file/context area, and the threads started inside it), reskinned in
Polaris's own night-sky/editorial visual language rather than copied outright — see
`mockups/projects.html`, which this plan ships alongside. Where claude.ai stops at
instructions + files, this plan goes further: each project also gets its own **default focus
mode**, **default model**, **memory mode**, **Constellation visibility**, and **chat-search
exclusion** (see "Per-project settings" below) — knobs claude.ai doesn't expose at all, because
Polaris already has the underlying mechanisms (a global standing focus-mode/model default, a
global memory on/off switch, ghost threads' "no memory tool" gate) sitting one layer up from
per-thread, ready to be pulled down to per-project instead of staying purely global or purely
per-thread. A purely visual **color tag** rounds these out — no behavior, just fast scanning in
the hub grid and sidebar.

**Not part of v1:** a fully isolated per-project memory *store* (separate facts that only that
project's threads can see/write). The memory table (`store/memory.go`, table `memories` at
`store/store.go:479`) is a single flat table with no scoping column at all — `name` is the only
key, and `tools.MemoryIndexPrompt` unconditionally injects the whole index into every turn's
prompt. Building real per-project isolation means either a `memories.project_id` column or a
separate table, plus scoping `ListMemories`/`MemoryIndexPrompt` off `ctx.ProjectID` — real work,
deliberately deferred (see "Explicitly out of scope for v1"). What v1 *does* ship is a
memory-mode picker shaped so that slot can drop in later without changing the UI's shape — see
below.

Constellation/Stars (`docs/plans/constellation.md`) is a separate, already-shipped system —
Weaver extracts durable facts *about the user* from conversations into evergreen "stars," which is
adjacent to Memory but has no notion of a thread-organizing container. It should not be confused
with Projects; the only place the two systems touch is the Constellation-visibility toggle below.

## Data model

### New `projects` table

Added to `const schema` in `store/store.go`, same block style as the existing `threads`
(`store/store.go:23`) and `memories` (`store/store.go:479`) tables — heavy inline doc comments,
`id`/timestamps/soft-delete conventions matched where they apply:

```sql
CREATE TABLE IF NOT EXISTS projects (
    id                        TEXT PRIMARY KEY,
    name                      TEXT NOT NULL,
    description               TEXT NOT NULL DEFAULT '',   -- one-line summary, hub card grid
    custom_instructions       TEXT NOT NULL DEFAULT '',   -- same free-text shape as the global field,
                                                            -- capped at maxCustomInstructionsChars
                                                            -- (gateway/settings.go), just project-scoped
    favorite                  INTEGER NOT NULL DEFAULT 0, -- 1 = pinned into the sidebar's Projects
                                                            -- section — same favorite/pin mechanism
                                                            -- threads.favorite already uses; unfavorited
                                                            -- projects are reachable only via /projects
    default_focus_mode        TEXT NOT NULL DEFAULT '',   -- '' = inherit the global standing default
                                                            -- (settings.defaultFocusMode); one of
                                                            -- FOCUS_MODES' ids otherwise
    default_model             TEXT NOT NULL DEFAULT '',   -- '' = inherit settings.defaultModel; a
                                                            -- model id otherwise
    memory_mode               TEXT NOT NULL DEFAULT 'default', -- 'default' | 'none' (v1); 'project_scoped'
                                                            -- is a reserved, not-yet-functional value —
                                                            -- see "Memory mode" below
    constellation_visible     INTEGER NOT NULL DEFAULT 1,  -- 0 = Weaver skips this project's threads
    exclude_from_chat_search  INTEGER NOT NULL DEFAULT 0,  -- 1 = SearchMessages skips this project's
                                                            -- threads entirely
    color                     TEXT NOT NULL DEFAULT '',   -- '' = no tag; otherwise one of the
                                                            -- --color-cat-* suffixes already defined
                                                            -- in app.css (no new tokens needed)
    created_at                TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                TEXT NOT NULL DEFAULT (datetime('now'))
);
```

No `disabled` soft-delete column — see "Deletion behavior" below; a deleted project is a real
`DELETE`, not a hide. `favorite` is not a soft-delete flag either — see "Pinning to the sidebar"
below.

### `threads.project_id`

One `ALTER TABLE threads ADD COLUMN project_id TEXT REFERENCES projects(id)` appended at the very
end of the `migrations` slice (`store/store.go:927`) — nullable, default `NULL`, same "add a
column, only matters going forward" shape `pulsar_routine_id` already uses, per issue #119's own
framing. `ListThreads`/`ListThreadsPage` (`store/store.go:1725`, `:2035`) need no change — a
project-linked thread is still an ordinary thread in every other respect (favoritable, forkable,
searchable); `project_id` is purely an additional attribute, the same way `pulsar_routine_id` sits
alongside without changing what "a thread" means.

## Workspace: read-only shared originals, private working copies

**Superseded design note:** an earlier draft of this plan had every thread in a project share one
mutable directory outright (a single `ctx.WorkspaceKey` swapped in for `ctx.ThreadID`). That
allows exactly the collision this codebase has already been burned by once — two independent
actors (here, two threads' turns instead of two `docker compose up -d` runs) mutating the same
file with no isolation. The design below replaces it entirely: **originals stay read-only, a
thread only ever writes to its own copy, and joining the shared pool is a deliberate action, never
an automatic one.**

Every thread keeps its own private, read-write workspace exactly as it does today —
`<CodeExecWorkspaceDir>/<ThreadID>/`, untouched by any of this. A project gets a second directory,
`<CodeExecWorkspaceDir>/<projectID>/`, that is *never* the target of an ordinary write:

- **`code_exec`'s sandbox mounts both directories** when the thread has a `project_id`: the
  thread's own directory read-write (as always), and the project's directory **read-only**
  alongside it. `codeExecRequest` (`tools/code_exec.go:155`) gains a second field —
  `ProjectHostWorkspaceDir string`, set only when the thread has a project — and the host-side
  watcher (`compose/watcher/codeexec.sh`) adds a second bind mount for it with `:ro`. This is what
  actually enforces "editing gets a copy, not the original": the mount itself refuses the write: a
  script that reads `project/router-configs.txt`, changes it, and tries to save back to that same
  path fails outright, so the model's only path to persisting a change is writing somewhere inside
  its own writable directory. No copy-tracking logic needed — the filesystem does it for free.
- **Reads fall through two tiers.** `resolveWorkspaceFilePath` (`tools/view_image.go:270`) and
  `gateway/workspace.go`'s `handleGetWorkspaceFile` both currently resolve a single
  `<CodeExecWorkspaceDir>/<ThreadID>/<relPath>`. Both gain a fallback: if the path doesn't exist
  under the thread's own directory and the thread has a `project_id`, retry under
  `<CodeExecWorkspaceDir>/<projectID>/<relPath>` instead, with the same `filepath.Rel`
  path-traversal check against *that* root. `handleGetWorkspaceFile` is a `*Server` method, so the
  one new piece of plumbing it needs — looking up the URL's `thread_id`'s `project_id` — is a
  single `s.db` query, not new wiring.
- **New files never land in the shared directory by default.** `fetch_url` writes a download into
  the thread's own directory, same as any non-project thread today; a `code_exec` chart/report
  goes to the thread's own directory too. A file attached through a thread's composer mid-chat
  behaves the same way — it's the thread's own file, not automatically shared.
- **Promoting a file into the shared pool is one explicit tool: `save_to_project(filename)`.**
  Takes a file already present in the calling thread's own workspace and copies it into the
  project's shared directory — works regardless of how that file got there (an upload, a
  `fetch_url` download, a `code_exec` output), so it's one primitive instead of a fetch_url-only
  flag that would leave code_exec-generated files with no equivalent path. If a file of that name
  already exists in the project directory, it's saved under an auto-incremented name
  (`router-configs-2.txt`) rather than silently overwritten — a later thread's promote can never
  quietly destroy an earlier thread's contribution to the shared pool — and the tool's result names
  whatever it actually got saved as, so the model can mention it if relevant.

  **Structurally absent from ordinary threads, not just refused at call time.** `tools.Context`
  gains a `ProjectID string` field (empty when the thread has none), set alongside `ThreadID` in
  `gateway/turn.go`'s per-turn context build — the same field the memory-mode and
  Constellation/chat-search gates above already read. `save_to_project`'s catalog entry
  (`tools/catalog.go`) gets a new `Requires: "project_workspace"` case in `offered()`:
  `return ctx.ProjectID != ""`, the exact same shape as the existing `docker_only` →
  `ctx.CodeExecEnabled` and `memory_store` → `ctx.WriteMemory != nil` cases just above it in that
  switch. A non-project thread never sees `save_to_project` in its offered tool list at all — the
  model can't attempt it, get a rejection, and retry; it simply isn't a tool that thread has, the
  same way `code_exec` isn't a tool a bare-metal install's threads have. `catalog_test.go`'s
  existing table-driven `offered()` tests (`"docker_only" cases`, `"chat_search" cases`) are the
  precedent to extend with `save_to_project`'s own "excluded without a project" / "offered with
  ctx.ProjectID set" pair.
- **Uploading directly through the project detail view's own "Shared workspace" card stays a
  separate, always-explicit path** straight into the project directory — that one was already a
  deliberate "add this to the project" action, not a mid-conversation default, so it's unaffected
  by any of the above.

## Prompt assembly: project instructions + opener

No new `prompt.md` placeholder needed. The existing `{custom_instructions}` placeholder
(`agent/driver.go:370`, `applyCustomInstructionsPlaceholder`) already gets its value from
`agentCtx.CustomInstructions`, set at `gateway/turn.go:628` via `CustomInstructionsFromStore(s.db)`.
When the thread has a `project_id`, that same line concatenates a short project block *after* the
global field (additive, never a replacement — a project adds context, it doesn't hide the
operator's standing instructions):

```
{global custom instructions, unchanged}

## Project: <project name>
<project's custom_instructions>

Shared workspace (read-only originals — save_to_project to add to them): file1.csv, chart_q3.png, notes.md
```

The file list is names only (the same "let the model `ls`/`code_exec` its way in rather than
eagerly describing every file" call already made for single-file attachments in
`docs/plans/workspace-store-unification.md` — more apt here, not less, since a shared project
workspace accumulates files across many threads over time). Calling the shared files out as
read-only originals in the opener itself matters more here than it did in the old single-directory
design: the model needs to know upfront that editing one of these means saving a copy into its own
workspace, not that the edit silently vanishes. This is a small, contained change at one call site
— no edits to `agent/driver.go`'s placeholder machinery or `prompts.yaml`.

## Pinning to the sidebar

A project does **not** appear in the sidebar just by existing. `projects.favorite` is the exact
same mechanism `threads.favorite` already is — a plain boolean toggled from the hub or detail view
— and the sidebar's new "Projects" section (see "UI structure" below) only ever lists favorited
projects, the same way its existing Favorites section only ever lists favorited threads. An
unfavorited project is reachable solely through `/projects`. This is deliberate, not an oversight:
a single-operator install can accumulate many projects over time (the claude.ai screenshots this
plan drew from show exactly that pattern — a handful of pinned projects in the left nav, a much
longer full list on the hub page), and pinning is what keeps the sidebar itself uncluttered without
requiring an archive/delete step just to stop seeing something in daily nav.

## Per-project settings

Five knobs beyond claude.ai's instructions+files pair, plus one purely visual tag, all surfaced in
the project detail view's settings section using one shared icon language (see "Icon language"
below) rather than plain-text labels:

### Default focus mode

Direct precedent already exists: `settings.defaultFocusMode` (`web/src/lib/settings.svelte.ts:106`)
is a global "standing default" applied once per new thread (`ChatView.svelte:101-105`, gated on
`settings.loaded` so `'off'` — itself a valid value — isn't mistaken for "not loaded yet"). A
project's `default_focus_mode` is the same mechanism, scoped: when starting a new thread inside a
project, the project's value takes precedence over the global one if set (`''` continues to mean
"inherit the global default," not "force off"). Reuses the exact `FOCUS_MODES` list from
`web/src/lib/focusModes.ts` — no new vocabulary.

### Default model

Identical shape to default focus mode, one level up: `settings.defaultModel`
(`web/src/lib/settings.svelte.ts:100`) is already a global standing default applied to new threads.
`projects.default_model` overrides it the same way — `''` inherits the global default, a model id
otherwise. Useful in both directions: pin a cheaper model for a low-stakes project (the budget
project in the mockup), or a stronger one for a research-heavy project, without having to
remember to switch models by hand every time a new thread starts there.

### Memory mode

Global memory wiring is gated once, at `gateway/turn.go:691`:

```go
if !ghost && MemoryEnabledFromStore(s.db) {
    agentCtx.ListMemories = s.db.ListMemories
    agentCtx.GetMemory = s.db.GetMemory
    agentCtx.WriteMemory = s.db.CreateMemory
    agentCtx.EditMemory = s.db.UpdateMemory
    agentCtx.ForgetMemory = s.db.DeleteMemory
}
```

Leaving these nil is what makes both the `memory` tool *and* the `{memories}` prompt section
disappear — `tools.MemoryIndexPrompt` (`tools/memory.go:275`) returns `""` outright when
`ctx.ListMemories == nil`. A project's `memory_mode = 'none'` extends the same condition
(`&& projectMemoryMode != "none"`) — zero new plumbing in `agent/driver.go` or `tools/memory.go`,
reusing exactly the mechanism ghost threads already rely on for "no memory tool."

Three-way picker, only two modes functional in v1:
- **Default** — behaves exactly as every non-project thread does today (reads/writes the one
  global table). No code change beyond the gate above.
- **None** — memory tool and index fully absent from the turn, as described.
- **Project-scoped** *(reserved, shown disabled/greyed in v1's UI, "coming later")* — the real v2
  work: an isolated per-project memory store. Building the picker's shape now means v2 only has to
  make the third option functional, not redesign the control.

### Constellation / Weaver visibility

Weaver's shooting-star candidate pool (`docs/plans/constellation.md`, "Thread eligibility") already
excludes `threads.disabled = 1` outright, alongside its idle-timing/delta/retry gates. A project's
`constellation_visible = 0` becomes a fifth, identical exclusion — joined through `project_id` at
query time (no new column mirrored onto every thread row): a thread whose project has visibility
turned off is skipped the same unconditional way a disabled thread is, full stop. Default is `1`
(included) — matches how every thread behaves today; a project can opt out for content that isn't
personal-fact material (e.g., a coding-reference project). The toggle itself is greyed out
entirely in the UI when Constellation is disabled globally (`constellation_config.enabled` —
`store/constellation.go:25`), since there'd be nothing to opt out of.

### Exclude from chat search

A privacy-shaped parallel to the Constellation toggle: `SearchMessages`
(`store/store.go:2150`, backing the `search_chats` tool via `agentCtx.SearchThreads` —
`gateway/turn.go:631`) needs one additional join/condition excluding rows whose thread's project
has `exclude_from_chat_search = 1`, the same unconditional-skip shape as the Constellation-
visibility join above. Default is `0` (included) — matches how every thread's content is
searchable today. The use case is the mirror image of Weaver visibility: a project like the
budget-rebuild one in the mockup is exactly the kind of content that shouldn't surface when the
assistant searches past chats from an unrelated thread, even though nothing about *this* project's
own threads changes when you're inside them.

### Color tag

Purely cosmetic, no turn-time behavior at all. `projects.color` holds one of the `--color-cat-*`
token suffixes already defined in `app.css` for Constellation's categories (`technology`,
`nature-environment`, `finance-shopping`, etc. — twenty-some hues already tuned for both themes) —
picking one just tints the project's hub card edge and its sidebar row dot. No new tokens, no new
palette to design; reusing an existing, already-accessible set is the entire feature.

## Deletion behavior

Deleting a project is a real `DELETE FROM projects WHERE id = ?`, not a soft-disable — there's no
`disabled` column on the table at all (see schema above). Alongside it:
- `UPDATE threads SET project_id = NULL WHERE project_id = ?` — every thread that belonged to the
  project falls back to being an ordinary, ungrouped thread, fully intact (messages, cost history,
  favorites status, and — since a thread's own workspace was never the project's directory to
  begin with — every file it ever created or had promoted a copy of stays exactly where it was).
- The project's shared directory (`<CodeExecWorkspaceDir>/<projectID>/`) is removed from disk —
  those were the shared, read-only originals for a project that no longer exists. Nothing else on
  disk is affected: no thread's own workspace lived inside that directory.

## Thread ↔ project mutability

A thread can move between projects (or in/out of having one at all) at any point after creation —
not fixed at creation time. Surfaced as a "Move to project" action in `ThreadMenu.svelte`, the same
place Favorite/Promote/Delete already live. Moving a thread changes nothing about its own files at
all — its own directory was never the project's directory. The only effect is on its *next* turn's
`code_exec` call: the read-only project mount attached is whichever project (if any) the thread
currently belongs to. A thread that already promoted a file into its old project's shared
directory via `save_to_project` doesn't take that file with it when moved — the file stays with
the project it was promoted into, same as leaving a shared drive behind when you change teams.

## UI structure

### Icon language

Hub cards and the settings panel share one icon vocabulary instead of plain-text chips/labels, so
recognizing a project's non-default settings on the hub grid trains the same visual read used
inside its detail view. Reuses icons already established elsewhere in the app rather than
inventing new ones: `Brain` (memory mode — already `MemorySettings.svelte`'s icon), `Cpu` (default
model — already imported in `ComposerMenu.svelte`), `SlidersHorizontal` (the *default focus mode*
row's own label, i.e. "this row configures a mode" — same icon `ComposerMenu.svelte` already uses
for its own mode-config affordance), `Orbit` (Constellation visibility — already `Sidebar.svelte`'s
nav icon for `/constellation`), a search-with-slash mark (chat-search exclusion), and a filled
`Star` for the favorite/pin toggle (already the exact icon Favorites uses). A hub card shows a
small icon-led chip only for settings that differ from their default — a project left entirely at
defaults shows no chips at all, keeping the common case quiet.

Once a specific focus mode is actually selected (not just "this row exists"), its chip drops
`SlidersHorizontal` for that mode's own icon from `FOCUS_MODES` (`web/src/lib/focusModes.ts` —
`Microscope` for Researcher, `Zap` for Brief, and so on) and shows no text label at all — each mode
already has a distinct icon in that list, so naming it again in words is redundant once you
recognize the glyph. Same treatment on the omnibox's mode chip in the project detail view. A
tooltip (`title="Researcher focus"`) covers the one moment that glyph isn't yet memorized.

### `/projects` hub

A dedicated route, same tier as `/pulsar`/`/daily`/`/constellation`. Card grid (name, one-line
description, updated-at, icon-led chips for any non-default settings, a color-tag edge if set, and
a tap-to-pin star) — structurally close to claude.ai's Projects hub, reskinned entirely in
Polaris's palette (`--color-surface`/`--color-surface-2` cards, `--color-accent` for the create
action, Lexend body text, no purple/blue gradient, no rounded chat-bubble chrome). Create/rename/
delete from here; tapping a card (not its star) opens the detail view.

### Project detail view

Leads with an **omnibox** — a composer sitting right at the top of the view, same as claude.ai's
own project page — where typing a prompt and sending it creates a new thread in this project
*and* opens it immediately, already mid-turn, rather than requiring a separate "create, then type"
step. A plain **"New thread"** button sits alongside it for the other case: opening a blank,
unsent composer scoped to the project, same as starting any ordinary chat. Below that: the
instructions editor (same textarea-with-char-count pattern `MemorySettings.svelte` already uses
for a named-entry-with-description field), a shared-workspace file list (upload straight in, same
attachment affordance the composer already has), the settings panel described above, and a list of
the project's own threads underneath (reusing the existing thread-row component from
`Sidebar.svelte` rather than inventing a second one).

### Sidebar

A new "Projects" section, same `.section-label` small-caps convention as Favorites/Recents
(`Sidebar.svelte:306`, `:315`) — sits above them, listing only *favorited* projects (see "Pinning
to the sidebar" above) as a shortcut into `/projects/<id>`, not a full nested thread tree in the
sidebar itself (that's what the detail view is for). Matches the existing "sidebar is a fast-nav
surface, detail lives in its own view" split already established by Pulsar/Daily/Constellation.

### Thread header: project indicator

A thread that belongs to a project shows a small pill in its chat header (next to the title, ahead
of `ThreadMenu`'s "…" button) — the same folder icon used in the sidebar's Projects section label,
plus the project's name and color tag if set. Tapping it navigates to the project's detail view.
Without this, a thread opened straight from search or a shared link would give no indication it's
part of a shared-context project at all — the instructions and workspace files silently in play
for that turn would be invisible.

### ThreadMenu: "Move to project"

New entry in `ThreadMenu.svelte` alongside Favorite/Promote/Delete — opens a small picker (project
list, plus "Remove from project" when the thread already has one).

## v1 scope

- `projects` table + `threads.project_id` migration.
- The read-only shared-workspace mount: `codeExecRequest`'s second `ProjectHostWorkspaceDir`
  field + `codeexec.sh`'s `:ro` mount, the two-tier read fallback in
  `resolveWorkspaceFilePath`/`handleGetWorkspaceFile`, and the new `save_to_project` tool.
- Project custom instructions concatenated into `{custom_instructions}` at turn-context build,
  plus the shared-workspace file-name opener (calling out that the listed files are read-only
  originals).
- Five per-project settings — default focus mode, default model, memory mode (Default/None only),
  Constellation visibility, chat-search exclusion — plus the purely cosmetic color tag.
- Favorite/pin toggle gating sidebar visibility, same mechanism as `threads.favorite`.
- `/projects` hub, project detail view (with the top-of-view omnibox + separate "New thread"
  button), sidebar section, thread-header project indicator, ThreadMenu "Move to project."
- Delete: orphan threads, remove workspace files, real `DELETE` (no soft-disable).
- Threads freely movable between projects after creation.

## Explicitly out of scope for v1

- **Project-scoped memory store (v1.5/v2)** — the real isolated-memory work described above. The
  picker's third slot is reserved and visibly disabled in v1 specifically so this can land later
  without a UI redesign.
- **Public/shared project pages** — unrelated to issue #120 (public thread sharing); if that ships
  first, a project-level equivalent is a separate future decision, not bundled here.
- **Nested projects / projects-within-projects** — one flat namespace only.
- **Hard per-project cost limits/budget enforcement** — `default_model` only seeds a new thread's
  starting model choice, same as the global default already does; it's a convenience default, not
  a cap, and a thread can still switch models freely afterward exactly as it can today.
- **Moving/copying workspace files between projects** — a thread that changes projects leaves
  anything it promoted into its old project's shared directory behind entirely (see "Thread ↔
  project mutability"); there's no "move these shared files to the new project too" step.
- **Any UI surfacing of `save_to_project`** beyond the tool call itself — v1 doesn't add a citation
  badge or explicit "promoted" indicator anywhere in the thread view; the tool's own result text
  (naming what got saved and under what name) is the only confirmation.

## Next steps

1. Land the `projects` schema + `threads.project_id` migration in `store/store.go`, with
   `store/projects.go` for CRUD (`CreateProject`, `GetProject`, `ListProjects`, `UpdateProject`,
   `DeleteProject` — the last one performing the orphan-and-cleanup sequence above in one
   transaction).
2. Shared-workspace plumbing: add `ctx.ProjectID` to `tools.Context`, set it in
   `gateway/turn.go` alongside `ThreadID`; `ProjectHostWorkspaceDir` on `codeExecRequest` +
   `codeexec.sh`'s second `:ro` mount; the two-tier fallback in `resolveWorkspaceFilePath` and
   `handleGetWorkspaceFile`; the new `save_to_project` tool with its own
   `tools/descriptions/*.yaml` entry and a new `Requires: "project_workspace"` case in
   `catalog.go`'s `offered()` (`return ctx.ProjectID != ""`) so it's absent from the tool list
   entirely on a non-project thread, not just refused when called.
3. Turn-context wiring: concatenate project instructions + opener into
   `agentCtx.CustomInstructions`; seed a new thread's `Model`/`FocusMode` from the project's
   defaults when set; extend the memory-wiring gate with `memory_mode != "none"`; add the
   Constellation-visibility join to Weaver's candidate-pool query and the chat-search-exclusion
   join to `SearchMessages`.
4. Frontend: `/projects` route + hub view (cards with icon-led chips, color tag, pin star),
   project detail view (omnibox, "New thread" button, settings panel), sidebar section (favorited
   projects only), thread-header project indicator, `ThreadMenu` "Move to project."
5. Live-verify per this repo's own standing practice (`CLAUDE.md`'s "Verify on real hardware, not
   just review or mocked tests") — specifically: a `code_exec` write attempt against a project's
   read-only mount actually fails instead of silently succeeding, `save_to_project` actually makes
   a promoted file visible to a sibling thread's next turn, a moved thread's next `code_exec` call
   actually mounts its new project instead of its old one, an ordinary non-project thread's
   offered-tools list genuinely has no `save_to_project` entry in it at all (not just a call that
   errors), and the Constellation opt-out actually excludes a project's threads from a live Weaver
   poll.
