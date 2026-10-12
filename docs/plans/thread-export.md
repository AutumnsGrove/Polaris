# Thread export

**Added: 2026-10-12.**

**Status:** planning only. Decisions below were settled in a design conversation on 2026-10-12; the
"Open items" section lists what is still undecided. No code yet.

## Goal

Take one finished or in-progress thread out of Polaris as a file (or clipboard text) that can be
loaded into another tool. The motivating case is a ~50,000-token thread moved into Claude. A second,
longer-term case is **auditing**: someone downloads a thread and checks how an answer was reached,
which needs the thinking and tool trail, not only the final text.

One versatile function behind an options modal, not a single fixed format.

This is separate from issue #120 (public read-only share links). Export produces a private file for
the operator; sharing exposes a thread to the internet and needs its own threat-model work. They
should share the intermediate document built here, but nothing in this plan opens a public route.

## Scope

In scope: one thread at a time, normal chat threads (`threads.source == "web"`).

Out of scope for now: Atlas threads, Weaver threads, Pulsar threads, bulk "export everything",
re-importing an export back into Polaris, bundling attachment bytes.

## The options modal

Opened from a new **Export** item in the thread menu (next to Favorite / Promote / Delete).

| Group | Option | Default |
|---|---|---|
| Format | Markdown / JSON | Markdown |
| Scope | Full conversation / Handoff (stored summary + messages after it) | Full |
| Include | Sources and citations | on |
| Include | Research trail (searches run, pages read, tool calls and results) | off |
| Include | Thinking traces | off |
| Include | Attachments (filename and type only, v1) | off |
| Include | Cost and model metadata | off |
| Privacy | Leave out memories and custom instructions | on |
| Delivery | Download / Copy | Download |

Notes on the modal:

- Research trail and thinking traces are **independent** toggles. An audit export turns both on; a
  paste-into-Claude export leaves both off.
- **Handoff is disabled (with a one-line reason) when the thread has no stored compaction summary.**
  It reuses `threads.compacted_summary` verbatim and appends the messages with
  `id > compacted_through_id`. It never makes a model call, so export has no cost and no spend
  path. If a summary doesn't exist, the option simply isn't offered; it is not generated on demand.
- The modal shows a rough token estimate for the chosen output (characters / 4) so it's clear
  whether it will fit a context window.
- The modal shows **availability notes** when an option can't be fully honoured, e.g. "Thinking
  traces: available for 14 of 40 turns (older ones were pruned)". See "Retention".
- The privacy toggle is labelled exactly what it does. It is not "redact personal information";
  see "Leave out memories and instructions" for what it does and does not guarantee.

## Where each piece of data comes from

| Export content | Source | Lifetime |
|---|---|---|
| Messages, order, roles, timestamps | `messages` (follow the effective variant path, see Open items) | permanent |
| Citations / sources | `messages.citations` | permanent |
| Cards, chart, pending question, verification marks, Oracle result | `messages.cards/chart/pending_question/verification/oracle_result` | permanent |
| Attachment filename and type | `messages.attachments` (fall back to the legacy single-attachment columns, as `GetMessages` does) | permanent (the files themselves are not exported in v1) |
| Tool calls and results | `messages.transcript` (untruncated), falling back to `events` (`tool_call` / `tool_result`) | transcript permanent, events pruned after 1 year |
| Thinking traces | `events` rows with `source == "turn"`, `message == "reasoning"`, `data.content`, `data.agent_id` | **pruned with events (1 year)** |
| Compaction summary | `threads.compacted_summary`, `compacted_through_id` | permanent |
| Cost, model, duration | `messages.cost_usd`, `duration_ms`, `threads.model` | permanent |

Two facts that shape the design (verified against the code on 2026-10-12):

1. **Thinking is only in `events`.** `EncodeTranscript` stores role, content, tool calls, tool call
   ids and image URLs. It has no reasoning field. Reasoning bursts are written as `events` rows,
   per `agent_id` (the main agent is `""`; Deep Research sub-agents have their own ids), and joined
   to a turn by `turn_id`.
2. **Custom instructions and the memory index are not persisted per message.** They are injected
   into the system prompt each turn (`{custom_instructions}`, `{memories}`), and `transcript`
   starts at the user message. So export never has to scrub them out of stored messages; it only
   has to make sure it never reads or emits the system prompt, and never emits raw `transcript`
   wholesale.

