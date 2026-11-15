# Public sharing ("share links")

**Added: 2026-11-15.**

**Status:** design only, no code yet. Issue #120. Written up after the `pixelsort-go` share
Worker (`PLAN.md` §6b, `docs/dev/phase-6b.md`) proved the pattern on the same hardware. Open
questions were settled in a design conversation on 2026-11-15 (domain, R2 in v1, one send-out
modal, the link back to the repo); "Decisions" records what was chosen and why.

**Relationship to `docs/plans/thread-export.md`:** export is a private file for the operator.
This is one deliberate, internet-reachable publication of a *snapshot*. They share the
intermediate document (`threadexport.Doc`) and nothing else — `threadexport/types.go`'s package
comment already says #120 should reuse the Doc without dragging in the schema.

---

## The idea

Polaris never becomes internet-facing. A small Cloudflare Worker — a D1 database for the snapshot,
an R2 bucket for the images inside it — is the only thing on the public internet; Polaris *pushes*
a finished thread to it and serves nothing itself.

```
   Tailnet only                                        Public internet
 ┌───────────────┐        signed PUT (HMAC)        ┌───────────────────────┐
 │ polaris       │ ──────────────────────────────► │ polaris-share Worker  │
 │ container     │        signed DELETE            │  ├─ D1: snapshot row  │
 │ + SQLite      │                                 │  └─ R2: its images    │
 └───────────────┘                                 │                       │
        ▲                                          │  GET /s/{token} ──────┼──► anyone with
        └── operator's browser (tailnet)           └───────────────────────┘    the link
                                                                │
                                                                └── renders static HTML
                                                                    + serves the images
```

This is `pixelsort-go`'s Worker with the album swapped for a document: same rule (**the Cloudflare
secret lives only on the potato/container**), same signing scheme, same R2 upload path for images,
same "unlisted, not private" honesty, same revoke-by-delete. The only job dropped is the `/fetch`
relay, which Polaris doesn't need — `web_read` already does that work inside the container under
its own guards.

## Why this, and not Tailscale Funnel

Issue #120 suggests Funnel first, as the zero-infra option. Given the precedent, the Worker is
better on every axis that matters, and it removes the issue's worst tradeoff:

