# view_image: letting the model actually look at an image

**Status: shipped (2026-09-13) for `image_search` card results.** `path` (a workspace file — a
fetched image, a `code_exec`-generated chart) is accepted in the tool's schema but always rejected
with a clear "not yet supported" error until `docs/plans/fetch-and-workspace-tools.md`'s workspace
exists. Filed against issue #60.

This doc covers a gap distinct from (but related to) `docs/plans/fetch-and-workspace-tools.md`:
once an image exists somewhere reachable (an `image_search` result, a fetched file, a `code_exec`-
generated chart), the model currently has **no way to reason about its actual contents** at all.

## The gap, confirmed in the real code

Checked `tools/image_search.go` directly. `finishImageSearch` returns the model a string like
*"found 3 images for 'pretty dresses' — they're attached to this turn's answer, no need to
describe them individually."* The real `ImageURL`/`FullImageURL` values only ever populate `Card`
structs consumed by the frontend gallery — **the model itself never sees them**, and has no
mechanism today to distinguish one result from another beyond a search snippet's title text. Ask
it to pick "the prettiest one" or compare two candidates and it's guessing from filenames, not
content.

Separately, checked how vision already works elsewhere in this codebase
(`gateway/attachments.go`'s `resolveAttachment`, `llm/vision.go`'s `DescribeImage`): **even for a
genuinely multimodal model, an uploaded image is only ever converted to a text description once,
upfront** — the description (not the image) is what enters the conversation, permanently flattened.
There is no code path anywhere that inserts a real image into an ongoing, multi-turn tool-calling
conversation. `vision.go`'s own doc comment explains why: `ChatMessage.Content` is a plain string
throughout the whole agent loop, and widening it for one one-off use case was deliberately avoided.

## The tool: one tool, a `mode` parameter, gated by model capability

`view_image`, taking:
- **A reference to what to look at** — either an `image_search` card index (see
  `fetch-and-workspace-tools.md`'s provenance discussion — this is the *other* consumer of that
  same card-index mechanism) or a workspace file path (a fetched image, or a `code_exec`-generated
  chart).
- **`mode`: `"describe"` or `"see"`.** `"see"` only appears in the tool's parameter schema at all
  when the currently selected model is multimodal — the same conditional-tool-definition mechanism
  `catalog.go` already uses to gate `read_attachment`'s availability on `ctx.AttachmentData`, just
  applied to one parameter's enum instead of a whole tool. A non-multimodal model's `view_image`
  only ever offers `"describe"`.
- **`instructions` (optional, `describe` mode only)** — same shape as `web_read`'s own
  `instructions` parameter: a caller-supplied focus ("just the fabric texture and color") folded
  into the description prompt instead of the default full-literal-description framing. Needs its
  own prompt entry in `prompts.yaml` (an instructed variant of `Vision.DescribeImage`) and a small
  extension to `DescribeImage` to accept it. `see` mode has no equivalent parameter — once the
  model can actually see the image on its own next turn, it just asks whatever follow-up question
  it wants directly, no separate instruction channel needed.

## `describe` mode: the existing mechanism, reused as-is

