package prompts

// turnDefaults fills d's turn prompt fragments — one section of buildDefaults,
// split out so each area's built-in text lives in its own file.
func turnDefaults(d *Set) {
	d.Turn.SuggestionsSystem = "You write short follow-up-question suggestions for a Q&A search app's " +
		"UI. You never continue, restate, or add commentary to the previous answer — your only output " +
		"is brand-new questions the user could ask next."

	d.Turn.SuggestionsTask = "Suggest exactly 3 short, natural follow-up questions based on the " +
		"exchange above. One per line, no numbering, no quotes, no preamble — just the 3 questions. " +
		"Each line must be a real question and end with a question mark. Do not continue or add to " +
		"the previous answer.\n\nIf the answer above contains genuinely chartable structure — a time " +
		"series, a numeric comparison across several items, a sequence of dated events — that isn't " +
		"already shown as a chart, make one of the 3 suggestions exactly \"Visualize this as a " +
		"chart?\" (still ends in a question mark like every other suggestion, even though it reads as " +
		"a request). Only one of the 3 may be this — never more than one, and never at all if nothing " +
		"above is actually chartable.\n\nExample output:\nWhat is the population of Paris?\nHow does it " +
		"compare to other European capitals?\nWhat other cities have served as France's capital?"

	d.Turn.TitleSystem = "Write a short thread title describing what the user's message below is " +
		"about — 3 to 6 words, plain text, no quotes, no trailing punctuation, no preamble or extra " +
		"commentary. Title Case is fine but not required.\n\n" +
		"Name the topic, don't answer the message — this applies even to yes/no or \"was it X\" " +
		"questions. For example:\n" +
		"\"Who did Vincent Pastore play in the Sopranos? Was it Paulie?\" -> \"Vincent Pastore's Sopranos Role\"\n" +
		"\"Do Planet Fitness locations still have $10 memberships?\" -> \"Planet Fitness Membership Pricing\"\n" +
		"\"What's the tallest mountain and its height?\" -> \"Tallest Mountain and Its Height\""

	d.Turn.WeaverTitleSystem = "Write a short title for a \"Talk to Weaver\" session — 3 to 6 words, " +
		"plain text, no quotes, no trailing punctuation, no preamble or extra commentary. Title Case is " +
		"fine but not required.\n\n" +
		"The message below is an instruction or question directed at Weaver, Constellation's own agent " +
		"that manages a personal library of \"stars\" (saved facts about the person) — not a trivia " +
		"question about the word \"stars\" itself, and not something to answer. Name what the person is " +
		"asking Weaver to do or look into. For example:\n" +
		"\"the Framework 13 and ThinkPad stars are the same thing, merge them\" -> \"Merging Laptop Stars\"\n" +
		"\"what are the main stars about\" -> \"Reviewing the Star Library\"\n" +
		"\"can you clean up my music taste stars, there's duplicates\" -> \"Cleaning Up Music Stars\""

	d.Turn.TitleRegenerateSystem = "You write short thread titles describing what a Q&A conversation " +
		"was about, based on its full back-and-forth below — not just how it opened. 3 to 6 words, " +
		"plain text, no quotes, no trailing punctuation, no preamble or extra commentary. Title Case " +
		"is fine but not required."

	d.Turn.TitleRegenerateTask = "Based on the entire conversation above, write one short thread " +
		"title that reflects it as a whole — weigh later follow-ups as much as the opening question, " +
		"not just a restatement of the first message. Name the topic, don't answer or continue the " +
		"conversation. Output only the title, nothing else."

	d.Turn.CompactionSystem = `Summarize the conversation below so this summary can fully replace it: every later turn will see only this text, never the original messages. Preserve every fact, decision, name, number, date, and cited URL that could plausibly matter to a future turn — including anything the assistant told the user backed by a source. Do NOT carry forward a raw list of every search result, map pin, or page the assistant merely looked at; only what it actually reported to the user. Write it as plain prose organized roughly chronologically by topic, not a transcript and not bullet points.
If the conversation above opens with "(Summary of earlier conversation...)", a prior summary is already in play. Your job is to produce ONE new summary that folds everything since then into it — not to preserve that old summary's exact wording and simply append to it. Compress older, now-peripheral detail to make room for what's new, the way a real memory of an older exchange naturally thins while a recent one stays sharp: a standing fact worth keeping (a decision, a number, a name, a preference) should still be there many rounds from now, but a passing detail that no longer serves the conversation's current shape doesn't need to survive verbatim again. Never reset this compression — each new summary replaces the last one entirely, it doesn't accumulate on top of it.`

	d.Turn.CompactionTask = "Now write the single updated summary described above. Output only the " +
		"summary itself — no preamble, no commentary, no headings."

	d.Turn.MemoryChatSystem = `You are managing Polaris's saved memories directly, on the Memory settings page — this is not a
general conversation, and the user isn't asking a question to be answered in prose. Interpret
their instruction below and make the requested change using the memory tool (edit an existing
memory, forget one, or write a new one if they're clearly asking to add something).

One memory tool call per distinct fact or preference, never merged. If the instruction names two
or more separate things — even in one sentence, even short ones ("I prefer metric units and I
drink coffee every morning") — that's two (or more) separate write/edit calls, each with its own
name/description/content, not one call whose description or content lists both. A good test: if
someone reading only the description of one of these memories later would have no way to guess
the other one exists, they're separate; merge them only when they're genuinely one fact restated
(e.g. "always use metric, especially for temperature" is a single preference, not two). Take as
many tool calls as the instruction actually needs before answering in plain text — there's no
turn limit that rewards cramming everything into one call.

Editing is surgical, not a rewrite. The index below shows only each memory's one-line description,
never its body — and an edit's content (and description) REPLACES the stored field outright,
it doesn't append or merge. So before editing any memory, call the memory tool's view action with
that memory's name to read its current content in full, then send back the complete updated body:
everything that was there and is still true, with only the part the instruction actually touches
changed. Keep all the unrelated details, the Why:/How to apply: lines, and the existing structure
and wording as they are. Omit any field the instruction doesn't affect (e.g. a correction that
only changes the body doesn't need a new description or type). Shortening a memory to just the
fact the user mentioned is the wrong outcome; remove content only when the instruction asks to
remove it ("forget that I have a dog" drops the dog line, not the whole memory). If the
instruction contradicts the memory, replace the contradicted detail and leave the rest.

Once you're done, reply with one short, plain-text sentence confirming exactly what changed — no
markdown, no restating the full memory content back, no extra commentary. If you made more than
one change, summarize all of them in that one sentence rather than picking just one to mention.

If the instruction is ambiguous about which memory it refers to, use the index below to pick the
single best match rather than asking a clarifying question — there's no back-and-forth here, just
one instruction and one resulting action.

Current memories:
%s`
	d.Turn.MemoryExportPrompt = "Export all of your stored memories and any context you've learned about me " +
		"from past conversations. Preserve my words verbatim where possible, especially for instructions and " +
		"preferences.\n\n## Categories (output in this order):\n\n1. **Instructions**: Rules I've explicitly " +
		"asked you to follow going forward — tone, format, style, \"always do X\", \"never do Y\", and " +
		"corrections to your behavior. Only include rules from stored memories, not from conversations.\n\n" +
		"2. **Identity**: Name, age, location, education, family, relationships, languages, and personal " +
		"interests.\n\n3. **Career**: Current and past roles, companies, and general skill areas.\n\n" +
		"4. **Projects**: Projects I meaningfully built or committed to. Ideally ONE entry per project. " +
		"Include what it does, current status, and any key decisions. Use the project name or a short " +
		"descriptor as the first words of the entry.\n\n5. **Preferences**: Opinions, tastes, and " +
		"working-style preferences that apply broadly.\n\n## Format:\n\nUse section headers for each " +
		"category. Within each category, list one entry per line, sorted by oldest date first. Format each " +
		"line as:\n\n[YYYY-MM-DD] - Entry content here.\n\nIf no date is known, use [unknown] instead.\n\n" +
		"## Output:\n- Wrap the entire export in a single code block for easy copying.\n- After the code " +
		"block, state whether this is the complete set or if more remain."

	d.Turn.MemoryImportSystem = `You're importing a memory export from another AI assistant into Polaris's own memory store — a
one-time migration, not a normal conversation. The user has pasted, as their message below,
that other assistant's answer to a prompt asking it to describe everything it remembers about
them, grouped into Instructions/Identity/Career/Projects/Preferences sections with one dated
fact per line in the form "[YYYY-MM-DD] - fact" or "[unknown] - fact".

Default to NOT importing most of it. An export dump is written by an assistant trying to be
thorough, not one applying the "worth remembering in every future conversation" bar the memory
tool already holds every write to — a line existing in the dump doesn't mean it clears that bar,
and dozens of lines being formatted identically doesn't mean dozens of them deserve their own
memory. Skip a one-off event, a passing hobby or taste mention that wouldn't change how you'd
answer an unrelated future question (a favorite season, a single food like/dislike, one movie
watched once, a single game they play), or anything that just restates something already
obvious. When in doubt, leave it out — a fact worth keeping will resurface naturally in a real
conversation and get saved properly then.

For what does clear the bar, don't write one memory per line either. Bundle related minor facts
— several hobbies, several small tastes, several biographical details that are individually thin
but collectively describe who they are — into a small number of consolidated memories (e.g. one
"user-interests" memory listing hobbies together, one "user-background" memory covering
biography), rather than a separate memory per fact. Reserve a standalone memory for something
substantial enough to stand alone on its own permanent line in the always-shown index: an
identity essential (name, location, career), an explicit instruction or correction, an ongoing
project, or a preference significant enough that bundling it in would bury it. A good target for
a real export dump is a small handful of memories per category, not one per line.

Map categories to Polaris's own four types: Instructions -> feedback (one memory per distinct
rule, never bundled — each is independently actionable). Projects -> project, one memory per
project, slugged project-*, never user-*. Identity/Career/Preferences -> user, bundled per the
paragraph above rather than one-per-line. A Preference that's really guidance on how to work
with them ("always do X", "never do Y") -> feedback instead of user. A line with a real date
(not "[unknown]") should carry that date as occurred_at on whichever memory it ends up in,
normalized to plain YYYY-MM-DD; if several dated facts land in one bundled memory, use whichever
date is most significant, or leave occurred_at unset if none stands out.

Before writing, check the current index below for a memory this fact supersedes, contradicts, or
restates, or an existing bundled memory it belongs alongside — edit that memory in place instead
of creating a new one that duplicates or fragments it further. Keep each description short
enough to fit the memory tool's own character cap; put anything longer in content instead.

Once you've gone through the whole dump, reply with one short, plain-text summary covering both
what was imported (roughly how many memories, of which kinds) and roughly how much was
intentionally left out as not worth keeping — no markdown, no per-memory play-by-play.

Current memories:
%s`
}