| | Tailscale Funnel | Share Worker |
|---|---|---|
| What's exposed | The whole Polaris process, one route away from `/ws`, `/api/ask`, tool dispatch | A static HTML renderer that can't do anything |
| `SECURITY.md` boundary | Broken — auth-by-network on a process with no auth, served to the internet | Unchanged. `gateway/server.go` gains no public route |
| Live vs snapshot | Must decide | **Forced to snapshot** (the Worker can't read SQLite) |
| Cost of a routing bug | Full app read/write | Someone reads a document |
| New infra | Enable Funnel on the node | One wrangler project (already have the account, token pattern, and a working template) |

The snapshot question in #120 ("live or frozen?") is answered *structurally*: the public renderer
has no path back to the database. That's the safest answer, so we take it and stop treating it as
a compromise. A share is an explicit **publish**, not a standing hole.

## What leaves, and what doesn't

The snapshot is `threadexport.Doc` — built by the existing `threadexport.Build`, serialised by the
existing `RenderJSON`. No new document model.

Share defaults are **stricter than export defaults**, deliberately, because the audience is
different (public vs. operator):

| Option | Export default | Share default | Why |
|---|---|---|---|
| Sources and citations | on | **on** | Sourcing is the product; it's the whole point of sharing |
| Attachments (names/types) | off | **on** | The images are half of why a research thread is worth sharing; non-image files are listed by name only |
| Cost / model metadata | off | off | No reason to publish spend |
| Research trail | off | off (choice) | Opt-in, same reasoning as export |
| Thinking traces | off | **not offered** | v1 keeps the public surface small; revisit if audit-sharing is ever wanted |
| Private (memory calls) | on | **on, pinned disabled** | There is no version of a public page that should carry memory contents, so offering the switch would only be a way to get it wrong |
| Private personal (chat search, star reads, save-to-field) | off | **on, still toggleable** | Starts stricter than the download tab, because a public page showing what she was reading is a different leak than a private file — but one deliberate flip is her call |

`Doc.Omitted` already records what was dropped — the Worker renders that list in the footer
("3 memory lookups omitted"), so a share is honest about being partial rather than silently
pruned. Citations keep `title`, `site_name`, `url`, `opened`; `image_url` is **stripped** (it would
make every viewer's browser call a third-party host, and would force a loose CSP).

### Images go to R2, in v1

A research thread with a screenshot or a generated image in it is exactly the thing worth sharing,
so images ship with v1 rather than later. They go to **R2, never D1**: a D1 row caps at 2 MB,
base64 inflates 33%, and D1 writes are the expensive meter, whereas R2's free tier is 10 GB with no
egress charge — and the upload path already exists, tested, in `pixelsort-go/worker/src/albums.ts`.

What actually gets published is resolved by a new `share/images.go` on the Go side, walking the
built `Doc` for two sources (both already precedented):

- **User-uploaded attachments** — `store.Attachment.WorkspaceFileID`, the generated filename in
  `CodeExec.WorkspaceDir/<thread_id>/`. `gateway/comet_approach.go` already gathers thread images
  exactly this way for the vision pass; reuse its shape, not its code.
- **Images a tool put in an answer** — `![](/api/workspace/<thread>/<file>)` markdown, which
  `tools/show.go` writes for charts and generated images.

Each candidate is resolved through the same path-traversal guard as
`gateway/workspace.go`/`tools/view_image.go` (never against an attacker-built intermediate base —
see that file's comment on why), checked for `image/*` by sniffing rather than trusting the
extension, capped, uploaded, and **rewritten in the markdown** to `/s/{token}/{n}`. An image that
fails any check is replaced with a plain-text note in place ("chart.png — not included"), never a
broken `img`. Non-image attachments keep today's text-only treatment.

Caps: 20 images per share, 10 MB each. No thumbnails — the viewer lazy-loads and clamps with CSS,
and a text-first page has no grid to fill.

## The Worker

`worker/` — a second wrangler project (`polaris-share`), same layout as `pixelsort-go/worker/`.

| Route | Auth | Does |
|---|---|---|
| `PUT /api/shares/{token}` | signed | create or replace the snapshot (idempotent, so "update share" is a re-push) |
| `PUT /api/shares/{token}/images/{n}` | signed | store image n (verified SHA-256, sniffed `image/*`, 10 MB cap) |
| `DELETE /api/shares/{token}` | signed | revoke: delete the row **and** every object under `shares/{token}/`. Idempotent `204` |
| `GET /api/shares/{token}` | signed | liveness check (`404` = gone), for the settings list |
| `GET /s/{token}` | public | the viewer page |
| `GET /s/{token}/{n}` | public | one image, `Cache-Control: public, max-age=3600` |
| `GET /s/{token}.json` | public | the same snapshot as JSON — the open loop, and it makes the viewer debuggable |
| `GET /robots.txt` | public | `Disallow: /` |
| anything else | | `404` |

Token: `^[a-z2-7]{26}$` (128 random bits, base32) — minted by **Go**, never by the Worker, so the
local `thread_shares` row exists before the push and a failed push leaves nothing orphaned.

### D1

```sql
CREATE TABLE IF NOT EXISTS shares (
  token      TEXT PRIMARY KEY,   -- 26-char base32
  v          INTEGER NOT NULL,   -- document schema version (threadexport.Version)
  kind       TEXT NOT NULL,      -- 'thread' today; 'star' / 'daily' later
  title      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,   -- unix seconds
  expires_at INTEGER,            -- NULL = never
  bytes      INTEGER NOT NULL,
  doc        TEXT NOT NULL       -- the snapshot JSON
);
CREATE INDEX IF NOT EXISTS shares_expires ON shares(expires_at);
```

One row per share; images live beside it in R2 under `shares/{token}/`, uploaded in the same signed
batch and deleted with the row. The 2 MB row cap is real, so the push is refused above **1 MB**
with a clear `413`, surfaced in the modal as "this thread is too large to share as a link — export
it instead" (never a silent truncation). If a monster thread ever makes that bite, the escape hatch
is one row per turn (`share_turns(token, idx, json)`) — noted, not built.

`kind` is the only concession to "sharing *things*" beyond threads: Constellation stars, Daily
editions and Comet periods are all already structured data, so each is a new `kind` + a case in the
renderer, not a second Worker. Nothing else changes.

### The page itself

Strangers will land on this page and not know what made it, so the page answers that once, quietly,
and points at the repo — the same move `pixelsort-go` makes in its settings footer ("source on
GitHub"), and the same reason a shared Perplexity thread shows you Perplexity. Sharing a thread is
how the project gets seen: here is a question, here is the answer, here are the sources it actually
opened, and here is the thing that did it.

- **The wordmark is the link.** Top-left `Polaris` → `https://github.com/AutumnsGrove/Polaris`,
  `rel="noopener nofollow"`, in the viewer's own type. No second "learn more" anywhere; one
  outbound link, in the one place a person already looks. (The Worker has no home page, so unlike
  `pixelsort-go`'s viewer this link goes somewhere real — that wordmark pointing at `/` was a dead
  link on every share they ever made.)
- **The footer says what it is**, in one line, no link: *"Shared from Polaris — a self-hosted
  research assistant that cites its sources. This is a snapshot from 16 Nov; it does not update."*
- **`Referrer-Policy: no-referrer` is load-bearing here, not boilerplate.** The token is in the URL,
  so if the page sent a `Referer`, every click to GitHub would hand the repo owner a working share
  link. Set on the response, it applies to the outbound link too.
- **Unfurls.** `og:title` (the thread title), `og:description` (the opening of the first answer,
  clamped, HTML-stripped), `og:site_name: Polaris`, `og:type: article`. A link pasted into Discord
  or Mastodon has to look like a piece of research, not a bare URL — that is half of "this is how
  we found it". No `og:image` in v1 (there is nothing honest to put there yet).
- **The gone page has no outbound link.** It exists to say nothing is kept; sending a 404'd
  visitor to the repo is a non-sequitur.

### Hardening

- Signed requests: `POL1\nMETHOD\nPATH\nTS\nBODYSHA`, HMAC-SHA256, 300 s skew window,
  constant-time compare, and the body hash re-verified against the bytes actually received. The
  `POL1` prefix (vs `pixelsort`'s `PS1`) means a signature can't be replayed against the other
  Worker even if the secrets were ever the same.
- One shared `vectors.json` loaded by both the Worker tests and the Go tests — this is the thing
  that actually caught drift in `pixelsort-go`, not review.
- R2 writes carry R2's own `sha256` option, so a body that doesn't hash to the declared value is
  refused by R2 rather than by our code.
- Viewer page: server-rendered HTML, `default-src 'none'; img-src 'self'; style-src 'unsafe-inline';
  script-src 'nonce-…'` with a per-response nonce, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`, `Cache-Control: no-store`.
  Images are `unsafe-inline`-free CSS, `loading="lazy"`, and served `nosniff` from our own origin —
  which is why stripping citation `image_url`s is what keeps the CSP this tight.
- One `escapeHtml`, and answers rendered through `marked` with `html:false` (the model emits
  Markdown; don't hand-roll a renderer and don't trust the text as HTML).
- Expired, revoked and never-existed all serve the **same** 404 page, with no hint which.
- System font stacks only, no third-party requests of any kind.

### Expiry and revoke

Optional expiry (never / 7 / 30 / 90 days; default **never** — a link posted as proof shouldn't rot
without being told). Checked on read, plus a daily cron sweep that deletes expired rows.

## The Polaris side

- **Config** `share:` (`worker_url`, `secret`) fed by `POLARIS_SHARE_WORKER_URL` /
  `POLARIS_SHARE_WORKER_SECRET`. Both unset ⇒ the whole feature is dark: `POST /api/shares`
  answers `503`, and `GET /api/shares` tells the UI to hide Share. That means the four places in
  `CLAUDE.md`'s checklist move together: `.env.example`, `docker-compose.yml`'s passthrough list,
  `compose/polaris/config.yaml.example`, `config/config.go`. No new Dockerfile `COPY` — nothing
  here is hot-reloadable from disk.
- **`share/`** — new package: `sign.go` + `client.go` (a close port of
  `pixelsort-go/server/share/client.go`: retries on network/5xx, `Opener` for idempotent PUTs, the
  secret never in an error string or log line) and `images.go`, the resolver and URL rewriter
  described under "Images go to R2" above. Ordering matters: images upload first, then the
  snapshot is pushed with their public paths already baked in, so a half-finished share can never
  be served pointing at images that don't exist yet.
- **`store/thread_shares.go`** + one migration:

```sql
CREATE TABLE IF NOT EXISTS thread_shares (
  token      TEXT PRIMARY KEY,
  thread_id  TEXT NOT NULL,
  url        TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at DATETIME,          -- NULL = never
  revoked_at DATETIME,          -- NULL = live
  bytes      INTEGER NOT NULL DEFAULT 0
);
```

  One live share per thread (partial unique index on `thread_id WHERE revoked_at IS NULL`), so
  Share on an already-shared thread shows the existing link with Copy / Update / Revoke rather than
  quietly minting a second one.
- **`gateway/shares.go`** — `POST /api/shares` `{thread_id, expires_in_days?, trail?}`,
  `PATCH /api/shares/{token}` (re-push an updated snapshot), `DELETE /api/shares/{token}`,
  `GET /api/shares` (live + revoked, for settings). Building the snapshot is three existing calls:
  `db.ThreadExportInput` → `threadexport.Build` → `RenderJSON`.
- **`cmd/shares.go`** — `polaris share <thread-id>`, `polaris share list`, `polaris share revoke
  <token>`, thin clients over the REST API like every other CLI command. Late phase; the UI comes
  first.

### The footgun that needs designing around, not documenting around

**A snapshot outlives the thread it came from.** Deleting or forgetting a thread does not delete
its public copy. Three responses, all needed:

1. The delete confirmation says so when a live share exists (one line, no modal-in-a-modal).
2. `forget`/ghost-purge marks the thread's shares revoked locally **inside the same transaction**
   (same reasoning as `deleteGhostThreads` purging embeddings — forgetting a conversation that
   stays publicly readable is the exact failure that rule exists to prevent).
3. A reconciler on the Pulsar scheduler tick pushes pending revocations to the Worker, retrying —
   because the Worker can be down, and "revoked locally, still live publicly" is the worst state
   to leave silently. `thread_shares.revoked_at` set but not yet confirmed gets one retry per tick
   until the Worker 404s.

## Frontend

- **One "Send this thread out" modal**, not two: `ExportModal.svelte` grows a tab pair —
  **Download** (today's panel, unchanged) and **Share link**. One privacy matrix instead of two
  that drift, one availability summary ("14 turns · 23 sources · 2 images · 1 lookup omitted",
  built from the `dry_run` availability the export endpoint already returns).
  The tabs hold *independent* option state so the stricter share defaults don't get silently
  adopted by, or bleed into, the download panel: on the Share tab, memory calls are pinned on
  (disabled, with the reason in a line beneath), the personal-use toggle starts on but is still
  hers to flip, and thinking traces aren't offered at all.
  Share has two states: not shared (create + expiry) / shared (link, copy, update snapshot,
  revoke, "shared 16 Nov"). Expiry is four choices, and **Never** is the default: *Never · 7 days ·
  30 days · 90 days*. Chosen under pressure, "Never" is the one that can't accidentally rot a link
  someone bookmarked; the other three are there for a thread posted into a busy thread where the
  answer will age.
- **Settings → Shared links** — every live share, newest first: title, created, expiry, revoke.
  This is the page that makes revoke discoverable without hunting through threads.
- **`HelpModal.svelte` `TERMS`** — a "Share link" entry ("a public snapshot of one thread"),
  per the glossary rule in `CLAUDE.md`.
- Thread menu icon stays quiet; no badge, no banner. A shared thread gets a small mark next to its
  title in the sidebar so a live link is never invisible.

## Threats and answers

| Threat | Answer |
|---|---|
| Bots/scanners hitting a posted URL | Token in the path, no index, `robots.txt`, `noindex`; there is no list route and no way to enumerate |
| Revoked link still cached by someone | Revoke deletes the row; `no-store`; snapshots are text, and the person who already read it already read it — say so in the UI |
| A routing bug exposing the app | There is no app on the Worker. Everything not in the table above is `404` |
| The shared secret leaking | It lives in one `.env` on the potato and one Worker secret, same as every other key here. It is never sent to a browser |
| Publishing something private by accident | Stricter defaults, `Omitted` shown, summary before publish, and a revoke that works |
| A malicious thread rendering script into the viewer | `marked` with `html:false`, one `escapeHtml`, nonce CSP, `nosniff` |
| A published image carrying EXIF/GPS | Re-encoded on upload (see Decisions), so it can't — and WebP/GIF/AVIF are refused rather than passed through unexamined |
| Someone hotlinking the images | They're `no-store`-ish public objects with unguessable paths, same as the page. Not worth an origin check |
| The repo link handing over a live token | `Referrer-Policy: no-referrer` on every page response; asserted in a vitest case |
| Stale snapshot read as current | Footer carries "shared <date>" and the viewer never claims to be live |

## Limits and cost

Workers Free: 100k requests/day. D1 Free: 10 databases (one used), 500 MB per database, 5 GB per
account, 5M rows read + 100k rows written per day, 50 queries per Worker invocation. R2 Free: 10 GB
stored, egress free. A page view is 1 request + 1–2 queries + a handful of rows, plus one request
per image. Tens of thousands of views/month is free. The writes budget is shared with the
`pixelsort-share` Worker only if they're on the same account — worth checking once, not worth
engineering for.

## Out of scope

No public index, search, comments, reactions, view counters, editing from the web, viewer auth
beyond the token, live updates, full-`GetThread` passthrough, or re-importing a share into Polaris.
No per-share passwords. No custom slug chosen by the operator (tokens are random or nothing). No
video or audio: `tts_audio_file_id` is a workspace file too, but a share is read-first, so a spoken
answer is one line in `Omitted`, not a player.

## Phases

| # | Work | Exit criteria |
|---|---|---|
| 1 | Worker scaffold + D1 schema + R2 bucket + signing (`POL1`), `vectors.json` | `pnpm --dir worker test` green; Go signer reproduces every vector; unsigned/bad-body writes are `401`/`400` |
| 2 | Viewer page, `/s/{token}`, `/s/{token}/{n}`, `/s/{token}.json`, gone page, CSP, unfurl meta | vitest covers escaping, `</script>` in content, nonce rotation, `noindex`, `no-referrer` on the outbound link, identical 404 for missing/expired/revoked |
| 3 | Go: `share/` client + `images.go` resolver, `thread_shares`, `gateway/shares.go`, config + env wiring | `go test ./...` green against a fake Worker; feature dark with no config; a real thread with a generated image pushed, fetched, and revoked live |
| 4 | Frontend: tabbed send-out modal, settings list, delete warning, sidebar mark, help term | Playwright: share a fixture thread, open the real `wrangler dev --local` Worker, see the answer, sources and the image, revoke, see the gone page |
| 5 | Deploy: `deploy-worker.yml`, `d1 migrations apply`, the `polaris.grove.place` route, docs | A real link opens on the real domain, images and all; `SECURITY.md`, `docs/FEATURES.md`, `SETUP.md`, `DEVELOPMENT.md` updated |
| 6 (maybe) | `cmd/shares.go`; `kind: star` / `kind: daily` | — |

## Decisions

- **Snapshot, not live.** Forced by the architecture, and the right answer anyway.
- **D1 for the document, R2 for its images** — both in v1. Text is queryable and revocable with a
  `DELETE`; images are what D1 is worst at.
- **Worker, not Tailscale Funnel.** Keeps `SECURITY.md`'s boundary literally true.
- **`polaris.grove.place`**, a Worker route on the grove zone, so links stay inside the family:
  `https://polaris.grove.place/s/{token}`. (One name to watch: if Polaris ever wants a public web
  presence of its own, `polaris.` is taken by a share viewer. Nothing to do about it today, just
  where the collision would show up.)
- **The wordmark links to the repo, and it's the only outbound link.** Sharing is how the project
  gets seen; the token never leaks to it (`no-referrer`).
- **One live share per thread**; "update share" re-pushes rather than minting a second token.
- **One send-out modal, two tabs** — Download and Share link, independent option state.
- **Share defaults stricter than export defaults**, and thinking traces not offered at all.
- **`image_url` stripped** from citations; source lists are text.
- **A share renders the export `Doc` and nothing else** — the same content, with image URLs
  rewritten to the Worker's own copies. That invariant is what keeps the public page and
  `polaris export` from ever disagreeing — and it is why charts are absent: the Doc does not carry
  `messages.chart` yet ("reserved for the rich-blocks pass"), so the share lists them in `Omitted`
  rather than growing a second renderer.
- **Expiry: never by default**, with 7 / 30 / 90 days offered. A link posted as proof should not rot
  invisibly; revoking is one click.
- **EXIF is removed by re-encoding, not by hoping.** PNG re-encoded losslessly, JPEG at q85 and
  clamped to 2400px wide — both through Go's stdlib `image` packages, so metadata cannot survive the
  round trip. WebP/GIF/AVIF/HEIC are refused with a text note instead of a half-measure: no stdlib
  decoder, and a wrong guess here is a photo's location on a public page.
- **Free tier is the target**; no paid Workers plan needed for this.

## Still open

- **`og:image`** for the unfurl. A static wordmark on the ink background would do it; that means
  hosting one fixed, committed object on the Worker (and nothing else — a Worker that accepts
  arbitrary uploads is a different thing than one that serves one file it shipped with).
- **Importing a share back into Polaris** would make `/s/{token}.json` more than a debugging
  affordance, and would let one Polaris read another. Not v1.

## What happened to #120's open questions

Issue #120 asked four things before implementation. Kept here because this doc is the record and
the issue body was rewritten to match it:

| #120 asked | Answer |
|---|---|
| Live or snapshot? | Snapshot — and not as a compromise: the Worker has no path back to SQLite, so the question is answered by the architecture |
| Can a share be un-listed after it's been posted? | Yes, trivially — revoke is a real row delete. What it can't do is un-post it from wherever it was shared, which the UI says out loud |
| Workspace files referenced in a shared thread? | Published, if they're images: resolved through the existing traversal guard, re-encoded, rewritten to `/s/{token}/{n}` |
| Docker-only / Funnel-dependent infra? | Neither. A Worker route on the grove zone, no `tailscaled`, no Docker, no reverse proxy |
