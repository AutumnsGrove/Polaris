# Workspace/attachment store unification

**Status: not yet designed — blocked on `docs/plans/docker-only.md` landing.** This stub captures
the motivating discussion so it isn't lost; the real design pass (concrete types, migration,
frontend changes) happens once Docker-only convergence is decided and in progress, since this
plan's central blocker only goes away once every deployment is guaranteed to have a workspace
directory.

## The observation that started this

Three distinct file-serving/storage mechanisms exist today for conceptually similar jobs —
"here's a file the model produced or the user gave it, show it and/or let them download it":

1. **Attachments** — DB-tracked (`messages.attachment_filename`/`attachment_content_type`), bytes
   on disk keyed by UUID in `cfg.Attachments.Dir`. Currently write-only from user uploads;
   deliberately single-read-then-deleted (`gateway/turn.go`'s `removeAttachmentFile`) — a privacy-
   relevant design choice, not an oversight.
2. **Workspace files** — per-thread directory (`<CodeExecWorkspaceDir>/<ThreadID>/`), generic
   `http.DetectContentType`-based serving route (`gateway/workspace.go`). Used by
   `code_exec`/`fetch_url`/`show`/`view_image`. Persists for the life of a thread, by design
   (code_exec's whole value is "the dataset is still there on turn 10").
3. **Cards** (`image_search`, recommendation tools) — no local bytes at all; `ImageURL`/
   `FullImageURL` point at a third party's server. Not a candidate for unification — proxying a
   search result's image through Polaris just to give it a "download" affordance is new cost
   (fetch latency, bandwidth, must run back through `web_read.go`'s blocklist/SSRF checks) for a
   browser feature (right-click → save) users already have for free. Out of scope for this doc.

The concrete trigger: issue #41 (report-generator) wants a Polaris-generated Markdown report to be
downloadable via the attachments mechanism; separately, code_exec's workspace has no equivalent
"give the user a download link" story for a generated CSV/cleaned dataset — `show`/`view_image`
only ever treat a workspace file as an image. Two different tools reaching for two different
mechanisms to solve the same underlying job (`docs/plans/report-generator.md`'s "Output shape"
section vs. code_exec's workspace) is what prompted asking whether one mechanism could serve both.

## Why this was blocked on Docker-only

Two real incompatibilities, not just a refactor-effort question:

1. **The workspace mechanism is Docker-only by design** (`docs/plans/code-execution.md`'s
   "Deployment scope") — `CodeExecWorkspaceDir` doesn't exist on bare-metal. Attachments (PDF/image
   upload) work on bare-metal today. Folding uploads into "the workspace" would have made file
   uploads a Docker-only feature — a real regression under the two-deployment-model world. Once
   bare-metal is gone (`docs/plans/docker-only.md`), this stops being a blocker by construction.
2. **The two mechanisms have opposite, deliberate lifetime guarantees** — attachments are
   single-read-then-deleted; workspace files persist indefinitely per thread. This tension is
   independent of Docker-only and still needs a real decision in the eventual design pass (does
   unification mean picking one lifetime and breaking the other's guarantee, or does the unified
   mechanism need per-artifact lifetime metadata — e.g. "ephemeral, delete after this turn" vs.
   "persistent, keep for the thread's life" — rather than one fixed policy for everything stored
   there?).

## Not yet decided (real design pass needed once unblocked)

- Single physical store (merge the attachments table and workspace directory into one thing) vs. a
  shared tool-facing API/type over two backing stores that keep their own lifetime semantics —
  the earlier conversation leaned toward the latter as lower-risk; worth revisiting once bare-metal
  is actually gone and the "must also work without Docker" constraint no longer forces caution.
- Per-message identity for a persistent artifact (a report's "Download report.md" must stay
  attached to one specific message forever) vs. the workspace's current `thread_id + filename` only
  granularity — needs a namespacing answer (e.g. a UUID-prefixed filename) regardless of which
  physical-store option above is chosen.
- Retention/garbage-collection policy for anything that becomes persistent under the unified
  model — today's workspace directory has no expiry story at all; extending its lifetime
  guarantees to more content types (a Polaris-generated report, a cleaned dataset) makes "how long
  does this live and does anything ever clean it up" a real question instead of a theoretical one.
