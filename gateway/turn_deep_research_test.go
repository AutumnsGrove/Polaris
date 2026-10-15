package gateway

import "testing"

func TestAwaitingResearchPlan(t *testing.T) {
	plan := `{"question":"Run it?","options":["Run it"],"plan":{"sub_agent_objectives":["a","b"]}}`
	plain := `{"question":"Which Paris?","options":["France","Texas"]}`
	promoted := `{"deep_research":true,"checks":[{"key":"deep_research","winner":"yes","fired":true}]}`
	cases := []struct {
		name, pending, oracle string
		want                  bool
	}{
		{"structured plan", plan, "", true},
		{"structured plan, no oracle (manual toggle later switched off)", plan, `{}`, true},
		{"plain question after an oracle promotion (plan left in prose)", plain, promoted, true},
		{"plain clarifying question, not promoted", plain, `{}`, false},
		{"promoted turn that finished with an answer", "", promoted, false},
		{"no previous question", "", "", false},
		{"corrupt pending json", "{", promoted, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := awaitingResearchPlan(c.pending, c.oracle); got != c.want {
				t.Errorf("awaitingResearchPlan = %v, want %v", got, c.want)
			}
		})
	}
}

// A carried-only result (Oracle off, or it never answered) must still be
// persisted and sent, or the reply could never say why it ran as Deep Research.
func TestOracleResultHasContent(t *testing.T) {
	if (OracleResult{}).hasContent() {
		t.Error("empty result should have no content")
	}
	if !(OracleResult{DeepResearchCarried: true}).hasContent() {
		t.Error("carried-only result should have content")
	}
	if !(OracleResult{Checks: []CheckOutcome{{Key: "focus"}}}).hasContent() {
		t.Error("a result with checks should have content")
	}
}
