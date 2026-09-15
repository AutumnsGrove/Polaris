# Workspace/attachment store unification

**Status: designed (2026-09-15).** Implementation still blocked on `docs/plans/docker-only.md`
landing — this design assumes every deployment has a workspace directory, which is only true once
bare-metal is gone. Once unblocked, this is buildable as-is; nothing below is a placeholder.

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
mechanisms to solve the same underlying job is what prompted asking whether one mechanism could
serve both.

## Why this was blocked on Docker-only

Two real incompatibilities, not just a refactor-effort question:

1. **The workspace mechanism is Docker-only by design** (`docs/plans/code-execution.md`'s
   "Deployment scope") — `CodeExecWorkspaceDir` doesn't exist on bare-metal. Attachments (PDF/image
   upload) work on bare-metal today. Folding uploads into "the workspace" would have made file
   uploads a Docker-only feature — a real regression under the two-deployment-model world. Once
   bare-metal is gone (`docs/plans/docker-only.md`), this stops being a blocker by construction.
2. **The two mechanisms had opposite, deliberate lifetime guarantees.** Resolved below — see
   "Decision: one store, workspace-shaped."

## Decision: one store, workspace-shaped

**Full merge, not a shared API over two backing stores.** The attachments table and the
single-read-then-deleted lifetime go away entirely. Every uploaded file — image, PDF, anything —
becomes a workspace file, persisted for the life of its thread, exactly like a `code_exec`-produced
file. There is only one storage mechanism after this lands, not two coordinated ones.

**Explicitly reversing the earlier lean toward "keep two stores behind a shared API."** That instinct
was about risk-avoidance while bare-metal still needed attachments to work independently of Docker.
With bare-metal gone, that reason no longer applies, and the earlier conversation's proposed
lifetime split (per-artifact "ephemeral" vs. "persistent" metadata) is rejected outright, not just
deferred: the single-read-then-deleted behavior was actively getting in the way of real use (asking
a follow-up question about a PDF from ten messages ago, or having `code_exec` parse an uploaded
dataset), and the privacy rationale behind it stopped being worth that cost. **No upload-type or
upload-size special-casing** — every attachment, regardless of type or size, becomes a permanent
workspace file. If retention ever becomes a real problem in practice, that's a future, separate
decision (see "Deliberately deferred" below) — not something to design around preemptively.

## Per-file identity: short IDs, same shape as thread IDs but shorter

Every file gets its own generated short hashed ID (same idea as thread IDs, but shorter, so the
model can reliably produce it in a tool call without transcription errors) instead of being
addressed only by `thread_id + filename` the way workspace files are today. The **short ID is what
the model deals with** — it's what gets referenced in prompts and tool calls, and what `code_exec`
opens as a filename inside its sandboxed working directory.

**The user-visible original filename survives, separately.** The model sees and uses the short ID;
the chat UI shows the real filename you uploaded (e.g. `Q3_Financial_Report.pdf`), never the hash.
Both need to be stored — the short ID as the file's actual on-disk/addressing identity, the original
filename as display-only metadata attached to it.

**The database keeps a pointer, not just the filesystem.** Rejected the "let the workspace directory
listing be the only source of truth" alternative — full observability was an explicit priority: a
lightweight row (message → short file ID → original filename) lets a message in a thread visibly
carry its attachment (a 📎-style chip in the transcript pointing at the exact message that
introduced the file), the same way `messages.attachment_filename` does today, rather than needing to
infer "which file belongs to which message" from upload timestamps or directory listings.

## How the model finds out a file exists

No eager processing at upload time. Today's `resolveAttachment` (`gateway/turn.go`) does real, costly
work up front — extracting a PDF preview, running a synthetic vision-model call to describe an
image — specifically *because* the file was about to be deleted and this was the model's only
chance to see it. That urgency disappears once the file just persists. Once it's a permanent
workspace file, the model only pays to look at it when it actually decides to.

**Mechanism: a short injected note in the turn, not a separate out-of-band signal.** When a file is
attached, a line like `A file has been included named <short_id>.<ext>.` gets appended to the
model-facing prompt for that turn — the same "augment the model-facing text, leave the persisted
message alone" split `resolveAttachment` already uses today, just carrying a pointer instead of
extracted content.

**If the user's own message is itself a question about the attachment, the injected note says so
explicitly** — e.g. `A file has been included named <short_id>.png. Read it to answer this
question.` — rather than leaving the model to infer relevance on its own. This is a deliberate
middle ground: not full eager processing (that cost is gone), but not silent either — a message that's
obviously about the attachment shouldn't require the model to guess it should go look.

**`code_exec` needs no new "list files" capability — it already has one.** Confirmed directly
against the real sandbox script (`compose/watcher/codeexec.sh`): the sandbox only blocks network
access (`--network none`) and privilege escalation (`--cap-drop=ALL`, read-only root except `/tmp`
and the workspace mount) — it's a full, unrestricted Python interpreter otherwise. `os.listdir(".")`
or `subprocess.run(["ls"])` both work today with zero new code. Any future "give the model a
structured file listing" affordance would be a convenience on top of this, not a missing capability.

## What this deletes/simplifies in existing code

- **`resolveAttachment`'s eager-processing pipeline** (`gateway/turn.go`) — the PDF-preview
  extraction and the synthetic vision-model description call both go away, replaced by "save the
  file into the thread's workspace, inject one line of pointer text." A real code simplification,
  not just a behavior change — most of what that function does today exists solely to front-load
  work before a file it assumed was about to disappear.
- **`removeAttachmentFile` and the single-read-then-deleted lifecycle** — gone. Nothing deletes an
  uploaded file after one read anymore.
- **The `read_attachment` tool** (`tools/catalog.go`'s `Requires: "attachment"` entry) — retired
  entirely. Its whole job (page through/search the current turn's PDF) is now strictly a subset of
  what `code_exec` can already do against any workspace file (open it, convert it, `grep` it,
  extract tables), scoped to more than just the current turn. Two tools doing overlapping jobs
  collapses to one.
- **The attachments table/directory** (`cfg.Attachments.Dir`, `messages.attachment_filename`/
  `attachment_content_type` in their current write-only-then-deleted shape) — replaced by the
  message → short-file-ID pointer described above; no separate attachments storage survives.
- **`gateway/workspace.go`'s serving route** — already type-agnostic (`http.DetectContentType`,
  no image-only assumption), so it needs no new serving mechanism, only a change from
  `thread_id + filename` addressing to the new short-ID addressing scheme. This route already does
  the job attachments would otherwise need a second serving path for.

## Deliberately deferred, not decided here

- **Retention/garbage collection.** No expiry policy — files persist indefinitely, same as
  workspace files do today. Explicitly not a concern right now (ample storage headroom, individual
  files expected to stay small); revisit only if it becomes a real, observed problem in practice,
  not preemptively.
- **Exact upload size limits.** Worth a real number at implementation time (today's eager pipeline
  had implicit limits baked into what a single LLM call could absorb; that constraint is gone, so
  the new limit should be chosen deliberately, not inherited by accident) — not a design-shape
  question, so left for implementation.

## Dependency on the Docker-only dev loop

Testing file upload locally now depends on `docs/plans/docker-only.md`'s "Dev loop: code_exec must
still be fully testable" section being fully wired up (Docker installed on the dev machine for the
sandbox only, the watcher script running locally, dev's `config.yaml` pointing at real workspace/
signal-dir paths) — see that doc, which now treats this as a required, decided piece of the work
rather than an open item, specifically because of this dependency.