Exactly today's `DescribeImage` flatten-to-text path, just invoked on demand instead of only at
upload time: fetch the bytes (via `SafeDialContext`, the same protection `resolveAttachment`'s own
remote-image fetch for Pulsar Daily's Picture of the Day already uses) if the source is a card
reference, or read them from the workspace if it's a file path; call `DescribeImage` using the
exact selected-model-or-fallback logic `resolveAttachment` already has (use the thread's own model
if it's multimodal, else `cfg.MultimodalModel()`'s configured vision model); return the text as an
ordinary tool result. No new architecture — a new entry point onto existing code.

## `see` mode: the actual leap — real image passthrough

Only available when the selected model is multimodal. The mechanism, chosen deliberately over
simpler-looking alternatives:

**Don't put the image inside the tool result itself.** Tool-role messages are conventionally
string-only across most providers' chat-completions schemas — betting this feature on undocumented
array-content support inside a `tool` message is a real, avoidable risk. Instead: the tool result
stays a short text confirmation ("viewing the image now"), and **a synthetic follow-up `user`-role
message carrying a real `image_url` content block gets appended immediately after** — the exact
content-block shape `vision.go`'s `DescribeImage` already sends and gets a real response back for,
just placed as a genuine conversation turn instead of a one-off side call. This is the smallest
change that gets real seeing rather than description: reuse a proven shape, don't invent a new one.

This also directly answers the motivating case for building this at all: a multimodal model
reviewing its own `code_exec`-generated chart gets the actual pixels this way, not a text
description of them — "does this chart look right" only means something if the model can actually
look.

**Implementation note**: the OpenAI-compatible wire protocol requires every "tool" role result
message in a batch to land back-to-back immediately after the assistant's tool-calls message, with
nothing else interleaved (confirmed the hard way elsewhere in this codebase — DeepSeek 400s
otherwise). A `see` call's synthetic image message can't be inserted right after its own tool
result for this reason; it has to be queued (`tools.Context.AddPendingImageMessage`) and flushed
once, after the *entire* batch's tool-result messages, the same way `agent/driver.go` already
defers "nudge" messages past a multi-call batch.

**`mode`'s multimodal gating happens in the handler, not the schema.** `catalog.go`'s `Requires`
mechanism only gates whole-tool availability, not one parameter's enum value — building
per-parameter conditional schemas for this one case wasn't worth it for v1. `"see"` stays valid
JSON Schema regardless of the model's capability; `handleViewImage` rejects it with a clear error
pointing back at `"describe"` if the thread's model isn't multimodal. Paired with a new
`{multimodal}` prompt placeholder (`prompt.md`, filled by `agent/driver.go`'s
`applyMultimodalPlaceholder`) that tells the model directly, every turn, whether it's vision-capable
and which mode to reach for — so in practice the model shouldn't need to hit that rejection to find
out.

### Two costs worth naming, not two blockers

**Not unique to images, but heavier per-turn than text.** A stateless chat-completions API resends
the entire prior conversation on every call, regardless of content type — a raw image just costs
more tokens each time that resend happens than plain text would, for the rest of that thread's
life. This is self-moderated by design, though, not by a hard cap: `see` is opt-in per call, and
the tool is expected to be used for "I need to actually judge this visually," not "describe every
result routinely" — `describe` stays the cheap default for routine checks. No pruning/summarization
mechanism is being built for this now; if it ever needs one (dropping older images from replay
after N turns, keeping only the most recent live), that's a later decision once real usage shows
whether it's actually needed.

**Provider/model vision support isn't guaranteed by "the model is marked multimodal."**
`attachments.go`'s own doc comment records a real, live-found gap: a model configured as
`Multimodal: true` still 404'd on image input for one specific OpenRouter provider routing ("No
endpoints found that support image input") — which is why `resolveAttachment`'s vision call
deliberately leaves provider routing unpinned rather than reusing the thread's pinned endpoint.
Decided not to treat this as a blocker here: the models actually configured for multimodal use in
this deployment route through their official default endpoints (verified image-input support),
not the kind of narrow third-party routing that produced the earlier gap. Still worth a real
smoke test — an actual multi-turn call through the configured model and provider, not just code
review — once this is built, matching this codebase's general "verify on real hardware" culture,
since this specific mechanism (an image inserted mid-conversation, not just a one-off call) has
never been exercised in this codebase at all.

## Open items, deliberately not resolved here

- Exact prompt wording for the instructed-`describe` variant in `prompts.yaml`.
- Whether an `image_search` card reference should be fetchable only within the turn it was
  returned, or for the life of the thread (shared with `fetch-and-workspace-tools.md`'s same open
  question — the two tools should agree on an answer, not each pick independently).
- Whether/when a pruning mechanism for old `see`-mode images in long-running threads is worth
  building — explicitly deferred until real usage shows it's needed, not designed speculatively now.
