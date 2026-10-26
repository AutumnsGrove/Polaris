package oraclebench

import (
	"math/rand"
	"sort"
	"strings"

	"polaris/decisions"
)

// modelAliases are short names for the models the plan compares most, so
// `--models jev-1.13,jev-1.14` works. Anything else is passed through as a
// catalog id, since the roster is queried live rather than hardcoded.
var modelAliases = map[string]string{
	"jev-1.13":    "typesafe/jev-1.13",
	"jev-1.14":    "typesafe/jev-1.14",
	"jev-latest":  "~typesafe/jev-latest",
	"~jev-latest": "~typesafe/jev-latest",
}

// ResolveModelID expands a short alias to a catalog id.
func ResolveModelID(s string) string {
	s = strings.TrimSpace(s)
	if id, ok := modelAliases[s]; ok {
		return id
	}
	return s
}

// ParseModelList splits a comma-separated --models value, resolving aliases and
// dropping blanks and duplicates while keeping order.
func ParseModelList(csv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		id := ResolveModelID(part)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// questionCaps lists models that reject a request over a fixed question count,
// so the bench splits Oracle's call for them (decisions.Client.WithMaxQuestions).
// Nace's Drex 2.0 and Max both 422 over 16 (measured 2026-10-25; 16 passes, 17
// fails). Drex 1.5 has no cap (accepted 23), so it deliberately isn't listed:
// it runs as one request like everything else. A capped model's score reflects
// the split, which the write-up must say.
var questionCaps = map[string]int{
	"nace-ai/drex-v2.0": 16,
	"nace-ai/drex-max":  16,
}

// MaxQuestionsFor returns the per-request question cap for a model, or 0 when
// it has none.
func MaxQuestionsFor(id string) int { return questionCaps[id] }

// minContext is the smallest context window worth trying. The full Oracle
// question set is ~4.8k input tokens (measured); anything under this can't hold
// it. A catalog entry reporting 0 (the respan family) is NOT filtered here: it
// is caught by the runner's probe, which learns the real reason.
const minContext = 8000

// EligibleDecisionModels turns the live catalog into the ids worth running.
func EligibleDecisionModels(catalog []decisions.DecisionModel) []string {
	var out []string
	for _, m := range catalog {
		if m.ContextLength > 0 && m.ContextLength < minContext {
			continue
		}
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out
}

// SampleCases picks n cases reproducibly: the same seed always yields the same
// subset, and every model in one invocation sees it, so the comparison is
// apples to apples. n <= 0 or n >= len means the whole bank. The result is
// ordered by id regardless of the shuffle.
func SampleCases(cases []Case, n int, seed int64) []Case {
	out := append([]Case(nil), cases...)
	if n > 0 && n < len(out) {
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		out = out[:n]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
