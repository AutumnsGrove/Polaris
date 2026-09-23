# Verbatim turn transcripts (replacing reconstructed history)

**Status:** implemented 2026-09-23 (all three phases). Decisions below are settled (see "Decisions").

## Phases

1. **Measure cache hits** (issue #107): per-message `prompt_tokens`/`cache_read_tokens`, summed per
   thread on read, and shown as a hit % in the thread menu next to thread cost.
2. **Stable system prompt**: date-only preamble; the time of day moves into a new `current_time`
   tool the model calls when it actually needs it.
3. **Verbatim transcripts**: persist each turn's exact wire messages, replay them as-is, remove the
   `full_turn_history` setting, default `context_window_tokens` to 200K.

## The problem

Every follow-up turn rebuilds the model's view of the earlier conversation from scratch, out of
pieces stored in different places. The result never matches, byte for byte, what the model was
actually sent while those turns ran. Two things go wrong because of that:

1. **The model loses track of its own earlier turns.** In default mode, a follow-up sees each earlier
   answer plus an app-written `[Polaris note ...]` listing source titles and URLs, but none of the
   pages it actually read. That is how you get "I can see I talked about a source, but I can't see
   it."
2. **Prompt caching across turns barely works.** OpenRouter's providers (DeepSeek, OpenAI, Xiaomi,
   Inception) cache on an exact prefix match. The prefix of turn N+1 differs from turn N's last
   request very early on, so almost nothing from earlier turns is served from cache.

## What is sent today

Request for turn N+1 (`agent.Run`, `agent/driver.go:491`):

```
[system]  currentContextPreamble()  ← date + time to the minute, at byte 0
          + prompt.md with {tools} {memories} {person} {custom_instructions} ...
          + focus/voice/deep-research/no-research text
[history] loadHistory or loadHistoryWithToolResults (gateway/turn.go:1530, history_replay.go)
[user]    turnMessage (msg.Content + attachment notes / Pulsar previous report)
[user]    modeReinforcement (focus/voice text again, only when history is non-empty)
... then this turn's tool loop appends assistant tool_calls / tool results / nudges / images
```

### Cache breakers, from most to least severe

| # | What | Where | Effect |
|---|------|-------|--------|
| 1 | The wall-clock time (to the minute) is the first thing in the system prompt | `currentContextPreamble`, `agent/driver.go:396` | **Almost every turn misses the cache completely.** Any two turns more than a minute apart differ at around byte 30, so nothing after that point can match, whatever the history mode. On its own this makes the history-mode question irrelevant to caching. |
| 2 | Earlier turns are rebuilt instead of replayed | `store.EffectiveHistory`, `loadHistoryWithToolResults` | Turn N's history entry is not what turn N sent (see the next table). The prefix match ends at `user_N` at best. |
| 3 | `modeReinforcement` is appended as an extra user message but never stored | `agent/driver.go:502` | The next turn's rebuilt history doesn't include it, so the prefix diverges right after `user_N`. |
| 4 | `turnMessage` augmentations are sent but never stored (attachment pointer notes, the Pulsar previous report) | `gateway/turn.go` ~L340–470 | The stored user message ≠ the sent user message, so the prefix diverges *at* `user_N`. The model also loses the attachment note on every later turn. |
| 5 | `{memories}` index in the system prompt | `applyMemoriesPlaceholder` | Full miss on the turn after any memory write. Accepted (see Decisions). |
| 6 | Mode toggles change the system prompt and/or the tool list | `loadSystemPrompt`, `tools.Defs` | Full miss whenever focus, voice, deep research or no-research is toggled. Acceptable because it's user-initiated. |

### How rebuilt history differs from what was sent

| Sent during turn N | Default mode | `full_turn_history` mode |
|---|---|---|
| Tool calls + results | dropped | rebuilt from the `events` log |
| Parallel calls in one assistant message | — | split into one message per call |
| Provider-issued `tool_call` IDs | — | replaced with `replay_i_n` |
| Commentary (text the model wrote before a tool call) | dropped | dropped |
| Nudges, "wrap up now", pseudo-tool-call results, `view_image` image messages | dropped | dropped |
| Tool results over 20 KB | — | cut off (`maxEventDataBytes`, `store/events.go:46`) |
| `spawn_researchers` sub-agents' own tool calls | — | **replayed as if the parent made them**: sub-agents share the parent's `Emit` (`agent/subagent.go:80`), so their events land under the parent's turn |
| The final answer | the answer **plus an appended `[Polaris note ...]`** | same, and the note is exactly what the model then started imitating (`StripFakeSourcesNote`, commit b2e60f0) |
| ask_user_question options | appended as "Options offered:" text | same |
| Events older than 90 days | — | gone; falls back to answer-only |

Most of the extra machinery exists to patch over what the rebuild loses. That includes
`appendCitedSources`, `polarisNoteMarker`, `StripFakeSourcesNote`, `appendPendingQuestionOptions`,
prompt.md's "Earlier turns" section, `groupToolEventsByTurn`, and the `full_turn_history`
setting, its `/api/ask` override and `effectiveContextWindowTokens`.

## Proposal: store what was sent, replay it as-is

This is what every chat product does: the conversation is an append-only list of messages, and each
request is that list plus the new message.

### 1. `agent.Run` returns its transcript

