package prompts

// uiDefaults fills d's Intelligent UI fragment — mirrored in prompts.yaml's
// `ui:` section, and kept identical by TestDefaults_MatchRealPromptsYAML.
func uiDefaults(d *Set) {
	d.UI.Base = "## Visual blocks\n\n" +
		"Prose is the default and is always a valid answer. Add a visual block only when it is clearly\n" +
		"easier to scan than the same words would be. A block is a fenced code block whose info string is\n" +
		"exactly `ui`, written at the top level of your answer (never inside a list item or a quote) and\n" +
		"mixed in with ordinary prose. Inside it, write one JSON object per line, with no commas between\n" +
		"lines:\n\n" +
		"- `{\"c\":\"callout\",\"tone\":\"answer\",\"text\":\"...\"}` — one highlighted point. tone is answer, note,\n" +
		"  warn or ok. \"answer\" is a bottom-line card; give it \"asof\":\"YYYY-MM\" when how recent it is matters.\n" +
		"- `{\"c\":\"stat\",\"label\":\"...\",\"value\":\"...\",\"note\":\"...\"}` — one headline number.\n" +
		"- `{\"c\":\"compare\",\"cols\":[\"A\",\"B\"],\"pick\":0}` then one `{\"row\":\"Price\",\"v\":[\"...\",\"...\"]}` line per\n" +
		"  attribute: 2 to 4 columns, at most 12 rows, \"v\" has one value per column, \"pick\" is the column you\n" +
		"  recommend (leave it out if you don't).\n" +
		"- `{\"c\":\"steps\",\"title\":\"...\"}` then `{\"i\":\"Step\",\"d\":\"detail\",\"t\":\"2 min\"}` lines, at most 15. Leave\n" +
		"  out \"t\" unless it is a real duration.\n" +
		"- `{\"c\":\"timeline\"}` then `{\"when\":\"1969\",\"i\":\"What happened\"}` lines, at most 15, in time order.\n" +
		"- `{\"c\":\"checklist\",\"title\":\"...\"}` then `{\"i\":\"Item\"}` lines, at most 20 — things to prepare or tick off.\n" +
		"- `{\"c\":\"procon\",\"pro_h\":\"Pros\",\"con_h\":\"Cons\"}` (headings optional) then `{\"+\":\"point\"}` and\n" +
		"  `{\"-\":\"point\"}` lines, at most 8 of each — one thing weighed for and against.\n" +
		"- `{\"c\":\"choose\",\"title\":\"...\"}` then `{\"if\":\"their situation\",\"then\":\"the pick\"}` lines, at most 8 —\n" +
		"  for when the right choice depends on the person.\n" +
		"- `{\"c\":\"facts\",\"title\":\"...\",\"sub\":\"...\"}` then `{\"k\":\"Label\",\"v\":\"value\"}` lines, at most 12 —\n" +
		"  an at-a-glance card about one named thing.\n" +
		"- `{\"c\":\"flow\"}` then `{\"n\":\"a\",\"t\":\"Step\",\"d\":\"detail\"}` node lines (add `\"kind\":\"decision\"` for a\n" +
		"  question) and `{\"e\":[\"a\",\"b\"],\"l\":\"Yes\"}` edge lines, at most 8 nodes, the first node is the start —\n" +
		"  a process or decision chain with branches.\n" +
		"- `{\"c\":\"tabs\"}` then `{\"tab\":\"macOS\",\"text\":\"...\"}` lines, at most 6 — parallel versions of one\n" +
		"  answer where the reader needs only one. A tab's text may hold a fenced code block (write the\n" +
		"  newlines as \\n inside the JSON string) and can run to about 1500 characters.\n" +
		"- `{\"c\":\"disclose\",\"title\":\"...\",\"hint\":\"...\"}` then `{\"p\":\"paragraph\"}` lines — a section of\n" +
		"  deeper detail, shown open, that the reader can fold away.\n" +
		"- `{\"c\":\"quote\",\"text\":\"...\",\"by\":\"Name\",\"src\":[\"https://...\"]}` — a short passage quoted word for\n" +
		"  word from a source you read. Never paraphrase inside a quote block.\n" +
		"- `{\"c\":\"claim\",\"text\":\"...\",\"verdict\":\"misleading\"}` then `{\"+\":\"supporting point\",\"src\":[\"https://...\"]}`\n" +
		"  and `{\"-\":\"disputing point\",\"src\":[\"https://...\"]}` lines, at most 6 of each — checking one specific\n" +
		"  claim. verdict is true, mixed, misleading, false or unverified: your own read of the evidence.\n\n" +
		"A line with a \"c\" key opens a new block; a line without one belongs to the block above it. Text\n" +
		"fields take **bold**, `code` and [Title](URL) citations just like prose; keep each under about 300\n" +
		"characters. Don't repeat a block's content in the prose around it, and keep any caveat that changes\n" +
		"what the person should do in the prose or a warn callout, not buried in a table cell. Cite only\n" +
		"sources you actually read, and where a row, step or line rests on one, give that line\n" +
		"`\"src\":[\"https://...\"]` (or a [Title](URL) in its text) so the reader can see where it came from."
	d.UI.LowBar = "Use a block rarely: only when the answer is clearly one of these shapes and prose would be\n" +
		"harder to scan. When in doubt, write prose."
	d.UI.NormalBar = "Use a block whenever one of these shapes clearly fits the answer, at most one or two per\n" +
		"answer. Short or conversational answers stay prose."
}

// UIFragment is the system-prompt text teaching the `ui` block grammar for a
// Visuals dial value, or "" when blocks shouldn't be offered ("off", the empty
// string an unwired entry point leaves, or anything unrecognized). Returning
// "" for the unknown case is the safe direction: a mode this code has no
// wording for must not guess at how eagerly to use blocks.
func (s *Set) UIFragment(visuals string) string {
	switch visuals {
	case "low":
		return s.UI.Base + "\n\n" + s.UI.LowBar
	case "normal":
		return s.UI.Base + "\n\n" + s.UI.NormalBar
	}
	return ""
}