### Retention

Events used to be pruned after 90 days (mirroring the log files). **Changed 2026-10-12:**
`cmd/run.go`'s `eventRetentionDays` is now 365, because thinking traces survive only in `events` and
an audit export that loses them after three months defeats its purpose. Event rows are still capped
at `maxEventDataBytes` (20 KB), so growth is bounded. Threads and messages are never pruned (see
`store/events.go`'s `PruneEvents` doc).

Even at a year, older turns and any turn whose events were already pruned under the 90-day rule
(before this change shipped) have no thinking available. The export must say so (availability
notes in the modal, `omitted` manifest in the file) instead of silently producing a thread with
gaps. Worth checking real table size on the potato after a few months (`SELECT count(*),
sum(length(data)) FROM events`).

## Output formats

### Intermediate document

Build one neutral struct, then render it. This is the "versatile function": options are applied
once, when building the struct, and the renderers are dumb.

```
Doc {
  export:   { version, exported_at, app_version, options }
  thread:   { id, title, created_at, updated_at, model, source }
  summary:  { text, through_message_id }            // handoff scope only
  turns:    []Turn
  omitted:  []OmissionNote                          // what was left out and why
}
Turn {
  turn_id
  user:      { id, created_at, content, attachments[] }
  assistant: { id, created_at, content, citations[], cards, chart, verification, ... }
  trace:     []TraceStep   // research trail, in order; present only if requested
  thinking:  []Thinking    // present only if requested
  meta:      { cost_usd, duration_ms, model }       // present only if requested
}
```

`omitted` is what makes the JSON auditable. Each note records a category and a reason, for example
`{"what":"thinking","turns":["t12","t13"],"reason":"events pruned"}` or
`{"what":"tool_calls","tools":["write_memory"],"reason":"privacy: memories left out"}`. A reader can
tell "nothing happened here" from "it was removed".

### Markdown

For humans and for pasting into another model.

- Header: title, date range, model (model only if metadata is on).
- Handoff scope: a "Summary of earlier conversation" section first, then the later messages.
- `## You` / `## Assistant` sections per turn, in order.
- Inline `[n]` citation markers are kept, and a numbered **Sources** list follows each answer.
- Research trail and thinking, when requested, appear as clearly fenced, collapsible-looking blocks
  (`> **Research trail**` / `> **Thinking**`) *before* the answer they belong to, so a model reading
  it can distinguish reasoning from output.
- Attachments appear as `📎 filename (type)`.
- Rich blocks (cards, charts, Prism `ui` blocks) are flattened to text with the existing Go
  flattener in `uiblocks`; verify its exported surface before relying on it.

### JSON

For tooling and audits. Versioned (`"polaris_export": 1`), stable field names, one object per turn
as above, ids and timestamps preserved so entries can be correlated with `/api/threads/{id}/events`.
It never includes the raw `transcript` blob; trace steps are normalized from it.

## Leave out memories and instructions

When on (the default), the builder:

- Drops tool calls and tool results for the memory tools (list/get/write/edit/forget memory) from the
  research trail, and removes their traces from any turn's thinking-adjacent content. The list of
  tool names should come from the tool registry or a flag on the tool definition rather than a
  string list duplicated in the exporter, so a new memory-adjacent tool can't be forgotten.
- Never reads the system prompt, `{custom_instructions}`, field instructions, or the memory index.
  A regression test asserts the rendered output of a fixture thread contains none of the fixture's
  memory/instruction marker strings.
- Records the omission in `omitted`.

What it does **not** guarantee, and the modal should say in one line:

- The model's own answer text, or a thinking trace, may still quote or paraphrase a memory.
- `compacted_summary` was written by the model with whatever context it had, so a handoff summary
  can carry personal context.
- Text the user typed is exported as typed. Nothing is scrubbed for names, emails, or addresses.
- Other personalization tools (chat search, Constellation star reads, save-to-field) are not
  covered by this toggle unless we decide to extend it; see Open items.

No automatic PII redaction. It can't be done reliably, and a button implying otherwise would be
worse than not having one.

## API

`GET /api/threads/{id}/export` with query parameters:

`format=md|json`, `scope=full|handoff`, `sources=1`, `trail=1`, `thinking=1`, `attachments=1`,
`meta=1`, `private=1`.

- `Content-Disposition: attachment` with a slugified title and date as the filename; the frontend's
  Copy path requests the same endpoint and puts the text on the clipboard instead.
- A single GET is enough because nothing here spends money (handoff reuses a stored summary).
- Response includes the same availability information the modal shows, so the modal can request a
  dry-run (`?dry_run=1`) for the token estimate and availability notes without building the full
  file. Cheaper alternative if that's overkill: compute the estimate client-side from the thread
  already in memory, and have only availability come from the server.
- CLI: `polaris export <thread-id> [--format] [--scope] ...`, a thin client over this endpoint per
  `CLAUDE.md`'s CLI convention, reusing the endpoint's response types.

## Code layout

- New package `threadexport/`: `Options`, `Doc`, `Build(data, opts)`, `RenderMarkdown`,
  `RenderJSON`, token estimate. Pure functions, no database access, so it's trivially testable and
  reusable by #120 later.
- `store/`: one read helper that gathers a thread's messages, effective path, events for the
  thread, and compaction fields in a single consistent pass. Reuse existing `GetMessages` /
  `ListEvents` rather than re-querying by hand.
- `gateway/thread_export.go`: handler + route registration in `gateway/server.go`.
- Frontend: `ExportModal.svelte`, a thread-menu item, and a small helper for download/copy. Use the
  `app.css` tokens (`--space-*`, `--radius-*`, `--z-*`) per `CLAUDE.md`.
- `cmd/export.go`: the CLI client.
- Docs: a line in `docs/FEATURES.md`. No `HelpModal` `TERMS` entry (no new themed name).
  `SETUP.md` / `DEVELOPMENT.md` only if retention changes or the CLI is notable.
- Nothing here reads a file relative to CWD, so the Dockerfile / compose bind-mount rule doesn't
  apply.

## Phases

1. **Core builder.** `threadexport` with Markdown and JSON, full scope, sources, metadata,
   attachments (names only), and the `omitted` manifest. Golden-file tests.
2. **Trail and thinking.** Normalize tool steps from `transcript` with `events` fallback; thinking
   from `events` with per-`agent_id` grouping; availability reporting. Privacy toggle and its
   regression test.
3. **Endpoint, modal, delivery.** The route, `ExportModal.svelte`, download and copy, token
   estimate, handoff scope.
4. **CLI and docs.** `polaris export`, FEATURES.md, then optionally the retention change.

## Verification

Following the repo's live-verification culture, not just unit tests:

- Unit/golden tests for both renderers over a fixture thread covering: citations, attachments,
  a card, a chart, a compacted thread, a forked/variant thread, a Deep Research turn with
  sub-agent thinking, and a turn whose events have been pruned.
- Regression tests that fail without the fix: a memory tool call must not appear when `private=1`,
  and must appear when it's off (reverting the filter should make the first fail).
- Round-trip the JSON through a schema check and compare turn/message counts to `GET /api/threads/{id}`.
- Live: run the dev stack with `dev/fakeopenrouter`, drive the modal with Playwright at 390px, and
  confirm the download and the clipboard path on a phone-sized viewport.
- Real data: export the actual ~50k-token thread on the potato (`ssh potato-remote`), upload it to
  Claude, and confirm it's usable and that the token estimate was in the right ballpark.

## Open items

- **Variants / forks.** Export the currently selected variant path only (as the chat view shows),
  not every variant. Confirm which store function defines that path (`EffectiveHistory` vs. the
  variants code) and test a thread with a swapped variant.
- **Other personalization tools.** Should the privacy toggle also drop chat-search, Constellation
  star reads, and save-to-field calls? Current plan: memories only, as decided.
- **Attachments.** v1 exports names and types. Embedding file contents (inline text, or a `.zip`
  with the files alongside) is a follow-up; text attachments are the likely first case.
- **Dry-run endpoint vs. client-side estimate** for the modal's size and availability notes.
- **Mid-turn threads.** If a turn is streaming when the user exports, include only completed turns
  and note it.
- **Ghost threads** (issue #67's no-memory threads): no special handling expected, but confirm they
  export like normal threads.
