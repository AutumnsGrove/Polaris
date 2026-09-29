// Package prompts loads prompts.yaml — every LLM instruction/prompt
// fragment Polaris sends outside of prompt.md (the main system prompt,
// which stays its own plain-text file so it's easy to write/paste
// without YAML string-escaping — see agent.loadSystemPrompt). Consolidating
// the rest here means updating how Polaris asks the model for a title, a
// summary, a follow-up suggestion, or an image description is a one-file,
// no-rebuild edit instead of a Go string literal buried in whichever
// package happens to make that call.
package prompts

import (
	"polaris/logger"
)

var log = logger.WithPrefix("prompts")

// path is read fresh (subject to the mtime cache below) every call —
// same hot-reload convention as agent.loadSystemPrompt for prompt.md:
// edit the file, see the change on the very next turn or tool call, no
// rebuild or restart.
const path = "prompts.yaml"

// defaults mirrors prompts.yaml's shipped content exactly — the
// fallback-of-the-fallback if the file is missing, unreadable, or fails
// to parse (e.g. right after a hand-edit with a YAML syntax error), and
// the source Get fills any blank field in from when prompts.yaml is only
// partially customized. Keeping this in Go rather than relying solely on
// the file means a corrupted prompts.yaml degrades to "the built-in
// prompts", not "broken/empty prompts sent to the model".
var defaults = buildDefaults()

// buildDefaults assembles the built-in prompt set from its per-section builders
// (defaults_*.go). TestDefaults_MatchRealPromptsYAML keeps the result identical to
// the shipped prompts.yaml, so a section's text is edited in exactly two places:
// that file and the matching defaults_*.go.
func buildDefaults() Set {
	var d Set
	agentDefaults(&d)
	turnDefaults(&d)
	toolsDefaults(&d)
	weaverDefaults(&d)
	wizardDefaults(&d)
	pulsarDefaults(&d)
	oracleDefaults(&d)
	return d
}
