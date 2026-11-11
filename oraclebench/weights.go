package oraclebench

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"polaris/prompts"
)

//go:embed weights.yaml
var defaultWeightsYAML []byte

// CheckWeights is one check's scoring weights. Credit is optional: nil means
// "same as FN" (see weights.yaml).
type CheckWeights struct {
	FP     float64  `yaml:"fp"`
	FN     float64  `yaml:"fn"`
	Credit *float64 `yaml:"credit,omitempty"`
}

// Weights is weights.yaml: every judgment call about what matters in the score.
type Weights struct {
	Alpha              float64                 `yaml:"alpha"`
	LatencyBudget      time.Duration           `yaml:"latency_budget"`
	RefusalMaxRate     float64                 `yaml:"refusal_max_rate"`
	DangerousFPMaxRate float64                 `yaml:"dangerous_fp_max_rate"`
	DangerousChecks    []string                `yaml:"dangerous_checks"`
	FocusActionCredit  float64                 `yaml:"focus_action_credit"`
	Default            CheckWeights            `yaml:"default"`
	Checks             map[string]CheckWeights `yaml:"checks"`

	// Hash identifies the exact weights a run was scored with, so the tracking
	// DB can refuse to compare scores made under different files.
	Hash string `yaml:"-"`
}

// For returns the weights for a check or chip key; every chip_* key shares the
// "chips" entry, and anything unlisted falls back to Default.
func (w Weights) For(key string) CheckWeights {
	if strings.HasPrefix(key, ChipPrefix) {
		key = "chips"
	}
	if cw, ok := w.Checks[key]; ok {
		return cw
	}
	return w.Default
}

// CreditFor is what a perfect answer to a labeled check earns.
func (w Weights) CreditFor(key string) float64 {
	cw := w.For(key)
	if cw.Credit != nil {
		return *cw.Credit
	}
	return cw.FN
}

// IsDangerous reports whether a false nudge from this check counts toward the
// dangerous-FP disqualifier.
func (w Weights) IsDangerous(key string) bool { return contains(w.DangerousChecks, key) }

// DefaultWeights returns the embedded weights.yaml.
func DefaultWeights() (Weights, error) { return parseWeights(defaultWeightsYAML) }

// LoadWeights reads a weights file from disk; "" means the embedded default.
func LoadWeights(path string) (Weights, error) {
	if path == "" {
		return DefaultWeights()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Weights{}, fmt.Errorf("reading weights %s: %w", path, err)
	}
	w, err := parseWeights(data)
	if err != nil {
		return Weights{}, fmt.Errorf("%s: %w", path, err)
	}
	return w, nil
}

func parseWeights(data []byte) (Weights, error) {
	var w Weights
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&w); err != nil {
		return Weights{}, fmt.Errorf("parsing weights: %w", err)
	}
	if err := w.validate(prompts.Get()); err != nil {
		return Weights{}, err
	}
	sum := sha256.Sum256(data)
	w.Hash = hex.EncodeToString(sum[:])[:12]
	return w, nil
}

func (w Weights) validate(p *prompts.Set) error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if w.Alpha < 0 || w.Alpha > 1 {
		bad("alpha %v must be between 0 and 1", w.Alpha)
	}
	if w.LatencyBudget <= 0 {
		bad("latency_budget must be a positive duration like 5s")
	}
	for name, v := range map[string]float64{"refusal_max_rate": w.RefusalMaxRate, "dangerous_fp_max_rate": w.DangerousFPMaxRate} {
		if v < 0 || v > 1 {
			bad("%s %v must be between 0 and 1", name, v)
		}
	}
	if w.FocusActionCredit < 0 {
		bad("focus_action_credit must not be negative")
	}
	check := func(where string, cw CheckWeights) {
		if cw.FP < 0 || cw.FN < 0 || (cw.Credit != nil && *cw.Credit < 0) {
			bad("%s: weights must not be negative", where)
		}
	}
	check("default", w.Default)
	for key, cw := range w.Checks {
		// "chips" is the shared entry for every chip_* key, not a check.
		if key != "chips" && Options(p, key) == nil {
			bad("checks.%s: no such check", key)
		}
		check("checks."+key, cw)
	}
	for _, key := range w.DangerousChecks {
		if Options(p, key) == nil {
			bad("dangerous_checks has %q, which is no such check", key)
		}
	}
	return errors.Join(errs...)
}
