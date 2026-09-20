# Transponder — v1 plan (living doc, mid-design)

**Name locked in.** A transponder is a device that receives a signal and automatically replies to
it — spacecraft, aircraft, and satellites all carry them. That's push-to-talk mode almost
literally: transmit, and something automatically replies. It sits in the same "night sky, not
tech-neon" register as Pulsar/Atlas/Constellation/Comet without naming a specific celestial
object the way those do, and it doesn't collide with anything already in use. Considered and
rejected: Uplink (operator's second choice — good fit, `Transponder` won on gut reaction), Signal
(collides with the messaging app), Beacon (nice tie to Polaris's own "fixed point" framing, but
weaker mechanical fit than Transponder's literal definition).

**Status: planning only — nothing here has been built yet, and it shouldn't be started until
`docs/plans/voice-playback-infra.md`'s two fixes (reliable audio playback, recency-anchored focus/
voice-mode reinforcement) actually land.** See that doc for why.

**Mockups live in the repo, per this project's usual convention** (see e.g. `mockups/pulsar-daily.html`
referenced from `docs/plans/pulsar-daily.md`):
- `mockups/transponder.html` — the four call-screen states (idle/listening/thinking/speaking) plus
  the chosen read-aloud audio-player direction, all renamed to Transponder.
- `mockups/transponder-audio-player-options.html` — the full A/B/C/D comparison for the
  read-aloud player, kept for the record of what was considered and why D won (same convention as
  `mockups/comet-icon-options.html`).
- `mockups/transponder-audio-reactive-orb.html` — a **working** proof of concept (not a static
  visual comp) that the call screens' orb should be driven by real audio amplitude, not a canned
  CSS loop — see "Resolved" below. Also published live at
  https://claude.ai/artifact/TjaugGpkkrrG2NJnCMwNhx (needs a real HTTPS origin to actually request
  mic access — opening the repo file directly as `file://` won't prompt for the microphone in most
  browsers, since that's not a secure context; the hosted link is the one to actually click "Start
  mic test" on).

A "Telephone Mode — Call Screen Mockups" canvas artifact from the same planning session also
exists with the same content (interactive, pan/zoomable) — kept in sync with the repo files above,
but the repo HTML is the durable copy.

## What this is

A full-screen, push-to-talk call UI for talking to the assistant out loud instead of typing —
"be on the phone with it," per the operator's own original framing (Transponder is the shipped
name; "telephone mode"/"call UI" below just describes the shape of the feature, not what it's
called). Not a new backend surface: it's **another interface onto an ordinary thread**, the same
way Atlas or the normal composer are — same WebSocket connection, same `agent.Run` turn pipeline,
same persisted messages/tool calls/citations. Dismissing Transponder drops the operator straight
into that same thread in the normal `ChatView`, with everything visible exactly as if they'd
typed it — every tool call, every search, every citation. Transponder is a front-end skin, not a
parallel thread type.

This matters architecturally: it means most of the backend plumbing this needs already exists.
`gateway/protocol.go`'s `ClientMessage.VoiceMode` field has existed since before this planning
session started, with a doc comment describing exactly this feature — it was just never set from
any UI. `prompts.yaml`'s `voice_mode_instruction` already tells the model to keep answers to 1-3
spoken-style sentences with no markdown and no inline citation recitation. The gap isn't backend
capability, it's a frontend surface that turns those knobs on and a call-shaped UI wrapped around
the existing turn pipeline.

## V1 scope, locked in

