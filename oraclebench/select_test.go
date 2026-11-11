package oraclebench

import (
	"fmt"
	"reflect"
	"testing"

	"polaris/decisions"
)

func TestParseModelList(t *testing.T) {
	got := ParseModelList(" jev-1.13, ~jev-latest ,typesafe/jev-1.14,jev-1.13,,openai/gpt-6-luna-decisions ")
	want := []string{"typesafe/jev-1.13", "~typesafe/jev-latest", "typesafe/jev-1.14", "openai/gpt-6-luna-decisions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseModelList = %v, want %v", got, want)
	}
	if len(ParseModelList("")) != 0 {
		t.Error("an empty list is empty")
	}
}

func TestEligibleDecisionModels(t *testing.T) {
	var catalog []decisions.DecisionModel
	for id, ctx := range map[string]int{"b/small": 4000, "a/big": 262000, "c/respan": 0, "d/edge": 8000} {
		catalog = append(catalog, decisions.DecisionModel{ID: id, ContextLength: ctx})
	}
	got := EligibleDecisionModels(catalog)
	// Too small to hold the ~4.8k-token question set is dropped; a reported 0
	// is kept so the probe can say why it fails.
	want := []string{"a/big", "c/respan", "d/edge"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EligibleDecisionModels = %v, want %v", got, want)
	}
}

func TestSampleCases(t *testing.T) {
	var cases []Case
	for i := 0; i < 50; i++ {
		cases = append(cases, Case{ID: fmt.Sprintf("c-%02d", i)})
	}
	a, b := SampleCases(cases, 10, 42), SampleCases(cases, 10, 42)
	if !reflect.DeepEqual(a, b) || len(a) != 10 {
		t.Fatalf("the same seed must give the same 10 cases: %v vs %v", a, b)
	}
	if reflect.DeepEqual(a, SampleCases(cases, 10, 43)) {
		t.Error("a different seed should give a different subset")
	}
	for i := 1; i < len(a); i++ {
		if a[i-1].ID >= a[i].ID {
			t.Errorf("sample must be ordered by id, got %v", a)
		}
	}
	if len(SampleCases(cases, 0, 1)) != 50 || len(SampleCases(cases, 99, 1)) != 50 {
		t.Error("n <= 0 or n >= len means the whole bank")
	}
	if cases[0].ID != "c-00" {
		t.Error("sampling must not reorder the caller's slice")
	}
}

func TestMaxQuestionsFor(t *testing.T) {
	for id, want := range map[string]int{
		"nace-ai/drex-v2.0": 16,
		"nace-ai/drex-max":  16,
		// v1.5 accepted all 23 questions in a single request (2026-10-25), so it must not be split.
		"nace-ai/drex-v1.5": 0,
		"typesafe/jev-1.14": 0,
	} {
		if got := MaxQuestionsFor(id); got != want {
			t.Errorf("MaxQuestionsFor(%q) = %d, want %d", id, got, want)
		}
	}
}
