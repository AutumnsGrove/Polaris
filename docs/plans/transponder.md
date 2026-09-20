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
voice-mode reinforcement) actually land.** See that doc for why. See the "Telephone Mode — Call
Screen Mockups" canvas artifact from the same planning session for the current call-screen visual
direction (idle/listening/thinking/speaking states) — still 80% there per the operator, pending a
revised indicator design (the mockup's canvas title and in-UI copy still say "Telephone Mode" /
"Polaris is speaking" and haven't been updated to the new name yet — that's a follow-up, not done
as part of this rename).

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

## Not-yet-decided

- **Visual indicator redesign.** Current mockup's top-of-screen "Polaris is speaking" text label
  was called out as not quite right, and the mockup canvas/artboards still need a pass to actually
  say "Transponder" now that the name is locked — does the UI put the name front and center, or
  lean entirely on the orb's motion/color to convey state without words (more in the spirit of
  `PRODUCT.md`'s "calm over clever")? Not decided yet.
- **Where the call screen lives in navigation** — a dedicated route, entry point from the
  composer, from the sidebar — not yet settled.
- **Whether this needs its own `store.Thread` marker at all.** Current thinking is no (see "What
  this is" above — it's a view, not a thread kind), but worth re-confirming once the actual
  routing/state design is drafted, in case something about reopening a call-originated thread
  later in normal chat view turns out to need a hint the thread came from a call.

## Why this is deliberately *not* more decided yet

Per the operator: two infrastructure passes (`docs/plans/voice-playback-infra.md`) need to land
and be validated first — reliable audio autoplay and recency-anchored mode reinforcement — before
it's worth locking down Transponder's own implementation details further. Building this UI on top
of a read-aloud mechanism that's never actually worked, or a voice-mode instruction that degrades
into long spoken answers by turn 4, would mean redoing this plan's implementation section anyway
once those fixes exist. Mockups and architecture-level scoping (this doc) are fine to keep
developing in parallel; wiring anything up is not.