- **Push-to-talk only.** Hold or tap to record (mirrors `VoiceButton.svelte`'s existing
  toggle-vs-hold setting), release, it transcribes, sends automatically (no composer review step
  — that's deliberate; review-before-send is the point of the *existing* voice-memo flow into the
  composer, but Transponder's entire value is not having to look at the screen), waits, speaks
  the reply, done. "Push, talk, let go, it thinks, it comes back to me" — the operator's own
  description.
- **No auto-stop-on-silence (client-side VAD) in v1.** Real v2 candidate, not now.
- **No full-duplex/real-time mode, ever.** Explicitly ruled out by the operator: it would need an
  entirely different STT/TTS provider (OpenRouter's `/audio/transcriptions` and `/audio/speech`
  are batch endpoints, not streaming), a redesigned duplex WebSocket audio protocol, real
  interruption of an in-flight `agent.Run`, and meaningfully higher per-minute cost — not worth it
  for what this tool is for. This is a permanent scope boundary, not a "maybe later."
- **Latency is accepted as-is, deliberately.** Polaris does real research (web search, page
  reads, tool calls) before answering — a reply is not going to come back instantly, and that's
  fine. No special latency engineering beyond what `docs/plans/voice-playback-infra.md` already
  covers (fast time-to-first-audio via chunked PCM streaming, persisted as one stitched WAV once
  the turn finishes — see that doc's resolved "chunked vs. persisted" section).
- **No cost UI in this mode.** The underlying thread still tracks cost normally (same
  `AddCost`/`cost_usd` machinery as any other turn) — Transponder's screen itself just doesn't
  surface it. Per the operator: cost has never been a real concern for this project even for
  pricier features (Daily, Constellation), and voice adds STT+TTS spend on top of normal turn
  cost, but that's an accepted trade, not something to design UI around.
- **Selectable voice**, per `voice-playback-infra.md`'s Bug 3 — depends on that landing, not new
  scope here.

## Resolved

- **The orb reacts to real audio amplitude on both sides, like ChatGPT's voice mode — not a
  canned animation.** The static call-screen mockups (`mockups/transponder.html`) use fixed CSS
  keyframe loops for the Listening/Speaking orb, which is fine for a visual comp but isn't what
  should actually ship. The real mechanism, proven working in
  `mockups/transponder-audio-reactive-orb.html`: a Web Audio `AnalyserNode` reads live frequency
  data every frame (`requestAnimationFrame` + `getByteFrequencyData`) and drives the orb's
  scale/glow and the waveform bars directly.
  - **Input side:** the same `getUserMedia` stream `VoiceButton.svelte` already captures for
    `MediaRecorder` gets a second branch into `AnalyserNode` (via
    `audioCtx.createMediaStreamSource(stream)`) — not connected to the speaker output, so there's
    no echo/feedback risk. The orb reacts to the operator's own voice while recording.
  - **Output side:** once `voice-playback-infra.md`'s Fix 1 (persisted WAV) lands, the same
    `<audio>` element gets a `MediaElementAudioSourceNode` → `AnalyserNode` → destination chain —
    the orb reacts to the actual synthesized speech as it plays, not a decorative loop timed to
    roughly match. The demo file fakes this side with a short synthesized tone sequence (no real
    Kokoro clip to work with in a standalone mockup), but the wiring is identical to what a real
    persisted clip would use.
  - This is purely a visual-fidelity decision, not an architecture change — doesn't touch the
    "no full-duplex" scope boundary above; the orb reacting to audio in each direction is still
    strictly turn-by-turn (record, then play), never simultaneous.
  - **Known limitation of the hosted demo, not of the technique:** the published artifact copy
    (https://claude.ai/artifact/TjaugGpkkrrG2NJnCMwNhx) can't actually request mic access — tested
    live, `getUserMedia` fails with `NotAllowedError` and no permission prompt ever appears, the
    exact signature of an iframe's Permissions Policy blocking the request before the browser gets
    to ask. Claude's artifact sandbox almost certainly doesn't delegate microphone access to
    embedded pages, deliberately. This says nothing about whether the real feature will work:
    `VoiceButton.svelte`'s `getUserMedia` call already succeeds today, in production, over
    Tailscale, outside any artifact sandbox — that's how push-to-talk voice memos already work.
    The `AnalyserNode` addition is a small extension of an already-working capture path, not new
    uncertain territory. Real verification of this specific piece happens naturally once it's
    wired into the actual app (per this codebase's "verify on real hardware" culture) — the
    artifact link was never going to be the right venue to prove it live.
- **Visual indicator copy.** "Polaris is speaking" broke the pattern the other three screens
  already used (plain single words: "Listening", "Thinking") — changed to "Speaking" for
  consistency. Real research behind the direction, not just a guess: ChatGPT's original dedicated
  voice UI (the full-screen blue orb, before its Nov 2025 redesign folded voice into the regular
  chat window) had **no text label at all**, just the orb's color/motion — some users specifically
  prefer that version because it's "less going on visually"
  ([TechCrunch](https://techcrunch.com/2025/11/25/chatgpts-voice-mode-is-no-longer-a-separate-interface/),
  [Coursiv](https://coursiv.io/blog/chatgpt-voice-mode)). Kagi's voice input turned out not to be a
  comparable full-screen call UI at all — just a mic button for dictation into the normal Assistant
  chat box ([Kagi docs](https://help.kagi.com/kagi/ai/assistant.html)), so there was no
  state-indicator convention to borrow from there. Dropping the label entirely (matching ChatGPT's
  original approach, and `PRODUCT.md`'s "calm over clever") is still worth considering later; the
  single-word fix is the safe, consistent v1 answer.

- **Yes, it needs one small persisted marker after all: whether a thread was ever used in a call,
  plus a "Transponder calls" count in Settings.** This settles the "own `store.Thread` marker"
  question below in favor of "yes, one boolean" — the operator wants to see usage, which needs
  something to query. Fits existing conventions closely enough that it's basically wiring, not new
  design:
  - **Per-thread flag:** a `threads.used_transponder` boolean (mirrors how `store.Thread.Source`
    is already a plain informational column — see `store/store.go`'s schema comment on it), set
    true the first time a turn on that thread carries `voice_mode: true`. Updated where
    `gateway/turn.go` already reads `msg.VoiceMode` (line ~356/528) — an `UPDATE threads SET
    used_transponder = 1 WHERE id = ? AND used_transponder = 0` alongside the existing turn
    bookkeeping there, not a new code path.
  - **Deliberately NOT thread origin.** This is not the same thing as `Source = "transponder"`
    would be — `Source` is fixed at thread creation and never revisited (`protocol.go`'s doc
    comment: "only read on thread creation; ignored on every later turn"), but a thread can move
    freely between chat and call view per "What this is" above. A thread started as plain text
    that later gets one call turn should count; the flag has to be able to flip on any turn, not
    just the first one — a plain column updated in `handleTurn`, not `Source`.
  - **What "one call" counts as, for the aggregate number:** simplest definition, avoiding a new
    "call session" concept the architecture otherwise has no use for — **the count is distinct
    threads with `used_transponder = true`**, not raw voice turns and not explicit call-start/end
    events. `store.Stats` (`store/stats.go`) gets one more field (e.g. `TransponderCallCount`),
    computed the same way `ThreadCount`/the existing `source`-grouped queries already are —
    `SELECT COUNT(*) FROM threads WHERE used_transponder = 1 AND disabled = 0`. This is an
    assumption, not confirmed with the operator: if "number of them" was meant as total call
    *turns* (every push-to-talk exchange) rather than distinct threads-that-had-a-call, that's a
    different, still-simple query (`COUNT` against whatever logs `voice_mode: true` per-turn) —
    worth a quick confirm before building, not before planning.
  - **Settings UI:** `SettingsPanel.svelte`'s existing "Activity" usage section already has this
    exact row shape — `usage-stat-row` label/value pairs like "Threads / turns" and "Tool calls"
    (lines ~157-176), with conditional rendering only when the count is nonzero (same pattern as
    the `code_exec_wall_time_ms > 0` row just below them). "Transponder calls" slots in there
    identically, no new UI pattern needed.

## Not-yet-decided

- **Where the call screen lives in navigation** — a dedicated route, entry point from the
  composer, from the sidebar — not yet settled.

## Why this is deliberately *not* more decided yet

Per the operator: two infrastructure passes (`docs/plans/voice-playback-infra.md`) need to land
and be validated first — reliable audio autoplay and recency-anchored mode reinforcement — before
it's worth locking down Transponder's own implementation details further. Building this UI on top
of a read-aloud mechanism that's never actually worked, or a voice-mode instruction that degrades
into long spoken answers by turn 4, would mean redoing this plan's implementation section anyway
once those fixes exist. Mockups and architecture-level scoping (this doc) are fine to keep
developing in parallel; wiring anything up is not.
