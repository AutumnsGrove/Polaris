# Report generator

**Status: designed, not yet implemented.** Answers issue #41's open questions with concrete
decisions tied to real files in this codebase; no application code has been written. A mockup of
the rendered result lives at `mockups/report-generator.html`.

## What it is

A `generate_report` tool the model reaches for when the user asks for something like "generate me
a report on this" — comparable to GPT Researcher's output shape (a real, citation-heavy Markdown
document), built out of machinery this codebase already has rather than a new research stack.

## Design decisions (the issue's open questions)

**Reuses Tier 2's fan-out, plus a dedicated synthesis pass — not a new research loop.**
`generate_report` triggers the same `agent.SpawnResearchers` machinery Deep Research's Tier 2 uses
(`docs/plans/deep-research-two-tier.md`) for source-gathering: the orchestrator decomposes the
topic into non-overlapping angles, `spawn_researchers` fans them out, each sub-agent comes back
with `{claim, sources}` findings. What's new is what happens *after* that: instead of the
orchestrator synthesizing findings into a normal chat reply, a dedicated synthesis call runs with
its own `prompts.yaml` fragment (`report_synthesis`, alongside `subagent_task`) instructing it to
produce a complete Markdown document — sections, inline citation markers, a closing bibliography —
rather than a conversational answer. This mirrors Pulsar Daily's own two-pass shape (Stage A
fan-out, then a distinct finalize pass — `tools/finalize_daily_items.go`) more than it resembles a
single agent turn.

For a narrower ask that doesn't need multi-angle fan-out, the tool still runs the synthesis pass
alone over whatever `web_search`/`web_read` calls the orchestrator already made in-turn — fan-out
is an escalation, not a requirement, matching `spawn_researchers`' own "reserve this for genuinely
broad questions" guidance.

**Citations: reuse `tools.Citation`/`AddCitation` as-is, no new shape.** Every source gathered
during the research phase already flows through `ctx.AddCitation` (`tools/registry.go:620`), which
already dedupes by URL. The synthesis pass receives the accumulated `ctx.Citations` list and is
instructed to (a) weave `[Title](URL)`-style inline references through the prose at the point each
claim is used, matching the plain-chat citation convention already in
`prompts.yaml`'s `fallback_system_prompt`, and (b) close with a numbered "Sources" section built
directly from that same list — a real bibliography, not a second citation system to keep in sync
with the first.

**Output shape: rendered inline as a normal chat message, plus a downloadable `.md` attachment.**
No new artifact/canvas system. The synthesis pass's Markdown output is streamed and rendered
exactly like any other assistant reply — the chat's existing Markdown renderer already handles
headers, tables, lists, and fenced code/mermaid blocks, so a long structured report needs nothing
new there. Additionally, the raw Markdown is saved through the existing attachment pipeline
(`gateway`'s attachments dir + `store.Store.SetMessageAttachment`) so the user gets a real
"Download report.md" affordance on the message, the same shape a user-uploaded file already gets,
just attached by Polaris instead of the user. See `mockups/report-generator.html` for how this
reads in the transcript.

**Persistence: lives in its thread, no dedicated Reports list for v1.** Unlike Pulsar Daily's
editions (a genuinely separate browsable surface by design), a report is the answer to one
specific ask in one specific conversation — `search_chats` can already find it later the same way
any other message is findable. A dedicated "Reports" list is real added surface (a new route, nav
entry, its own empty/loading states) that the issue itself only poses as an open question, not
something real usage has asked for yet. Revisit if report generation turns out to get heavy
enough real use that "which thread was that report in again?" becomes an actual complaint — the
same bar Pulsar Daily's own editions list cleared before it got built.

## Shape

```go
// tools/generate_report.go — new file, same pattern as spawn_researchers.go
var generateReportDef = llm.ToolDef{
    Function: llm.ToolFunctionDef{
        Name: "generate_report",
        Parameters: map[string]interface{}{
            "type": "object",
            "properties": map[string]interface{}{
                "topic": map[string]interface{}{
                    "type": "string",
                    "description": "The report's subject, phrased as a title/question — becomes the document's own heading.",
                },
                "angles": map[string]interface{}{
                    "type": "array",
                    "items": map[string]interface{}{"type": "string"},
                    "description": "Optional: 2-10 distinct sub-topics/angles to research in parallel via spawn_researchers. Omit for a narrower report that doesn't need fan-out.",
                },
            },
            "required": []string{"topic"},
        },
    },
}
```

**Gating: available in plain chat too, not `requires: deep_research`** — reconsidered from the
first pass, which would have hard-gated this behind Deep Research the same way `spawn_researchers`
is. Decided against that: someone asking for "a report on X" in plain chat shouldn't be told the
tool doesn't exist until they flip a toggle they may not know is relevant. Instead, `generate_report`
is offered everywhere, and *which tier it runs* depends on `angles` and the composer's Deep
Research state exactly as the "Reuses Tier 2's fan-out" section above already describes — Deep
Research on lets the orchestrator use `spawn_researchers` for real fan-out, Deep Research off runs
synthesis-only over whatever's already been found in-turn.

That narrower plain-chat report is real but genuinely thinner, so it needs a visible label, not a
document indistinguishable from a fanned-out one — the synthesis pass tags its own output with
which tier produced it, and the rendered report card shows a small badge (`Quick report` vs. `Deep
Research report`) next to the source count, so it's never ambiguous which depth the user is
looking at. `mockups/report-generator.html` should get this badge added when the mockup is next
touched — not yet reflected there.

## Open items for implementation

- `report_synthesis` prompt fragment: needs real drafting/iteration (section structure, how
  strict to be about citation density) the way `subagent_task` and `research_check_in` were
  tuned, not something to get right on the first pass.
- Progress visibility: this can run for minutes (fan-out + synthesis), same territory as Deep
  Research's existing `research_check_in` nudges — the tool should emit `tool_call`/`tool_result`
  events per phase (researching → synthesizing → done) the way `spawn_researchers` already emits
  per-sub-agent progress, so the transcript doesn't sit blank the whole time.
- Real migrations/store changes: none required — this reuses `SetMessageAttachment` as-is.
- CLAUDE.md's dual-deployment checklist: no new hot-reloaded resource file, no new CLI command, no
  new settings-panel deployment action — this is pure in-agent tool logic, so the checklist mostly
  doesn't apply. The one item worth a pass: confirm attachment storage (a bind-mounted volume
  under Docker vs. a plain host directory bare-metal) behaves identically for a
  Polaris-generated attachment as it does for a user-uploaded one — it should, since it's the same
  code path, but worth a live check per this repo's culture rather than assuming.
