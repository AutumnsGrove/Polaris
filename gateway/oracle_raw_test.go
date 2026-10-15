package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"polaris/jev"
)

// blockingJevClient never answers until its context ends — the shape of a
// decision model that hangs, used to prove RunOracleRaw's timeout is the one
// the caller passed rather than the 2.5s production constant.
type blockingJevClient struct{}

func (blockingJevClient) AskChoice(ctx context.Context, _ interface{}, _ map[string]jev.ChoiceQuestion) (*jev.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunOracleRaw_ReportsTheFailureRunOracleSwallows(t *testing.T) {
	boom := errors.New("openai refused to answer question \"focus\"")
	stub := stubJevClient{err: boom}
	in := OracleInput{CurrentMessage: "test"}

	if _, err := RunOracleRaw(context.Background(), stub, in, time.Second); !errors.Is(err, boom) {
		t.Errorf("RunOracleRaw err = %v, want the client's error so the bench can record a refusal", err)
	}
	// The production wrapper must keep its never-fails contract.
	if got := RunOracle(context.Background(), stub, in); len(got.Checks) != 0 || len(got.Chips) != 0 {
		t.Errorf("RunOracle must still return a zero result on failure, got %+v", got)
	}
}

func TestRunOracleRaw_NilClientIsAnError(t *testing.T) {
	if _, err := RunOracleRaw(context.Background(), nil, OracleInput{CurrentMessage: "test"}, time.Second); err == nil {
		t.Error("a nil client is a bench wiring bug and must not look like a quiet Oracle result")
	}
}

func TestRunOracleRaw_UsesTheCallersTimeout(t *testing.T) {
	start := time.Now()
	_, err := RunOracleRaw(context.Background(), blockingJevClient{}, OracleInput{CurrentMessage: "test"}, 30*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v: the 30ms timeout was ignored", elapsed)
	}
}

// The reason RunOracleRaw exists as a thin sibling instead of a copy: given the
// same answers it must reach the same verdict RunOracle does.
func TestRunOracleRaw_MatchesRunOracleOnTheSameAnswers(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("researcher", 0.9),
		"high_stakes": answer("medical", 0.95),
		"research":    answer("yes", 0.9),
		"emotional":   answer("yes", 0.9),
		"format":      answer("table", 0.9),
		"chip_pulsar": answer("yes", 0.9),
	}}}
	in := OracleInput{CurrentMessage: "what's the max dose of tylenol", IsFirstMessage: true}

	want := RunOracle(context.Background(), stub, in)
	got, err := RunOracleRaw(context.Background(), stub, in, oracleTimeout)
	if err != nil {
		t.Fatalf("RunOracleRaw: %v", err)
	}
	// Compare the production-visible fields only; ChipOutcomes/ResolvedModel
	// are the additive bench fields.
	got.ChipOutcomes, got.ResolvedModel = nil, ""
	want.ChipOutcomes, want.ResolvedModel = nil, ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunOracleRaw diverged from RunOracle:\n raw: %+v\nprod: %+v", got, want)
	}
}

func TestRunOracle_ChipOutcomesKeepEveryRawChipAnswer(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("off", 0.9),
		"chip_pulsar": answer("yes", 0.9),  // offered
		"chip_daily":  answer("no", 0.9),   // quiet
		"chip_safari": answer("yes", 0.60), // yes, but under its 0.85 bar
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})

	if len(result.Chips) != 1 || result.Chips[0].Key != "pulsar" {
		t.Fatalf("Chips changed shape, want only pulsar: %+v", result.Chips)
	}
	byKey := map[string]CheckOutcome{}
	for _, o := range result.ChipOutcomes {
		byKey[o.Key] = o
	}
	for key, want := range map[string]struct {
		winner string
		fired  bool
	}{"pulsar": {"yes", true}, "daily": {"no", false}, "safari": {"yes", false}} {
		o, ok := byKey[key]
		if !ok {
			t.Errorf("ChipOutcomes is missing %q: a quiet chip must still be scorable", key)
			continue
		}
		if o.Winner != want.winner || o.Fired != want.fired {
			t.Errorf("chip %s = winner %q fired %v, want %q %v", key, o.Winner, o.Fired, want.winner, want.fired)
		}
	}
}

// A chip the model said yes to but production withheld keeps its raw answer —
// otherwise "chip must not be offered in a routine thread" couldn't tell a
// model that behaved from one the server gate rescued.
func TestRunOracle_ChipOutcomesSurviveWithholding(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("off", 0.9),
		"chip_pulsar": answer("yes", 0.95),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test", ThreadSource: "pulsar"})
	if len(result.Chips) != 0 {
		t.Fatalf("a pulsar thread must offer no chips, got %+v", result.Chips)
	}
	if len(result.ChipOutcomes) != 1 || result.ChipOutcomes[0].Winner != "yes" || result.ChipOutcomes[0].Fired {
		t.Errorf("want the raw yes recorded with Fired=false, got %+v", result.ChipOutcomes)
	}
}

func TestRunOracle_BenchOnlyFieldsStayOutOfThePersistedJSON(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{
		Model: "typesafe/jev-1.14-20261014",
		Answers: map[string]jev.ChoiceAnswer{
			"focus":       answer("off", 0.9),
			"chip_pulsar": answer("yes", 0.9),
		},
	}}
	result, err := RunOracleRaw(context.Background(), stub, OracleInput{CurrentMessage: "test"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResolvedModel != "typesafe/jev-1.14-20261014" {
		t.Errorf("ResolvedModel = %q, want the response's resolved build", result.ResolvedModel)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"ChipOutcomes", "ResolvedModel", "chip_outcomes", "resolved_model", "jev-1.14"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("persisted OracleResult JSON leaked %q: %s", leaked, raw)
		}
	}
}
