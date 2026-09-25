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
// "factual" (full correctness grading) and "agent_loop"/"injection"
// (tool-selection and injection-resistance fixtures) are deferred — they
// need either a running agent.Run turn or a more involved fixture shape
// than a single prompt/Jev call, and are follow-on work once this
// narrower slice is proven out.
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
	KindTitle           Kind = "title"
	KindTitleWeaver     Kind = "title_weaver"
	KindSuggestions     Kind = "suggestions"
	KindCitationSupport Kind = "citation_support"
)

// Case is one eval case. Which of the kind-specific fields are read
// depends on Kind — see RunCase.
type Case struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Kind     Kind     `json:"kind"`

	// KindTitle/KindSuggestions:
	UserMessage string `json:"user_message,omitempty"` // the first user message a title/suggestions call would see
	Answer      string `json:"answer,omitempty"`       // KindSuggestions only — the assistant answer suggestions are generated from

	// KindCitationSupport: the exact Jev call shape
	// gateway/verification.go's verifySource makes — a source text and one
	// claim to check against it, with a hand-labelled expected verdict.
	SourceText    string  `json:"source_text,omitempty"`
	ClaimText     string  `json:"claim_text,omitempty"`
	WantChoice    string  `json:"want_choice,omitempty"`    // one of supported/partially_supported/contradicted/not_addressed
	MinConfidence float64 `json:"min_confidence,omitempty"` // 0 means "any confidence, just check the choice matches"
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
