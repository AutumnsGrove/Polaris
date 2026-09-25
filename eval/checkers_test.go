package eval

import "testing"

func TestCheckTitleFormat(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"good title", "Water Boiling Point at Sea Level", true},
		{"quoted title", `"Koala Facts"`, true},
		{"empty", "", false},
		{"single word", "Koalas", false},
		{"too many words", "This Is Way Too Many Words For A Short Title Honestly", false},
		{"reads like an answer", "Yes, the boiling point is 100C", false},
		{"trailing period", "Water Boiling Point.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := CheckTitleFormat(c.title)
			if ok != c.want {
				t.Errorf("CheckTitleFormat(%q) = %v (%s), want %v", c.title, ok, reason, c.want)
			}
		})
	}
}

func TestCheckSuggestionsFormat(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"three good lines", "What caused this?\nHow does it compare?\nVisualize this as a chart?", true},
		{"numbered list", "1. What caused this?\n2. How does it compare?\n3. Anything else?", true},
		{"only two lines", "What caused this?\nHow does it compare?", false},
		{"missing question mark", "What caused this?\nHow does it compare\nAnything else?", false},
		{"continuation sentence not a question", "The model was never open-sourced, but the architecture was published.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := CheckSuggestionsFormat(c.raw)
			if ok != c.want {
				t.Errorf("CheckSuggestionsFormat(%q) = %v (%s), want %v", c.raw, ok, reason, c.want)
			}
		})
	}
}
