// Package oraclebench is the home-built benchmark that compares decision
// models against the Oracle-mode task (docs/plans/oracle-benchmark.md). This
// file is the case bank's schema, loader, and validator; scoring and the
// runner build on top of it.
//
// Cases are one YAML file each under cases/<track>/<id>.yaml, same convention
// as eval/cases and for the same reasons (readable block scalars for pasted
// text, one diffable unit per case). Unlike eval, the loader is strict:
//
//   - Files decode with KnownFields(true), so a typo'd key (`acept:`) fails
//     loudly instead of silently weakening a label.
//   - Every check name and answer is validated against prompts.yaml's real
//     oracle: options, so a renamed or removed option turns the bank red
//     instead of leaving labels that can no longer be matched.
//
// "Fired" in this package means "a nudge was delivered", not
// gateway.CheckOutcome.Fired. RunOracle sets Fired for any winner clearing
// the check's bar — including quiet options, so high_stakes=none at 0.9 is
// Fired with nothing injected. A label is therefore expected to produce a nudge
// exactly when its answer has inject text in prompts.yaml (see ExpectsNudge),
// which is why the schema has no hand-written must_fire flag to contradict it.
package oraclebench

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"polaris/prompts"
)

// Tracks are the reporting buckets (the plan's coverage table). A case may
// label checks from other tracks too; Track only decides which row it
// counts toward in the leaderboard.
var Tracks = []string{
	"focus", "high_stakes", "emotional", "premise", "private_person", "research",
	"intent", "clarify", "format", "ui", "depth", "recency", "source_type",
	"contested", "claim_check", "locale", "task", "recall", "chips",
}

// ChipPrefix is how chips are keyed in expect/quiet, matching the question
// names RunOracle sends Jev ("chip_pulsar", ...).
const ChipPrefix = "chip_"

// DeepResearchKey is the check whose "yes" is an action rather than injected
// text; see ExpectsNudge.
const DeepResearchKey = "deep_research"

// FixtureFields is the Field list every bench case runs against, so the
// field chip is asked (RunOracle skips it with no fields) and chip_field
// labels have a closed answer set. Names are deliberately distinct in topic
// so a clear positive and a clear negative both exist.
var FixtureFields = map[string]string{
	"Home Lab":      "Self-hosting, servers, networking, Docker, and homelab projects.",
	"Cooking":       "Recipes, techniques, meal planning, and kitchen equipment.",
	"Trip Planning": "Upcoming travel: destinations, itineraries, flights, and lodging.",
	"Health":        "Personal health, fitness, sleep, and medical questions.",
}

// Case is one bench case, loaded from its own YAML file.
type Case struct {
	ID string `yaml:"id" json:"id"`
	// Track is the reporting bucket; see Tracks.
	Track string `yaml:"track" json:"track"`
	// Rationale is required: why the labels are right. It is what the judge
	// sees later and what a reviewer checks a label against.
	Rationale string `yaml:"rationale" json:"rationale"`
	State     State  `yaml:"state" json:"state"`
	// Expect labels the checks this case stresses (1-3 typically), keyed by
	// check name or "chip_<name>".
	Expect map[string]Expectation `yaml:"expect" json:"expect"`
	// Quiet lists checks that must NOT deliver a nudge (chips: must not be
	// offered). Only list a check a human can confidently call quiet for
	// this message; anything unlisted and unlabeled is unscored.
	Quiet []string `yaml:"quiet,omitempty" json:"quiet,omitempty"`
	// DisputedLabel marks a case whose label reasonable people would split
	// on. It is still scored, but reported separately and always routed to
	// the judge, so it can neither sink nor lift the headline. Widening
	// accept is optional: on a binary check it would make the label a no-op.
	DisputedLabel bool `yaml:"disputed_label,omitempty" json:"disputed_label,omitempty"`
}

// State is the conversation state handed to RunOracle.
type State struct {
	// Latest is the message being classified.
	Latest string `yaml:"latest" json:"latest"`
	// Previous is the previous user message; "" means this is the thread's
	// first message (OracleInput.IsFirstMessage is derived from it, not
	// stored, so the two can't disagree).
	Previous string `yaml:"previous,omitempty" json:"previous,omitempty"`
	// PriorOracleFocus is a focus mode Oracle itself set earlier in the
	// thread, for sticky/switch cases. Requires Previous.
	PriorOracleFocus string `yaml:"prior_oracle_focus,omitempty" json:"prior_oracle_focus,omitempty"`
	// ThreadSource defaults to "web". "pulsar" / "pulsar-daily" withhold
	// every chip.
	ThreadSource string `yaml:"thread_source,omitempty" json:"thread_source,omitempty"`
}

// IsFirst reports whether this is the thread's first message.
func (s State) IsFirst() bool { return s.Previous == "" }

// Expectation is one check's label.
type Expectation struct {
	// Answer is the single labeled option.
	Answer string `yaml:"answer" json:"answer"`
	// Accept widens Answer to a defensible set. When set it must include
	// Answer. Omitted means only Answer counts.
	Accept []string `yaml:"accept,omitempty" json:"accept,omitempty"`
}

