# Verbatim turn transcripts (replacing reconstructed history)

**Status:** proposal, 2026-09-23. Nothing implemented yet.

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
| 5 | `{memories}` index in the system prompt | `applyMemoriesPlaceholder` | Full miss on the turn after any memory write. This one is acceptable (see Open questions). |
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
final assistant message. That covers the user message, tool-call assistant messages (commentary
text included, original IDs, original batching), tool results (untruncated), nudges, the wrap-up
prompt and the final answer. It is simply `messages[len(history)+1:]` plus
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

- **Move the timestamp out of the system prompt and into the user message**, e.g. a first line like
  `[Sent Tuesday, September 23, 2026, 15:04 EDT]` on `turnMessage`. It's stored in the transcript,
  so it never changes afterward, and the model also learns *when* each earlier turn was asked
  (a real gain for "latest"-type follow-ups days later). prompt.md gets one sentence saying the
  newest stamp is "now".
- **Mode instructions** (focus/voice/deep/no-research): keep them in the system prompt so they
  still apply from the first turn. Stop sending `modeReinforcement` as a separate unstored message;
  append it to the stored `turnMessage` instead. It then stays near the end, where it has the most
  effect, and it becomes part of the stable transcript.

### 5. Delete the patches

- `gateway/history_replay.go` (entire file), except the legacy fallback if it's kept (see Open
  questions).
- The `full_turn_history` setting: `settings.go`, the `SettingsPanel.svelte` toggle,
  `settings.svelte.ts`, `ClientMessage.FullTurnHistoryOverride`, `AskRequest.FullTurnHistory`.
- `effectiveContextWindowTokens`. `config.ContextWindowTokens` defaults to **200 000** (config.go
  plus both `config.yaml.example` files). The smallest registry window is Mercury 2.5 at 260K.
- `appendCitedSources`, `polarisNoteMarker`, `StripFakeSourcesNote` and its call in `turn.go`, and
  `appendPendingQuestionOptions`, from the live-turn path. `EffectiveHistory` stays for
  `search_chats`' `ReadThread`, a flat read of *another* thread, where a plain source list is still
  useful. It no longer needs the "Polaris note" framing because it never goes back into the model
  as its own words.
- prompt.md's "Earlier turns" section shrinks to a line or two.

### 6. Compaction gets cheaper, too

`compactThread` currently sends a different system prompt plus an answer-only history, so it never
hits the cache and summarizes only answers. Instead, send the **exact turn prefix** (same system
prompt, same tools, same transcript) plus a final user message with the compaction instruction.
That is almost entirely cache reads, and the summary can draw on the actual sources. The same
change works for `generateSuggestions` and `regenerateTitle` if wanted.

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

## Open questions (decisions for the operator)

1. **Legacy threads:** keep today's rebuild as a fallback for turns with no stored transcript
   (recommended: invisible, one cache miss per old thread), or treat them as answer-only and delete
   `history_replay.go` outright?
2. **`view_image` data URLs in stored transcripts:** store them verbatim (exact replay, but base64
   images re-sent every turn), or store a text placeholder (small and cheap, at the cost of one
   mid-transcript cache miss on turns that viewed an image)? Recommendation: placeholder.
3. **Memory index:** leave it in the system prompt (a cache miss only on the turn after a memory
   write, recommended), or snapshot it per thread?
4. **Ghost threads:** these keep a client-held, answer-only history. Leave as-is (recommended,
   since they're deliberately ephemeral), or have the server hand the client an opaque transcript blob?
5. **Reasoning passback** (OpenRouter `reasoning_details`) is out of scope here. Today it isn't
   passed back even *within* a turn. That's a separate follow-up worth a spike against DeepSeek
   V4.1.

## Verification plan

- Unit: for a two-turn fake-LLM run (`llm/llmtest`), assert turn 2's request messages start with
  exactly turn 1's last request messages (a byte-level JSON prefix check). That's the property the
  whole design rests on, so it gets a test that fails if anything re-introduces a rebuild step.
- Live: `dev/fakeopenrouter`'s `/_control/calls` to diff consecutive requests in a real running
  server.
- Measure first: `llm.ChatResponse` already parses `CacheReadTokens`/`CacheWriteTokens`, but
  nothing records them. Summing them per turn onto the "turn completed" event is a small change
  that can ship ahead of everything else. It gives a real before/after hit rate from the potato
  instead of a guess, and it confirms breaker #1 on live data.