Add `Result.Transcript []llm.ChatMessage`: every message `Run` appended after `history`, plus the
final assistant message. That covers the user message, tool-call assistant messages (original IDs,
original batching), tool results (untruncated), nudges, image messages, the wrap-up prompt and the
final answer. Pre-tool-call commentary is streamed to the UI but was never part of the in-turn
request, so it's (correctly) not in the transcript either: the transcript is what was sent. It is simply `messages[len(history)+1:]` plus
`{Role: "assistant", Content: answer}`, collected at each return point. For `ask_user_question`
and the finalize tools, the final assistant message holds the text shown to the user, which gives
the valid shape `assistant(tool_calls) → tool → assistant(text) → user(next reply)`.

### 2. Persist it on the assistant message row

New column `messages.transcript TEXT NOT NULL DEFAULT ''`: a JSON array of `llm.ChatMessage` for the
whole turn, including the model-facing user message. Keeping it on the assistant row means:

- `ForkThread` carries it into edit/retry forks by adding one column to its `INSERT ... SELECT`.
- Compaction's `compacted_through_id` still works unchanged.
- The `events` table goes back to being only a UI/debug trail, with its 20 KB cap and 90-day pruning.

Go's `encoding/json` is deterministic for a fixed struct, so a stored transcript marshals back to
the same bytes every time.

### 3. `loadHistory` becomes concatenation

For each turn after the compaction point: if the assistant row has a `transcript`, append it
verbatim; otherwise (threads from before this change) fall back to today's rebuild. The fallback
costs one cache miss per old thread, and stays until those threads compact or age out.

For turn N+1 the request is then `[system, T1 … TN, user_{N+1}]`. Turn N's last request was
`[system, T1 … T(N-1), user_N … last tool result]`, so **everything up to turn N's final answer is
a cache hit**. Only the final answer and the new message are uncached. That's the ideal.

### 4. Make the system prompt stable across turns

- **Drop the time of day from the preamble, keep the date.** The system prompt then changes at
  most once a day. A new `current_time` tool returns the exact local time (and timezone) for the
  rare question that needs it, e.g. "is X open right now" or "how long until...".
- **Mode instructions** (focus/voice/deep/no-research): keep them in the system prompt so they
  still apply from the first turn. `modeReinforcement` stays a separate message right after the user
  message, and it is now saved as part of the turn's transcript like everything else. It's still
  near the end, where it has the most effect, and it no longer breaks the prefix on the next turn.

### 5. Delete the patches

- The `full_turn_history` setting: `settings.go`, the `SettingsPanel.svelte` toggle,
  `settings.svelte.ts`, `ClientMessage.FullTurnHistoryOverride`, `AskRequest.FullTurnHistory`.
- `effectiveContextWindowTokens`. `config.ContextWindowTokens` defaults to **200 000** (config.go
  plus both `config.yaml.example` files). The smallest registry window is Mercury 2.5 at 260K.
- `gateway/history_replay.go` shrinks to the legacy-turn fallback only: always on for turns without
  a transcript, no longer behind a setting. `appendCitedSources` and `StripFakeSourcesNote` stay
  while legacy turns can still show up in history, and apply only to those turns. New turns never
  carry the note, so there's nothing new for the model to imitate.
- prompt.md's "Earlier turns" section shrinks to a line or two.

### 6. Compaction (follow-up, not done)

`compactThread` and `regenerateTitle` still use an answers-only view (`loadAnswerHistory`) under
their own system prompt, same as before. A cheaper compaction would send the exact turn prefix
(same system prompt, same tools) plus a final instruction, so nearly all of it is cache reads and
the summary can draw on the actual sources. Left for later: it needs the side call to carry the
main turn's tools list without the model treating it as an invitation to call one.

## Cost

Per-turn input grows because earlier tool results are carried forward. A heavily researched turn
is typically 30–80K tokens of tool output (`web_read` is capped at 12K chars per page). But:

- Today almost none of it is cached anyway (breaker #1). After the change, carried-forward history
  is billed at cached-input rates, roughly 10–25% of normal on the registry's providers.
- The earlier `fc34afa` spot check measured the full-history mode at about +2% cost, and that
  was *with* the cache breaking every turn.
- Compaction at 200K is the ceiling, and each compaction is one deliberate cache miss.

The one real cost increase is DB size: a researched turn stores roughly 50–300 KB of transcript.
SQLite handles this fine, but backups grow.

## Decisions

1. **Legacy threads:** turns stored before this change (no `transcript`) keep being rebuilt the old
   way (tool calls replayed from the events log, answer plus sources note). Every new turn is
   stored and replayed verbatim, including new turns on an old thread.
2. **`view_image`:** store whatever the model actually got. A "see" call's image message is kept
   verbatim, and a "describe" call's text result is kept as text. The transcript is exactly what
   was sent, so the cache prefix holds.
3. **Memory index:** stays in the system prompt. A cache miss on the turn after a memory write is
   acceptable.
4. **Ghost threads:** unchanged (client-held, answer-only history).
5. **Reasoning passback** (OpenRouter `reasoning_details`): out of scope, separate follow-up.

## Verification plan

- Unit: for a two-turn fake-LLM run (`llm/llmtest`), assert turn 2's request messages start with
  exactly turn 1's last request messages (a byte-level JSON prefix check). That's the property the
  whole design rests on, so it gets a test that fails if anything re-introduces a rebuild step.
- Live: `dev/fakeopenrouter`'s `/_control/calls` to diff consecutive requests in a real running
  server.
- Measure first: phase 1 (issue #107) ships ahead of the rest, so the potato gives a real
  before/after hit rate. The per-turn numbers also go on the "turn completed" event.
