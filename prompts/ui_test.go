package prompts

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestUIFragment(t *testing.T) {
	s := &defaults

	for _, v := range []string{"off", "", "bogus", "LOW"} {
		if got := s.UIFragment(v); got != "" {
			t.Errorf("UIFragment(%q) = %q, want empty (blocks not offered)", v, got)
		}
	}

	low, normal := s.UIFragment("low"), s.UIFragment("normal")
	for name, got := range map[string]string{"low": low, "normal": normal} {
		if !strings.HasPrefix(got, s.UI.Base) {
			t.Errorf("%s: fragment should start with the shared grammar", name)
		}
	}
	if !strings.HasSuffix(low, s.UI.LowBar) || !strings.HasSuffix(normal, s.UI.NormalBar) {
		t.Error("each dial value should end with its own bar sentence")
	}
	if s.UI.LowBar == s.UI.NormalBar {
		t.Error("the two bars must differ, or the dial does nothing")
	}
}

// Every example line in the grammar is a fixed string the model copies, so it
// has to stay valid JSON — a typo here would teach every answer to emit a
// broken line.
var uiExample = regexp.MustCompile("`(\\{[^`]*\\})`")

func TestUIBase_ExamplesAreValidJSONAndCoverEveryBlock(t *testing.T) {
	examples := uiExample.FindAllStringSubmatch(defaults.UI.Base, -1)
	if len(examples) == 0 {
		t.Fatal("found no example lines in the base grammar")
	}
	seen := map[string]bool{}
	for _, m := range examples {
		var obj map[string]any
		if err := json.Unmarshal([]byte(m[1]), &obj); err != nil {
			t.Errorf("example %s is not valid JSON: %v", m[1], err)
		}
		if c, ok := obj["c"].(string); ok {
			seen[c] = true
		}
	}
	for _, c := range []string{"callout", "stat", "compare", "steps", "timeline", "checklist", "procon", "choose", "facts", "flow", "tabs", "disclose"} {
		if !seen[c] {
			t.Errorf("base grammar has no example opening a %q block", c)
		}
	}
}
