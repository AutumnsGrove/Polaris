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
Polaris's own night-sky/editorial visual language rather than copied outright — see the mockups
this plan ships alongside. Where claude.ai stops at instructions + files, this plan goes further:
each project also gets its own **default focus mode**, **memory mode**, and **Constellation
visibility** toggle (see "Per-project settings" below) — knobs claude.ai doesn't expose at all,
because Polaris already has the underlying mechanisms (a global standing focus-mode default, a
global memory on/off switch, ghost threads' "no memory tool" gate) sitting one layer up from
per-thread, ready to be pulled down to per-project instead of staying purely global or purely
per-thread.

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
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',   -- one-line summary, shown on the hub card grid
    custom_instructions    TEXT NOT NULL DEFAULT '',    -- same free-text shape as the global field,
                                                          -- capped at maxCustomInstructionsChars
                                                          -- (gateway/settings.go), just project-scoped
    default_focus_mode     TEXT NOT NULL DEFAULT '',    -- '' = inherit the global standing default
                                                          -- (settings.defaultFocusMode); one of
                                                          -- FOCUS_MODES' ids otherwise
    memory_mode            TEXT NOT NULL DEFAULT 'default', -- 'default' | 'none' (v1); 'project_scoped'
                                                          -- is a reserved, not-yet-functional value —
                                                          -- see "Memory mode" below
    constellation_visible  INTEGER NOT NULL DEFAULT 1,  -- 0 = Weaver skips this project's threads
    created_at             TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at             TEXT NOT NULL DEFAULT (datetime('now'))
);
```

No `disabled` soft-delete column — see "Deletion behavior" below; a deleted project is a real
`DELETE`, not a hide.

### `threads.project_id`

One `ALTER TABLE threads ADD COLUMN project_id TEXT REFERENCES projects(id)` appended at the very
end of the `migrations` slice (`store/store.go:927`) — nullable, default `NULL`, same "add a
column, only matters going forward" shape `pulsar_routine_id` already uses, per issue #119's own
framing. `ListThreads`/`ListThreadsPage` (`store/store.go:1725`, `:2035`) need no change — a
project-linked thread is still an ordinary thread in every other respect (favoritable, forkable,
searchable); `project_id` is purely an additional attribute, the same way `pulsar_routine_id` sits
alongside without changing what "a thread" means.

## Workspace: `ctx.WorkspaceKey`

Four call sites currently key a thread's workspace directory directly off `ctx.ThreadID`:
`tools/code_exec.go:143` (`filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID)`) and `:158` (the
host-side mirror), `tools/view_image.go`'s `resolveWorkspaceFilePath`, and `tools/fetch_url.go`'s
equivalent write path. `gateway/workspace.go`'s `handleGetWorkspaceFile` serves whatever directory
name it's given in the URL and doesn't need to know about projects at all — its path-traversal
defense (validated against `root`, not the `root/threadID` intermediate — see the doc comment
above `handleGetWorkspaceFile`) is agnostic to what the segment means.

Cleanest fix, per issue #119: add one field, `ctx.WorkspaceKey`, computed once per turn in
`gateway/turn.go` alongside `agentCtx.ThreadID` — the thread's `project_id` when set, else
`ctx.ThreadID` itself. All four tool call sites read `ctx.WorkspaceKey` instead of `ctx.ThreadID`
directly; nothing branches on "is this a project thread" in more than one place. Any code that
mints a citation/workspace URL (`tools/show.go`, code_exec's chart-output path) uses the same key,
so a citation link naturally points at the shared project directory rather than a directory
scoped to whichever thread happened to generate the file.

Practical effect: every thread in a project shares one directory on disk
(`<CodeExecWorkspaceDir>/<projectID>/`), so a file uploaded or generated in thread A is visible to
`code_exec`/`view_image`/`fetch_url` from thread B in the same project without re-uploading.

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

Shared workspace: file1.csv, chart_q3.png, notes.md
```

The file list is names only (the same "let the model `ls`/`code_exec` its way in rather than
eagerly describing every file" call already made for single-file attachments in
`docs/plans/workspace-store-unification.md` — more apt here, not less, since a shared project
workspace accumulates files across many threads over time). This is a small, contained change at
one call site — no edits to `agent/driver.go`'s placeholder machinery or `prompts.yaml`.

## Per-project settings

The three knobs beyond claude.ai's instructions+files pair, all surfaced in the project detail
view's settings section:

### Default focus mode

Direct precedent already exists: `settings.defaultFocusMode` (`web/src/lib/settings.svelte.ts:106`)
is a global "standing default" applied once per new thread (`ChatView.svelte:101-105`, gated on
`settings.loaded` so `'off'` — itself a valid value — isn't mistaken for "not loaded yet"). A
project's `default_focus_mode` is the same mechanism, scoped: when starting a new thread inside a
project, the project's value takes precedence over the global one if set (`''` continues to mean
"inherit the global default," not "force off"). Reuses the exact `FOCUS_MODES` list from
`web/src/lib/focusModes.ts` — no new vocabulary.

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

## Deletion behavior

