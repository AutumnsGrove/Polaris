# Voice playback infra — fixing read-aloud and reinforcing focus modes (living doc, mid-design)

**Status: planning only — nothing here has been built yet. This is prerequisite work for
`docs/plans/telephone-mode.md`; that plan explicitly depends on both fixes below landing first.**
See `docs/plans/telephone-mode.md` for the feature this unblocks, and the "Telephone Mode —
Call Screen Mockups" canvas artifact from the same planning session for UI mockups (call screens
plus the audio-player options this doc covers).

## Why this exists

Two infrastructure gaps surfaced while planning telephone mode, both blocking it outright rather
than being nice-to-haves:

1. **Read-aloud (`ChatTurnView`'s speaker icon) doesn't actually play audio in practice.** It
   bills real Kokoro TTS spend (visible in OpenRouter's logs) but the operator has never
   successfully heard it play, going back to shortly after it first shipped.
2. **Focus-mode instructions (Brief, and the not-yet-wired VoiceMode) drift.** A conversation
   started in Brief mode answers briefly for the first exchange or two, then drifts back to long
   answers within ~4 turns — even though the instruction is technically still present every turn.

Telephone mode's core premise ("push, talk, let go, it thinks, it comes back to you, out loud")
needs both of these solved first: reliable audio autoplay with no fresh tap required, and answers
that stay short for the whole call, not just the first exchange.

## Bug 1: read-aloud playback silently fails

**Root cause: a browser autoplay-policy rejection, not a model or synthesis problem.** The call
chain is `ChatTurnView`'s speaker icon → `appState.readAloud()` (`web/src/lib/state.svelte.ts`)
→ `AudioPlayer.readAloud()` → `synthesizeStream()` (`web/src/lib/speech.ts`), which does
`await fetch('/api/speak/stream', ...)` — real network time — before the first `audio.play()`
call ever happens (that only fires later, inside the NDJSON chunk callback via
`AudioPlayer.enqueue()`/`playNext()` in `web/src/lib/audio.svelte.ts`).

By the time that first `play()` fires, the browser's "this was triggered directly by a user
gesture" window has expired — especially strict on iOS Safari in `display: standalone` PWA mode
(`web/static/manifest.json`), which is the primary deployment target per `PRODUCT.md`. `play()`
rejects with `NotAllowedError`. `playNext()`'s catch block (`audio.svelte.ts` around line 92)
just does `console.error(...)` and silently advances to the next queued chunk — so synthesis
succeeds and gets billed (`s.db.AddCost` in `gateway/voice_handlers.go`), while playback never
audibly happens and nothing surfaces the failure to the UI.

This isn't cosmetic for telephone mode: walkie-talkie mode's whole premise is the reply
auto-playing with *no new tap* after you release push-to-talk — the exact same async-gap
problem, guaranteed to trigger on every single turn instead of occasionally on a read-aloud
click deep in a scrolled transcript.

### Fix, two parts

**1. Unlock media playback synchronously inside a real user gesture, once per page session.**
Browsers (Chrome's autoplay policy and Safari's equivalent) grant continued playback permission
for the rest of a tab's lifetime once a media element has successfully played following a direct
user gesture — the standard trick is to play+immediately-pause a silent/tiny audio element
*synchronously inside the click handler itself*, before any `await`. Do this once (the first
read-aloud tap, or telephone mode's "start call" tap) and later async-triggered `play()` calls
stop being blocked for that session.

**2. Persist the synthesized audio as a real file instead of streaming ephemeral blob URLs
through a JS class.** Confirmed with the operator: **code_exec's workspace storage is always
available** in the only supported deployment model (Docker — see `CLAUDE.md`'s "Install and
production are Docker-only" section). `compose/polaris/config.yaml.example` sets
`code_exec.workspace_dir`/`host_workspace_dir` by default, and `docker-compose.yml` wires the
bind mount unconditionally — there's no scenario in production where it's absent. (Bare-metal
*local dev* still needs the extra opt-in `DEVELOPMENT.md` describes under "Local dev
code_exec" — same as it already does for every other code_exec-dependent feature; this doesn't
add a new constraint, just reuses the existing one.) So: reuse that storage wholesale rather than
inventing a second one.

Concretely:
- Synthesize server-side the same way `handleSpeak`/`handleSpeakStream` already do
  (`gateway/voice_handlers.go`), but write the resulting audio bytes into the thread's existing
  code_exec workspace directory (`cfg.CodeExec.WorkspaceDir/<thread_id>/<generated-id>.mp3`)
  instead of returning them directly in the HTTP response.
- Serve it back with the *same* route `tools/show.go`'s embeds already use —
  `gateway/workspace.go`'s `handleGetWorkspaceFile` (`GET /api/workspace/:thread_id/:filename`) —
  no new serving code needed, just a new writer into an existing, already-served directory.
- Persist a reference on the message so a reload shows the same player instead of losing it —
  mirror the existing `store.Message.WorkspaceFileID` pattern (see `store/store.go`) rather than
  inventing a parallel field, unless the two need to coexist on one message (a user upload *and*
  a read-aloud on the same assistant turn), in which case this probably wants its own column.
- Frontend: replace `AudioPlayer`'s ephemeral blob-URL queue with a real `<audio controls>`
  element appended to the message once the file exists — see the audio-player mockup options
  below for the visual options under consideration.

**Resolved: stream for time-to-first-audio, persist one stitched file for everything after.**
Worth being explicit about a misconception this question raised: chunked delivery is a *latency*
optimization, not a *naturalness* one — each chunk is still a complete, ordinary Kokoro synthesis
of one sentence, so a persisted single file built from the same chunks sounds identical, not more
robotic. If anything, today's chunk-by-chunk playback (separate `Audio` elements handed off via
`onended`→`playNext` in `audio.svelte.ts`) risks small audible gaps at sentence boundaries that a
properly stitched single file wouldn't have. So this was never really a naturalness trade-off —
it only ever traded time-to-first-audio against having one clean, reliably scrubbable file, and
there's no reason to have to pick one:

- Keep requesting chunks from `handleSpeakStream` as today (fast start, chunk *n+1* synthesizing
  while chunk *n* plays), but request Kokoro's **`pcm` response format** (already a supported
  `TTSClient` format, see `config/config.go`) instead of `mp3` for this path. Raw PCM samples
  concatenate byte-exact with no frame-boundary artifacts — unlike MP3, which has per-frame
  headers that make naive concatenation of independently-encoded chunks a real (if usually minor)
  risk of audible clicks at the seams.
- As each chunk arrives, append its raw PCM bytes to the thread's workspace file server-side
  *and* still forward it to the client for immediate low-latency playback, exactly as now.
- Once the turn's synthesis is done, wrap the fully-concatenated PCM in a single WAV header (trivial
  — WAV is just a 44-byte header over raw PCM) and that's the persisted, seekable file this doc's
  Fix 2 describes. WAV also scrubs natively and reliably in every browser's `<audio>` element,
  mobile Safari included, which matters for the scrubbing requirement below.
- On reload (or a second visit to a thread), the player just points at that one finished WAV —
  no chunk-boundary seams, real scrubbing across the whole clip, same file whether it's being
  heard for the first time mid-stream or replayed later.

This resolves the operator's "streaming heart vs. persisted-file intuition" tension directly:
streaming is still what makes the first playback start quickly; the persisted file is what makes
replay, reload, and scrubbing reliable — they were never actually in tension once chunking stops
meaning "several separate audio files" and starts meaning "the same one file, written
incrementally."

**Chosen UI direction: a waveform-as-scrubber, combining mockup Options B and C** (see the canvas
artifact) — Option C's bordered card (matches `ChartCard`/tool-result convention) and real
drag-to-seek, with Option B's static waveform bars replacing C's plain progress line: bars behind
the playhead render solid/accent, bars ahead render dimmed, and the whole waveform is the
scrub track (drag anywhere on it to seek), not just a thin line. Per the operator, this needs to
work reliably as a drag gesture on mobile, not just desktop — the scrub handle gets a generous
touch target (≥44px hit area per `PRODUCT.md`'s accessibility note, even though the visible
handle stays small). Playback-speed control (1x/1.5x/2x, in the original Option C) is dropped —
not wanted.

## Bug 2: focus/voice-mode instructions drift over a conversation

**Root cause: recency decay, not absence.** `agent.Run` (`agent/driver.go` around line 460-468)
*does* rebuild the system prompt — including the focus-mode instruction
(`p.Agent.FocusModes[focusMode]`, `driver.go:244`) or the not-yet-wired
`VoiceModeInstruction` (`prompts.yaml`'s `voice_mode_instruction`) — fresh on every single turn.
It's not a one-time injection. The problem is positional: that instruction sits at
`messages[0]`, then the thread's full prior history gets appended after it
(`driver.go:467`), then the new user message last (`driver.go:468`). As history grows, the
model's attention drifts toward the bulk of recent conversation and away from an instruction
buried at the very top of a long context — even though it's technically resent every call.

The operator's own diagnosis matches how Claude's own products handle this: re-anchor the
reminder near the **end** of the per-turn message list (right at/after the latest user message),
not only at position 0. Recency, not mere presence, is what keeps a model reliably honest about a
standing constraint over a long conversation.

### Fix direction (not yet implemented)

When `ctx.FocusMode` or `ctx.VoiceMode` is set, inject a short, low-noise reinforcement message
positioned after `history` and at/after the final user message in the `messages` slice built in
`agent.Run` — the exact insertion point is `agent/driver.go`'s message-assembly block
(currently lines ~465-468). Two shapes to weigh when this gets built:

- A separate synthetic message (system or user-adjacent role) appended right before the final
  LLM call, **not persisted** to `store.Store` — it should exist only in the `messages` slice
  built for that one API call, so the operator's actual sent message stays clean in the
  transcript and on reload.
- Folded directly onto the tail of the outgoing `userMessage` string for that one LLM call only
  (again, not what gets persisted) — simpler structurally, no new message role, but conflates
  "what the user said" and "what we're reminding the model of" in one string if not built
  carefully.

This is a general fix, not voice-mode-specific — it should also fix Brief focus mode's drift,
since both go through the same `loadSystemPrompt`/message-assembly path. Build and validate it
against Brief mode (existing, easy to test blind) before leaning on it for voice mode, where
short answers are the entire point of the feature working at all.

## Bug 3 (smaller): no way to change the TTS voice

Not a bug exactly, but bundled into this pass since it's cheap and adjacent. `config.Voice.TTSVoice`
(`config/config.go`) is a single static config-value (`"bf_lily"` by default) — there's no
per-session picker the way `SettingsPanel.svelte` already has one for the LLM model. Kokoro-82M's
OpenRouter endpoint supports a small roster beyond `bf_lily` (other `af_`/`am_`/`bf_`/`bm_`
prefixed voices — American/British, female/male). Surfacing a curated handful of those as a
Settings dropdown is the same UI pattern as the existing model picker and needs no new provider
integration — it's the same `voice.TTSClient`, just a different `voice` field in the request
payload `TTSClient.Speak` already sends. Multi-provider TTS (ElevenLabs, etc.) is explicitly out
of scope for this pass — a possible future idea, not a blocker for anything here.

## Non-goals for this pass

- No telephone-mode UI or backend work — that's `docs/plans/telephone-mode.md`, and depends on
  this doc's fixes landing first, not the other way around.
- No new TTS/STT provider integration.
- No changes to `handleTranscribe`/STT — the bug is entirely on the TTS playback side.

## Sequencing

1. Fix 1 (persisted audio + gesture unlock) — read-aloud in normal chat becomes reliable; this is
   the same mechanism telephone mode needs for auto-play.
2. Fix 2 (recency-anchored reinforcement) — validate against Brief focus mode first, since it's
   easy to test without any voice infra involved at all.
3. Bug 3 (voice picker) — small, can land alongside either of the above.
4. Only then: `docs/plans/telephone-mode.md`.
