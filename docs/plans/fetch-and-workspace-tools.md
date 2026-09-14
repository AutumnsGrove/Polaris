# Fetch tool, persistent workspace, and read_attachment's extension

**Status: `fetch_url` shipped and live-verified (2026-09-14)** — `tools/fetch_url.go`,
`tools/descriptions/fetch_url.yaml`, wired into `catalog.go` under the same `docker_only` gate as
`code_exec`. Both provenance paths from the design below are implemented: a `url` checked against
`ctx.CitationsSnapshot()`, and a `card_index` resolved against `ctx.CardsSnapshot()` (mirroring
`view_image`'s own card_index handling) — no free-typed URL ever reaches the fetch. Content-type/
magic-byte allowlist and a 20MB size cap are enforced before anything touches disk (see
`fetchURLContentAllowed`/`fetchURLBytes`). Live-verified end to end against a real running
`polaris` (Docker mode) + real SearXNG + `dev/fakeopenrouter`: `image_search` → `fetch_url` (by
`card_index`) → `show` rendered a real fetched photo inline, workspace file written with the exact
fetched bytes. `read_attachment`'s extension (offering it whenever the workspace holds a PDF, not
just this turn's upload) is **not yet implemented** — still open, tracked separately.

This doc covers the "get untrusted external content safely to the model" half of the code-execution
work: how a file actually gets from the web into somewhere `code_exec` or `read_attachment` can use
it, and how it survives across turns once it's there. `docs/plans/view-image.md` covers the
separate (but related) question of how the model actually *looks at* an image once one exists
locally — split out on request since it's substantial enough to deserve its own doc.

## The gap this closes

Two real, confirmed gaps surfaced discussing `docs/plans/code-execution.md`, not hypothetical ones:

1. **Nothing can get a structured data file (CSV, Parquet, SQLite, a large PDF) into the sandbox at
   all.** `web_read` (`tools/web_read.go`) only ever produces *extracted text* — HTML stripped to a
   readable string via goquery, or a PDF turned into text via `ExtractPDFText`. There's no path
   today for "download these raw bytes and let `code_exec` load them with pandas/sqlite3."
2. **`image_search`'s own result URLs never reach the model.** Checked `tools/image_search.go`
   directly: `finishImageSearch` returns the model a string like *"found 3 images — they're
   attached to this turn's answer, no need to describe them individually"* — the real
   `ImageURL`/`FullImageURL` values only ever populate `Card` structs for frontend rendering. The
   model has no way to reference a specific result at all today, image-fetching or otherwise.

## Why this needs to be *stricter* than `web_read`, not just a variant of it

Worth being explicit about an apparent inconsistency: `web_read` today lets the model fetch **any**
URL it types, protected only by `search.Blocklist` and the SSRF-safe dialer (`safeDialContext` in
`web_read.go`) — no restriction on *which* URL, no content-type check, no size cap beyond the flat
20MB response ceiling (`maxResponseBytes`). A new fetch tool deserves real additional restriction
that `web_read` doesn't have, and the reason isn't inconsistency for its own sake: `web_read` only
ever produces a *string* — worst case, a malicious page feeds weird text into the model's context
(a prompt-injection risk this codebase already accepts and lives with elsewhere). A fetch tool that
saves **raw bytes of an arbitrary content type**, later opened by real parsing libraries
(`PIL.Image.open`, `pandas.read_csv`, `pyarrow.parquet`, `sqlite3.connect`) inside `code_exec`, is a
materially larger attack surface — real parser CVEs, decompression bombs, disk-filling attachments.
That's what justifies real restriction here that `web_read` doesn't need for itself.

## `fetch_url`: provenance-scoped, content-validated, workspace-writing

A new tool, not an extension of `web_read` (different contract entirely: raw bytes to a file, not
extracted text as a return value).

**Provenance — the specific mechanism differs by *how* the model already has the URL**, because
the two source tools expose URLs differently:

- **From `web_search`/`web_read` results** — the model already has the real URL as plain text
  (that's how citations work today; unlike `image_search`, nothing is withheld here). `fetch_url`
  checks the given URL against an allowlist built from this thread's own already-seen tool results
  (citations recorded via `ctx.AddCitation`/`ctx.CitationsSnapshot()`) — it must match something
  the model was actually shown, not a string the model invented or a lookalike domain it guessed
  at. This doesn't fully close a determined-attacker gap (a real search result *can* rank a
  convincingly-disguised phishing/malware-hosting page — SEO poisoning is a documented real-world
  technique), but it's a meaningfully narrower surface than "the model can type any URL."
- **From `image_search` results** — there is no raw URL to check against, because `image_search`
  never gives the model one (see the gap above). `fetch_url` accepts an opaque **card index**
  instead (e.g. "fetch card 3" from this turn's image results), resolved server-side to the real
  `FullImageURL` already sitting in that `Card`. This is a deliberate design choice, not a
  workaround: an index is strictly *less* for the model to juggle than a URL, and it structurally
  can't reference anything that didn't come from a genuine search result, closing the
  lookalike-domain risk completely for this path (there's no string for the model to mistype or
  invent in the first place).

**Content validation, regardless of provenance path**:
- Content-Type / magic-byte check against an expected allowlist (`image/png`, `image/jpeg`,
  `text/csv`, `application/json`, `application/octet-stream` only when the URL extension strongly
  implies a known format like `.parquet`/`.sqlite`) — rejects anything that isn't a plausible
  data/image file outright, independent of how trusted the source URL is.
- A size cap (candidate: 10-20MB) rather than trusting the remote server's `Content-Length` header,
  same `readLimited`-style hard stop `web_read.go` already uses for its own 20MB response ceiling.
- The fetch always runs host-side, through `safeDialContext`'s existing SSRF/DNS-rebinding
  protection — never from inside the sandbox, which stays `--network none` permanently regardless
  of whether this tool exists (see `code-execution.md`'s "Network access from executed code"
  section). The fetch and the execution deliberately never share a trust boundary: even a
  successfully-downloaded malicious file only ever gets *processed* inside the already-sealed,
  non-root, resource-capped, network-off sandbox.

**Where the file lands**: `<workspace_root>/<thread_id>/<filename>` — the same persistent,
per-thread directory `code_exec` always mounts (see `code-execution.md`'s "File persistence"
section). `fetch_url` and `code_exec` share one workspace concept, not two.

## `read_attachment`'s extension

`read_attachment` (`tools/read_attachment.go`) is PDF-specific — its actual machinery
(`pdfPageText`, `searchPDFPages`) only knows how to page through or literal-search a PDF, nothing
else. That scope stays the same; what changes is *where* the PDF it operates on can come from and
*when* it's offered as a tool:

- **Today**: only offered when `ctx.AttachmentData` is non-empty — i.e. only for a PDF uploaded
  *this specific turn*, gone forever after (`catalog.go`'s conditional-tool-definition pattern,
  the same mechanism `view-image.md`'s `see` mode reuses for its own gating).
- **Extended**: also offered whenever the thread's workspace directory contains a PDF, from either
  an upload (which now also lands in the workspace — see `code-execution.md`'s "Uploaded
  attachments join the same workspace") or a `fetch_url` call. This is the same conditional-tool
  mechanism, just a wider trigger condition — no new detection machinery needed, just checking the
  workspace directory in addition to `ctx.AttachmentData`.
- **New parameter needed**: `path` (optional), to disambiguate which PDF once more than one has
  accumulated in a thread's workspace over its lifetime — defaults to the current turn's fresh
  upload when that's what's happening, otherwise required when multiple candidates exist. Exact
  shape (required vs. auto-selecting the most recent, error message when ambiguous) still needs
  deciding at implementation time, not resolved here.

Deliberately **not** extending `read_attachment` into a general "read any downloaded file" tool —
non-PDF files (CSV, JSON, Parquet, SQLite) are already well served by `code_exec` itself (a
one-line `print(open(path).read()[:2000])` or a real `pd.read_csv(...)` call), and building a
second, parallel "preview any file" tool for cases `code_exec` already covers would be duplicate
surface for no real capability gain.

## Open items, deliberately not resolved here

- Exact `fetch_url` size cap and content-type allowlist — needs real numbers, not a guess, same
  "measure, don't guess" culture as `code-execution.md`'s memory test.
- `read_attachment`'s `path`-disambiguation UX when multiple fetched PDFs exist in one thread.
- Whether an `image_search` card reference should expire (only fetchable within the same turn it
  was returned) or remain valid for the life of the thread — affects how long `Card` data needs to
  be retrievable server-side beyond just rendering the frontend gallery once.
