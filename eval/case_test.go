package eval

import "testing"

func TestLoadCases_RealFixtures(t *testing.T) {
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("loaded zero cases from eval/cases — expected the committed *.yaml fixtures under its category subdirectories")
	}

	seen := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" {
			t.Errorf("case with empty ID: %+v", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate case ID %q", c.ID)
		}
		seen[c.ID] = true
		if c.Category == "" {
			t.Errorf("case %q has no category", c.ID)
		}
		if c.Kind == "" {
			t.Errorf("case %q has no kind", c.ID)
		}
	}
}

func TestLoadCases_MissingDir(t *testing.T) {
	if _, err := LoadCases("does-not-exist"); err == nil {
		t.Error("LoadCases with a missing directory should return an error, not silently return zero cases")
	}
}
