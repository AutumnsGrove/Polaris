package eval

import "testing"

func FuzzCheckTitleFormat(f *testing.F) {
	f.Add("Go fuzzing basics")
	f.Add("\"Quoted title here\"")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if ok, reason := CheckTitleFormat(raw); !ok && reason == "" {
			t.Errorf("failure with no reason for %q", raw)
		}
	})
}

func FuzzCheckSuggestionsFormat(f *testing.F) {
	f.Add("1. Why?\n2. How?\n3. When?")
	f.Add("- a?\n- b?")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if ok, reason := CheckSuggestionsFormat(raw); !ok && reason == "" {
			t.Errorf("failure with no reason for %q", raw)
		}
	})
}
