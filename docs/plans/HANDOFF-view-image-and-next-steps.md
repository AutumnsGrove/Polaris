# Handoff: view_image shipped, what's next

Scratch/status doc for picking this work back up — not a design doc itself (see the three linked
below for that). Written 2026-09-13 after `view_image` shipped and was live-verified.

## What's actually shipped and working (commit `fd7cf0e`, on `main`)

`view_image(card_index, mode, instructions)` — lets the model reference a numbered `image_search`
result and either get a text description (`describe`, always available) or have the real image
inserted into the live conversation (`see`, only when the thread's model is multimodal).

**Live-verified against the real `/api/ask` endpoint** (local `polaris.db`, real SearXNG at
`localhost:18888` via the `searxng-dev` container, real OpenRouter keys), not just unit tests:

- `model: "deepseek"` (DeepSeek V4 Flash, **not** multimodal) — correctly used `describe` mode,
  identified itself as non-multimodal via the new `{multimodal}` prompt line, self-corrected
  gracefully when one candidate image failed to fetch as valid image bytes.
- `model: "deepseek-v41-flash"` (DeepSeek V4.1 Flash, multimodal) — log-confirmed
  `view_image mode=see card_index=1` actually fired, and the model's response contained specific
  visual details it could only have from real image content. This resolves `view-image.md`'s one
  named open risk (whether the actual configured provider/model accepts a real image content block
  mid-conversation) — confirmed live, not just inferred.
- **One real, non-blocking finding**: a `lookaside.fbsbx.com` (Facebook crawler-media) image URL
  failed against all four vision providers in the OpenRouter fallback chain ("invalid image
  format" / "base64 data is not valid") — that URL likely doesn't serve real image bytes without a
  browser session. Not fixed — the system already degrades gracefully (model tried a different
  card without any special-casing needed). Worth knowing if it recurs with other Facebook-hosted
  image results specifically.

Files touched: `tools/view_image.go` (+test), `tools/image_search.go` (card-index hints in result
text), `llm/client.go` (`ChatMessage.ImageURLs` + custom `MarshalJSON`), `llm/vision.go`
(`DescribeImage` gained an `instructions` param), `gateway/attachments.go` (`visionClient` helper,
shared with the new `DescribeImage` closure), `gateway/turn.go` (wires `Multimodal`/`DescribeImage`
onto `tools.Context`), `agent/driver.go` (`FlushPendingImageMessages` after each tool-call batch —
**never interleaved with tool-result messages**, a real DeepSeek 400 otherwise), `prompt.md` +
`prompts.yaml` + `prompts/prompts.go` (new `{multimodal}` placeholder).

## What's designed but NOT built yet — this is the actual next work

In dependency order:

1. **`docs/plans/code-execution.md` (issue #42)** — the sandboxed `code_exec` tool itself. Memory
   or package-set questions are all resolved (real numbers measured on the potato), the
   file-persistence design is settled (ephemeral-per-call containers, persistent per-thread
   workspace directory). **Nothing has been implemented yet** — this doc is 100% still a plan.
2. **`docs/plans/fetch-and-workspace-tools.md` (issue #60)** — `fetch_url` (provenance-scoped: a
   citation-checked URL or an `image_search` card index, content-type/size validated, host-side
   only) and `read_attachment`'s widened gating. **Blocked on #1** — writes into the workspace
   directory #1 creates.
3. **`view_image`'s `path` parameter** — already in the tool's schema (`tools/view_image.go`),
   currently always rejected with "viewing a workspace file isn't supported yet (code execution
   hasn't shipped)". Once #2's workspace exists, wire `path` to read a file from
   `<workspace_root>/<thread_id>/<path>` the same way `card_index` reads from `ctx.CardsSnapshot()`
   today — same describe/see mode logic applies unchanged, just a second image *source*.
4. **`docs/plans/pulsar-daily.md`'s/#44's code-generated charts** — rides #1, unblocked once it
   ships (full package set including matplotlib was confirmed to fit with wide margin).

Also open, no urgency: **issue #61** (deprecate/unify bare-metal vs. Docker deployment modes) — a
"someday" conversation, has a comment recording a concrete middle-path idea (bare-metal + an
optional local Docker daemon used narrowly as a sandbox engine, not for deploying Polaris itself).

## Quick-start for resuming

- Read `docs/plans/code-execution.md` first (the memory/package-set/persistence decisions are all
  there) — that's the next thing to actually build.
- `docs/plans/fetch-and-workspace-tools.md` and `docs/plans/view-image.md` are the other two design
  docs from this same session; `view-image.md` also documents the two real implementation
  decisions this doc doesn't repeat (why `see` mode uses a synthetic follow-up message instead of
  putting the image in the tool result itself, why the multimodal gate lives in the handler rather
  than the tool schema).
- Issue #60 has a summary comment linking all three design docs together — good starting point on
  GitHub instead of re-reading every doc cold.
