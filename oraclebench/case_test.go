package oraclebench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polaris/prompts"
)

func writeCase(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadCases_RealBank is the gate on the committed bank: every case must
// parse strictly and validate against prompts.yaml's real options. It is what
// turns a renamed option into a red test instead of a silently stale label.
//
// ORACLEBENCH_CASES (comma-separated dirs, relative to this package) narrows
// the check to a subtree so someone authoring one track isn't blocked by a
// half-written file in another; unset, it covers the whole bank.
func TestLoadCases_RealBank(t *testing.T) {
	dirs := []string{"cases"}
	if env := os.Getenv("ORACLEBENCH_CASES"); env != "" {
		dirs = strings.Split(env, ",")
	}
	for _, dir := range dirs {
		cases, err := LoadCases(dir)
		if err != nil {
			t.Fatalf("LoadCases(%s): %v", dir, err)
		}
		if len(cases) == 0 {
			t.Fatalf("loaded zero cases from %s", dir)
		}
	}
}

func TestLoadCases_UnquotedYAML11BoolsStayStrings(t *testing.T) {
	// The spike's PyYAML builder read the focus key `off` as False. yaml.v3
	// into a string field keeps the literal text; pin that so a decoder
	// change can't quietly turn unquoted answers into booleans.
	dir := t.TempDir()
	writeCase(t, dir, "bool-words.yaml", `
id: bool-words
track: focus
rationale: unquoted yaml 1.1 booleans
state:
  latest: hello
expect:
  focus: {answer: off}
  premise: {answer: no}
  recall: {answer: yes}
`)
	cases, err := LoadCases(dir)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	e := cases[0].Expect
	if e["focus"].Answer != "off" || e["premise"].Answer != "no" || e["recall"].Answer != "yes" {
		t.Errorf("unquoted answers changed: %+v", e)
	}
}

func TestLoadCases_RejectsFileProblems(t *testing.T) {
	valid := `
id: ok
track: focus
rationale: r
state: {latest: hi}
expect: {focus: {answer: "off"}}
`
	tests := []struct {
		name, file, body, want string
	}{
		{"typo'd key", "ok.yaml", strings.Replace(valid, "rationale", "ratonale", 1), "ratonale"},
		{"id vs filename", "other.yaml", valid, "must match the filename"},
		{"two documents", "ok.yaml", valid + "---\nid: second\n", "more than one YAML document"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCase(t, dir, tc.file, tc.body)
			_, err := LoadCases(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadCases_DuplicateID(t *testing.T) {
	// Two files can't share an id and a filename at once, so duplicates are
	// only reachable across subdirectories.
	dir := t.TempDir()
	body := `
id: dup
track: focus
rationale: r
state: {latest: hi}
expect: {focus: {answer: "off"}}
`
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		writeCase(t, filepath.Join(dir, sub), "dup.yaml", body)
	}
	if _, err := LoadCases(dir); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("err = %v, want duplicate id", err)
	}
}

func TestLoadCases_MissingDir(t *testing.T) {
	if _, err := LoadCases("does-not-exist"); err == nil {
		t.Error("a missing directory should error, not load zero cases")
	}
}

func TestValidate(t *testing.T) {
	p := prompts.Get()
	base := func() Case {
		return Case{
			ID: "x", Track: "focus", Rationale: "r",
			State:  State{Latest: "hi"},
			Expect: map[string]Expectation{"high_stakes": {Answer: "none"}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Case)
		want   string // "" means valid
	}{
		{"valid", func(*Case) {}, ""},
		{"bad id", func(c *Case) { c.ID = "Not Kebab" }, "kebab-case"},
		{"bad track", func(c *Case) { c.Track = "vibes" }, "track"},
		{"no rationale", func(c *Case) { c.Rationale = " " }, "rationale"},
		{"no latest", func(c *Case) { c.State.Latest = "" }, "state.latest"},
		{"empty expect", func(c *Case) { c.Expect = nil }, "at least one"},
		{"unknown check", func(c *Case) { c.Expect = map[string]Expectation{"vibes": {Answer: "no"}} }, "no such check"},
		{"unknown option", func(c *Case) { c.Expect["high_stakes"] = Expectation{Answer: "scary"} }, "not one of"},
		{"accept outside options", func(c *Case) {
			c.Expect["high_stakes"] = Expectation{Answer: "none", Accept: []string{"none", "scary"}}
		}, "accept has"},
		{"accept omits answer", func(c *Case) {
			c.Expect["high_stakes"] = Expectation{Answer: "none", Accept: []string{"medical"}}
		}, "must include the answer"},
		{"widened accept", func(c *Case) {
			c.Expect["high_stakes"] = Expectation{Answer: "none", Accept: []string{"none", "medical"}}
		}, ""},
		{"unknown quiet", func(c *Case) { c.Quiet = []string{"vibes"} }, "no such check"},
		{"quiet and expect", func(c *Case) { c.Quiet = []string{"high_stakes"} }, "both expect and quiet"},
		{"quiet focus", func(c *Case) { c.Quiet = []string{"focus"} }, "quiet can't hold focus"},
		{"duplicate quiet", func(c *Case) { c.Quiet = []string{"emotional", "emotional"} }, "more than once"},
		{"clarify mid-thread", func(c *Case) {
			c.State.Previous = "earlier"
			c.Expect["clarify"] = Expectation{Answer: "no"}
		}, "first message"},
		{"clarify first message", func(c *Case) { c.Expect["clarify"] = Expectation{Answer: "yes"} }, ""},
		{"prior focus on first message", func(c *Case) { c.State.PriorOracleFocus = "safari" }, "needs state.previous"},
		{"prior focus off", func(c *Case) {
			c.State.Previous = "earlier"
			c.State.PriorOracleFocus = "off"
		}, "other than off"},
		{"prior focus ok", func(c *Case) {
			c.State.Previous = "earlier"
			c.State.PriorOracleFocus = "safari"
		}, ""},
		{"bad thread source", func(c *Case) { c.State.ThreadSource = "cron" }, "thread_source"},
		{"chip offer in pulsar thread", func(c *Case) {
			c.State.ThreadSource = "pulsar"
			c.Expect[ChipPrefix+"pulsar"] = Expectation{Answer: "yes"}
		}, "withholds every chip"},
		{"chip quiet answer in pulsar thread", func(c *Case) {
			c.State.ThreadSource = "pulsar"
			c.Expect[ChipPrefix+"pulsar"] = Expectation{Answer: "no"}
		}, ""},
		{"field chip fixture name", func(c *Case) { c.Expect["chip_field"] = Expectation{Answer: "Cooking"} }, ""},
		{"field chip none", func(c *Case) { c.Expect["chip_field"] = Expectation{Answer: "none"} }, ""},
		{"field chip unknown name", func(c *Case) { c.Expect["chip_field"] = Expectation{Answer: "Gardening"} }, "not one of"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate(p)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	c := Case{ID: "Bad ID", Track: "vibes", State: State{Latest: "hi"}, Expect: map[string]Expectation{"nope": {Answer: "x"}}}
	err := c.Validate(prompts.Get())
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"kebab-case", "track", "rationale", "no such check"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// TestExpectsNudge pins the plan's central scoring distinction: a quiet
// option winning is not a nudge, even though gateway.CheckOutcome.Fired is
// true for it.
func TestExpectsNudge(t *testing.T) {
	p := prompts.Get()
	tests := []struct {
		key, answer string
		want        bool
	}{
		{"high_stakes", "medical", true},
		{"high_stakes", "none", false},
		{"research", "no", true}, // the no-research hint is the nudge
		{"research", "yes", false},
		{"emotional", "yes", true},
		{"emotional", "no", false},
		{"intent", "general", false},
		{"intent", "weather", true},
		{"chip_pulsar", "yes", true},
		{"chip_pulsar", "no", false},
		{"chip_field", "Cooking", true},
		{"chip_field", "none", false},
	}
	for _, tc := range tests {
		if got := ExpectsNudge(p, tc.key, tc.answer); got != tc.want {
			t.Errorf("ExpectsNudge(%s, %s) = %v, want %v", tc.key, tc.answer, got, tc.want)
		}
	}
}

func TestEveryTrackNamesARealCheckOrChips(t *testing.T) {
	p := prompts.Get()
	for _, tr := range Tracks {
		if tr == "chips" {
			continue
		}
		if Options(p, tr) == nil {
			t.Errorf("track %q is neither a check nor the chips bucket", tr)
		}
	}
}
