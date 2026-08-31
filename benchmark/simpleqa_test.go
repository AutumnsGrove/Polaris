package benchmark

import "strings"
import "testing"

func TestGradeSimpleQA(t *testing.T) {
	cases := []struct {
		name        string
		graderResp  string
		wantCorrect bool
		wantVerdict string
	}{
		{"correct letter", "A", true, "correct"},
		{"incorrect letter", "B", false, "incorrect"},
		{"not attempted letter", "C", false, "not_attempted"},
		{"letter embedded in prose", "The predicted answer matches the gold target. A", true, "correct"},
		{"unparseable defaults to not_attempted", "I refuse to grade this.", false, "not_attempted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			correct, verdict := GradeSimpleQA(tc.graderResp)
			if correct != tc.wantCorrect || verdict != tc.wantVerdict {
				t.Errorf("GradeSimpleQA(%q) = (%v, %q), want (%v, %q)", tc.graderResp, correct, verdict, tc.wantCorrect, tc.wantVerdict)
			}
		})
	}
}

func TestBuildSimpleQAGraderPrompt_PlacesFieldsInQuestionTargetPredictedOrder(t *testing.T) {
	prompt := BuildSimpleQAGraderPrompt("What city?", "San Francisco", "San Francisco, California")
	wantTail := "Question: What city?\nGold target: San Francisco, California\nPredicted answer: San Francisco\n"
	if !strings.Contains(prompt, wantTail) {
		t.Errorf("grader prompt missing expected question/target/predicted-answer block, want substring:\n%s", wantTail)
	}
}

func TestBuildSimpleQAQuery_DoesNotWrapQuestion(t *testing.T) {
	q := "What is the capital of France?"
	if got := BuildSimpleQAQuery(q); got != q {
		t.Errorf("BuildSimpleQAQuery(%q) = %q, want unwrapped question (unlike BrowseComp's QueryTemplate)", q, got)
	}
}
