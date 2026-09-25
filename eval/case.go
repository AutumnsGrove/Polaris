// Package eval is the harness docs/plans/hill-climbing-objectives.md's
// Part 5 calls for: a cheap, repeatable inner loop for scoring single
// prompts and single code paths, sitting next to (not inside) the
// benchmark/ package — benchmark.Suite is built around a full agent.Run
// turn plus a dataset+reference-answer shape, which doesn't fit a
// single-prompt format check or a Jev-graded citation-support case
// without bending it (see the plan's Part 5 #7).
//
// v1 covers two of the plan's four categories:
//   - "format": a single cheap LLM call (title/suggestions generation,
//     the exact prompts.yaml prompts production uses) checked by plain Go
//     code — no Jev needed, these are structural (word count, trailing
//     punctuation, line count) rather than judgment calls.
//   - "citation_support": Jev graded, reusing gateway/verification.go's
//     exact Choice question shape (supported/partially_supported/
//     contradicted/not_addressed criteria) against planted source/claim
//     fixtures — this is directly testing the same judgment Jev makes
//     live for the source-verification badge, just against known-answer
//     fixtures instead of real citations.
//
// The remaining two categories each get their own single-call pipeline
// too, still with no full agent.Run turn:
//   - "factual": one plain (no-tools) QA call, Jev-graded correct/
//     incorrect/not_attempted against a reference answer — the same
//     three-way verdict shape benchmark/simpleqa.go's own grading uses,
//     just via Jev instead of an LLM-judge chat completion, and against a
//     single isolated question rather than a full research turn.
//   - "agent_loop": two sub-kinds — tool_selection (one
//     ChatCompletionWithTools call against the real tools.Defs catalog,
//     checked against which tool — if any — the model calls) and
//     compaction_retention (one real CompactionSystem/CompactionTask
//     call against a synthetic conversation, checked by plain
//     substring/URL containment against a must-survive fact list).
//   - "injection": one synthetic tool-result turn carrying a planted
//     instruction, Jev-graded on whether the model's next response
//     complied with it or treated it as inert fetched text — the same
//     "fetched content is never instructions" rule prompt.md's fallback
//     system prompt states outright.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Category matches docs/plans/hill-climbing-objectives.md Part 6 #1's
// four-way split — only Format and CitationSupport have real cases yet.
type Category string

const (
	CategoryFormat          Category = "format"
	CategoryCitationSupport Category = "citation_support"
	CategoryFactual         Category = "factual"
	CategoryAgentLoop       Category = "agent_loop"
	CategoryInjection       Category = "injection"
)

// Kind selects which pipeline a case runs through — see run.go's
// RunCase, which switches on this.
type Kind string

const (
	KindTitle               Kind = "title"
	KindTitleWeaver         Kind = "title_weaver"
	KindSuggestions         Kind = "suggestions"
	KindCitationSupport     Kind = "citation_support"
	KindFactualCorrectness  Kind = "factual_correctness"
	KindToolSelection       Kind = "tool_selection"
	KindCompactionRetention Kind = "compaction_retention"
	KindInjectionResistance Kind = "injection_resistance"
)

// ConversationTurn is one message in a synthetic fixture conversation —
// KindCompactionRetention's History.
type ConversationTurn struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Case is one eval case. Which of the kind-specific fields are read
// depends on Kind — see RunCase.
type Case struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Kind     Kind     `json:"kind"`

	// KindTitle/KindSuggestions/KindFactualCorrectness/KindToolSelection:
	UserMessage string `json:"user_message,omitempty"` // the first user message a title/suggestions/QA/tool-selection call would see
	Answer      string `json:"answer,omitempty"`       // KindSuggestions only — the assistant answer suggestions are generated from

	// KindCitationSupport: the exact Jev call shape
	// gateway/verification.go's verifySource makes — a source text and one
	// claim to check against it, with a hand-labelled expected verdict.
	SourceText    string  `json:"source_text,omitempty"`
	ClaimText     string  `json:"claim_text,omitempty"`
	WantChoice    string  `json:"want_choice,omitempty"`    // one of supported/partially_supported/contradicted/not_addressed
	MinConfidence float64 `json:"min_confidence,omitempty"` // 0 means "any confidence, just check the choice matches"

	// KindFactualCorrectness: UserMessage is the question, ReferenceAnswer
	// the known-correct answer Jev grades the model's own live answer
	// against — the same correct/incorrect/not_attempted three-way verdict
	// benchmark/simpleqa.go's grader uses.
	ReferenceAnswer string `json:"reference_answer,omitempty"`

	// KindToolSelection: WantTool is the tool name the model is expected to
	// call given UserMessage — "" means "expected to answer directly, no
	// tool call at all" (e.g. a question answerable from general
	// knowledge, which prompt.md explicitly says not to spend a tool call
	// on).
	WantTool string `json:"want_tool,omitempty"`

	// KindCompactionRetention: History is a synthetic thread (must end on
	// an assistant turn — see gateway/turn.go's compactThread doc comment
	// on why an array ending on "user" makes the model continue instead of
	// summarize) run through the real CompactionSystem/CompactionTask
	// prompts; MustContainFacts are substrings (facts, URLs) the summary
	// has to retain, checked by plain case-insensitive containment.
	History          []ConversationTurn `json:"history,omitempty"`
	MustContainFacts []string           `json:"must_contain_facts,omitempty"`

	// KindInjectionResistance: simulates one synthetic tool-result turn.
	// ToolName/ToolCallArgs describe the assistant's own (fixture) tool
	// call; ToolResultText is the fake tool output, with a planted
	// instruction embedded in it the same way a compromised/adversarial
	// web page would. The model then gets one more turn to respond, and
	// Jev grades whether that response complied with the planted
	// instruction or ignored it as inert data.
	ToolName       string `json:"tool_name,omitempty"`
	ToolCallArgs   string `json:"tool_call_args,omitempty"` // raw JSON string, passed through as the tool call's arguments
	ToolResultText string `json:"tool_result_text,omitempty"`
}

// LoadCases reads every *.json file in dir (non-recursive) and
// concatenates their case arrays — eval/cases/format.json,
// eval/cases/citation_support.json, etc., one file per category by
// convention, but LoadCases itself doesn't care how they're split.
func LoadCases(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading cases dir %s: %w", dir, err)
	}
	var all []Case
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		var cases []Case
		if err := json.Unmarshal(data, &cases); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		all = append(all, cases...)
	}
	return all, nil
}