Deleting a project is a real `DELETE FROM projects WHERE id = ?`, not a soft-disable — there's no
`disabled` column on the table at all (see schema above). Alongside it:
- `UPDATE threads SET project_id = NULL WHERE project_id = ?` — every thread that belonged to the
  project falls back to being an ordinary, ungrouped thread, fully intact (messages, cost history,
  favorites status — nothing about the thread itself changes).
- The project's shared workspace directory (`<CodeExecWorkspaceDir>/<projectID>/`) is removed from
  disk — those files were the shared context for a project that no longer exists, and orphaned
  threads have no other claim on them (per-thread workspace addressing resumes via
  `ctx.WorkspaceKey` falling back to `ctx.ThreadID`, which was never that directory to begin with).

## Thread ↔ project mutability

A thread can move between projects (or in/out of having one at all) at any point after creation —
not fixed at creation time. Surfaced as a "Move to project" action in `ThreadMenu.svelte`, the same
place Favorite/Promote/Delete already live. Moving a thread does **not** move or copy any files —
its `ctx.WorkspaceKey` simply resolves differently on the next turn (the new project's shared
directory, or back to the thread's own directory if removed from a project). Worth calling out
plainly in the move-thread UI copy so it isn't a silent surprise the first time someone moves a
thread that had generated files.

## UI structure

### `/projects` hub

A dedicated route, same tier as `/pulsar`/`/daily`/`/constellation`. Card grid (name, one-line
description, updated-at) — structurally close to claude.ai's Projects hub, reskinned entirely in
Polaris's palette (`--color-surface`/`--color-surface-2` cards, `--color-accent` for the create
action, Lexend body text, no purple/blue gradient, no rounded chat-bubble chrome). Create/rename/
delete from here; tapping a card opens the detail view.

### Project detail view

Instructions editor (same textarea-with-char-count pattern `MemorySettings.svelte` already uses
for a named-entry-with-description field), a shared-workspace file list (upload straight in, same
attachment affordance the composer already has), the three per-project settings above, a "new
thread in this project" action, and a list of the project's own threads underneath (reusing the
existing thread-row component from `Sidebar.svelte` rather than inventing a second one).

### Sidebar

A new "Projects" section, same `.section-label` small-caps convention as Favorites/Recents
(`Sidebar.svelte:306`, `:315`) — sits alongside those two, listing project names as a shortcut into
`/projects/<id>`, not a full nested thread tree in the sidebar itself (that's what the detail view
is for). Matches the existing "sidebar is a fast-nav surface, detail lives in its own view" split
already established by Pulsar/Daily/Constellation.

### ThreadMenu: "Move to project"

New entry in `ThreadMenu.svelte` alongside Favorite/Promote/Delete — opens a small picker (project
list, plus "Remove from project" when the thread already has one).

## v1 scope

- `projects` table + `threads.project_id` migration.
- `ctx.WorkspaceKey` refactor across the four tool call sites + citation-URL minting.
- Project custom instructions concatenated into `{custom_instructions}` at turn-context build,
  plus the shared-workspace file-name opener.
- Default focus mode, memory mode (Default/None only), Constellation visibility — all three
  per-project settings.
- `/projects` hub, project detail view, sidebar section, ThreadMenu "Move to project."
- Delete: orphan threads, remove workspace files, real `DELETE` (no soft-disable).
- Threads freely movable between projects after creation.

## Explicitly out of scope for v1

- **Project-scoped memory store (v1.5/v2)** — the real isolated-memory work described above. The
  picker's third slot is reserved and visibly disabled in v1 specifically so this can land later
  without a UI redesign.
- **Public/shared project pages** — unrelated to issue #120 (public thread sharing); if that ships
  first, a project-level equivalent is a separate future decision, not bundled here.
- **Nested projects / projects-within-projects** — one flat namespace only.
- **Per-project model or cost limits** — projects don't touch billing/model-selection logic in v1;
  a thread's model choice works exactly as it does today regardless of project membership.
- **Moving/copying workspace files between projects** — a thread that changes projects leaves its
  old project's files behind entirely (see "Thread ↔ project mutability").

## Next steps

1. Land the `projects` schema + `threads.project_id` migration in `store/store.go`, with
   `store/projects.go` for CRUD (`CreateProject`, `GetProject`, `ListProjects`, `UpdateProject`,
   `DeleteProject` — the last one performing the orphan-and-cleanup sequence above in one
   transaction).
2. `ctx.WorkspaceKey` plumbing: add the field to `tools.Context`, compute it once in
   `gateway/turn.go`, retarget the four call sites named above.
3. Turn-context wiring: concatenate project instructions + opener into
   `agentCtx.CustomInstructions`; extend the memory-wiring gate with `memory_mode != "none"`; add
   the Constellation-visibility join to Weaver's candidate-pool query.
4. Frontend: `/projects` route + hub view, project detail view, sidebar section, `ThreadMenu`
   "Move to project," "new thread in project" entry point.
5. Live-verify per this repo's own standing practice (`CLAUDE.md`'s "Verify on real hardware, not
   just review or mocked tests") — specifically: two threads in the same project actually sharing
   a `code_exec`-written file without re-upload, a moved thread's workspace key actually changing
   on its next turn, and the Constellation opt-out actually excluding a project's threads from a
   live Weaver poll.
