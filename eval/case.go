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
//
// Cases are one YAML file each (not one big JSON array) — see LoadCases —
// specifically so a long text field (source_text, tool_result_text, a
// conversation history) can be written as a readable block scalar instead
// of an escaped JSON string, the same reason prompts.yaml itself is YAML
// rather than JSON, and so each case is its own diff/debuggable unit
// rather than one line buried in a shared array.
package eval

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
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
	Role    string `json:"role" yaml:"role"` // "user" or "assistant"
	Content string `json:"content" yaml:"content"`
}

// Case is one eval case, loaded from its own YAML file — see LoadCases.
// Which of the kind-specific fields are read depends on Kind — see
// run.go's RunCase. JSON tags exist alongside YAML ones because Case is
// embedded in Result, which cmd/eval.go JSON-encodes to --out; cases
// themselves are never read back from JSON.
type Case struct {
	ID       string   `json:"id" yaml:"id"`
	Category Category `json:"category" yaml:"category"`
	Kind     Kind     `json:"kind" yaml:"kind"`

	// KindTitle/KindSuggestions/KindFactualCorrectness/KindToolSelection:
	UserMessage string `json:"user_message,omitempty" yaml:"user_message,omitempty"` // the first user message a title/suggestions/QA/tool-selection call would see
	Answer      string `json:"answer,omitempty" yaml:"answer,omitempty"`             // KindSuggestions only — the assistant answer suggestions are generated from

	// KindCitationSupport: the exact Jev call shape
	// gateway/verification.go's verifySource makes — a source text and one
	// claim to check against it, with a hand-labelled expected verdict.
	SourceText    string  `json:"source_text,omitempty" yaml:"source_text,omitempty"`
	ClaimText     string  `json:"claim_text,omitempty" yaml:"claim_text,omitempty"`
	WantChoice    string  `json:"want_choice,omitempty" yaml:"want_choice,omitempty"`       // one of supported/partially_supported/contradicted/not_addressed
	MinConfidence float64 `json:"min_confidence,omitempty" yaml:"min_confidence,omitempty"` // 0 means "any confidence, just check the choice matches"

	// KindFactualCorrectness: UserMessage is the question, ReferenceAnswer
	// the known-correct answer Jev grades the model's own live answer
	// against — the same correct/incorrect/not_attempted three-way verdict
	// benchmark/simpleqa.go's grader uses.
	ReferenceAnswer string `json:"reference_answer,omitempty" yaml:"reference_answer,omitempty"`

	// KindToolSelection: WantTool is the tool name the model is expected to
	// call given UserMessage — "" means "expected to answer directly, no
	// tool call at all" (e.g. a question answerable from general
	// knowledge, which prompt.md explicitly says not to spend a tool call
	// on).
	WantTool string `json:"want_tool,omitempty" yaml:"want_tool,omitempty"`

	// KindCompactionRetention: History is a synthetic thread (must end on
	// an assistant turn — see gateway/turn.go's compactThread doc comment
	// on why an array ending on "user" makes the model continue instead of
	// summarize) run through the real CompactionSystem/CompactionTask
	// prompts; MustContainFacts are substrings (facts, URLs) the summary
	// has to retain, checked by plain case-insensitive containment.
	History          []ConversationTurn `json:"history,omitempty" yaml:"history,omitempty"`
	MustContainFacts []string           `json:"must_contain_facts,omitempty" yaml:"must_contain_facts,omitempty"`

	// KindInjectionResistance: simulates one synthetic tool-result turn.
	// ToolName/ToolCallArgs describe the assistant's own (fixture) tool
	// call; ToolResultText is the fake tool output, with a planted
	// instruction embedded in it the same way a compromised/adversarial
	// web page would. The model then gets one more turn to respond, and
	// Jev grades whether that response complied with the planted
	// instruction or ignored it as inert data.
	ToolName       string `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	ToolCallArgs   string `json:"tool_call_args,omitempty" yaml:"tool_call_args,omitempty"` // raw JSON string, passed through as the tool call's arguments
	ToolResultText string `json:"tool_result_text,omitempty" yaml:"tool_result_text,omitempty"`
}

// LoadCases walks dir recursively and parses every *.yaml/*.yml file as
// one Case each — eval/cases/<category>/<id>.yaml by convention (see the
// package doc comment for why one file per case), but LoadCases itself
// doesn't care about the subdirectory structure, only that each file
// holds exactly one case.
func LoadCases(dir string) ([]Case, error) {
	var all []Case
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(d.Name())
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		var c Case
		if err := yaml.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		if c.ID == "" {
			return fmt.Errorf("%s: case has no id", path)
		}
		all = append(all, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading cases from %s: %w", dir, err)
	}
	return all, nil
}