// Acceptable returns the set of options that count as correct.
func (e Expectation) Acceptable() []string {
	if len(e.Accept) == 0 {
		return []string{e.Answer}
	}
	return e.Accept
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Options returns the answer set for a check or chip key, or nil if the key
// names nothing the bench can ask. chip_field's options are the fixture
// fields plus "none", matching how RunOracle builds that question.
func Options(p *prompts.Set, key string) []string {
	var opts map[string]string
	if name, isChip := strings.CutPrefix(key, ChipPrefix); isChip {
		if name == "field" {
			out := []string{"none"}
			for n := range FixtureFields {
				out = append(out, n)
			}
			sort.Strings(out)
			return out
		}
		chip, ok := p.Oracle.Chips[name]
		if !ok {
			return nil
		}
		opts = chip.Options
	} else {
		check, ok := p.Oracle.Checks[key]
		if !ok {
			return nil
		}
		opts = check.Options
	}
	out := make([]string, 0, len(opts))
	for o := range opts {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// ExpectsNudge reports whether a correct answer to this check should deliver
// a nudge: the option has inject text. Chips never inject; for them an
// offer is expected unless the answer is the quiet one ("no"/"none").
func ExpectsNudge(p *prompts.Set, key, answer string) bool {
	if strings.HasPrefix(key, ChipPrefix) {
		return answer != "no" && answer != "none"
	}
	// deep_research has no inject text: a "yes" promotes the turn to Deep
	// Research (result.DeepResearch), an action that spends sub-agent money.
	// That action is what a label is expected to produce or not produce.
	if key == DeepResearchKey {
		return answer == "yes"
	}
	check, ok := p.Oracle.Checks[key]
	if !ok {
		return false
	}
	_, has := check.Inject[answer]
	return has
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Validate checks a case against the schema rules and prompts.yaml's real
// options. It returns every problem found, joined, so an author fixes a file
// in one pass rather than one error at a time.
func (c Case) Validate(p *prompts.Set) error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !idPattern.MatchString(c.ID) {
		bad("id %q must be lowercase kebab-case (a-z, 0-9, hyphens)", c.ID)
	}
	if !contains(Tracks, c.Track) {
		bad("track %q is not one of %v", c.Track, Tracks)
	}
	if strings.TrimSpace(c.Rationale) == "" {
		bad("rationale is required")
	}
	if strings.TrimSpace(c.State.Latest) == "" {
		bad("state.latest is required")
	}
	switch c.State.ThreadSource {
	case "", "web", "pulsar", "pulsar-daily":
	default:
		bad("state.thread_source %q must be web, pulsar, or pulsar-daily", c.State.ThreadSource)
	}
	if pf := c.State.PriorOracleFocus; pf != "" {
		if c.State.IsFirst() {
			bad("state.prior_oracle_focus needs state.previous: a first message has no earlier Oracle pick")
		}
		if pf == "off" || !contains(Options(p, "focus"), pf) {
			bad("state.prior_oracle_focus %q must be a real focus mode other than off", pf)
		}
	}
	chipsWithheld := c.State.ThreadSource == "pulsar" || c.State.ThreadSource == "pulsar-daily"

	if len(c.Expect) == 0 {
		bad("expect must label at least one check")
	}
	keys := make([]string, 0, len(c.Expect))
	for k := range c.Expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		e := c.Expect[key]
		opts := Options(p, key)
		if opts == nil {
			bad("expect.%s: no such check or chip", key)
			continue
		}
		if !contains(opts, e.Answer) {
			bad("expect.%s.answer %q is not one of %v", key, e.Answer, opts)
		}
		for _, a := range e.Accept {
			if !contains(opts, a) {
				bad("expect.%s.accept has %q, not one of %v", key, a, opts)
			}
		}
		if len(e.Accept) > 0 && !contains(e.Accept, e.Answer) {
			bad("expect.%s.accept must include the answer %q", key, e.Answer)
		}
		if key == "clarify" && !c.State.IsFirst() {
			bad("expect.clarify: clarify is only asked on a thread's first message; label it nowhere mid-thread")
		}
		if strings.HasPrefix(key, ChipPrefix) && chipsWithheld && ExpectsNudge(p, key, e.Answer) {
			bad("expect.%s: thread_source %q withholds every chip, so an offer can never be expected", key, c.State.ThreadSource)
		}
	}
	for _, key := range c.Quiet {
		if Options(p, key) == nil {
			bad("quiet has %q, which is no such check or chip", key)
		}
		if _, dup := c.Expect[key]; dup {
			bad("%q is in both expect and quiet", key)
		}
		if key == "clarify" && !c.State.IsFirst() {
			bad("quiet has clarify, but clarify is never asked mid-thread")
		}
		// focus never injects, so "no nudge" says nothing about it; the
		// quiet-focus label is an explicit expect.focus.answer: "off".
		if key == "focus" {
			bad(`quiet can't hold focus: label expect.focus.answer "off" instead`)
		}
	}
	if len(c.Quiet) != len(dedupe(c.Quiet)) {
		bad("quiet lists a check more than once")
	}
	return errors.Join(errs...)
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// LoadCases walks dir recursively, strictly decodes every *.yaml/*.yml file
// as one Case, and validates each against prompts.yaml. A file's id must
// match its filename, and ids must be unique across the bank. Cases come
// back sorted by id so runs are reproducible.
func LoadCases(dir string) ([]Case, error) {
	p := prompts.Get()
	var all []Case
	seen := map[string]string{}
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
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var c Case
		if err := dec.Decode(&c); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		// One case per file: a stray second document would otherwise be
		// ignored without a word.
		if err := dec.Decode(&yaml.Node{}); err != io.EOF {
			return fmt.Errorf("%s: holds more than one YAML document", path)
		}
		if stem := strings.TrimSuffix(d.Name(), ext); stem != c.ID {
			return fmt.Errorf("%s: id %q must match the filename", path, c.ID)
		}
		if err := c.Validate(p); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if prev, dup := seen[c.ID]; dup {
			return fmt.Errorf("%s: duplicate id %q (also in %s)", path, c.ID, prev)
		}
		seen[c.ID] = path
		all = append(all, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading cases from %s: %w", dir, err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}
