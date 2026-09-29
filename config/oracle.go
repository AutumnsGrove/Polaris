package config

// oracle.go holds Oracle mode's tuning: the numbers and lists that decide
// *when* a check fires or a focus mode is allowed to change (docs/plans/
// oracle-mode.md). What Jev is asked and what gets injected into the
// system prompt stays in prompts.yaml — that's wording, this is policy.
// The split exists so prompts.yaml holds only text a person would edit as
// prose, and so a threshold can be retuned from config.yaml (hot-reloaded
// on every turn, see gateway.Server.liveConfig) without touching prompts.
//
// Every field is optional in config.yaml. Anything left out inherits from
// DefaultOracle, whose values are mirrored, with explanations, in
// config.yaml.example's oracle: block — TestExampleOracleMatchesDefaults
// fails if the two drift apart, so the example file is always an accurate
// picture of what an unconfigured install runs.

import "fmt"

// OracleConfig is config.yaml's oracle: block. Keys under Checks/Chips are
// the same keys prompts.yaml's oracle.checks/oracle.chips use.
type OracleConfig struct {
	Checks map[string]OracleCheckRules `yaml:"checks"`
	Chips  map[string]OracleChipRules  `yaml:"chips"`
}

// OracleCheckRules is one check's policy. A zero/nil field in config.yaml
// means "inherit the default" (see mergeOracle), which is why
// FirstMessageOnly is a pointer: a plain bool couldn't tell an explicit
// `first_message_only: false` from an absent line.
type OracleCheckRules struct {
	// Threshold is the minimum probability the winning option needs for
	// the check to count as fired.
	Threshold float64 `yaml:"threshold,omitempty"`
	// SwitchThreshold only applies to the focus check — the bar to change
	// a mode Oracle already set mid-thread, higher than Threshold.
	SwitchThreshold float64 `yaml:"switch_threshold,omitempty"`
	// OptionThresholds raises the bar for specific options above
	// Threshold (focus.brief/safari) — checked in addition to it.
	OptionThresholds map[string]float64 `yaml:"option_thresholds,omitempty"`
	// Sticky options are never switched away from (or cleared) once
	// Oracle has set them for a thread.
	Sticky []string `yaml:"sticky,omitempty"`
	// NeverWithHighStakes options are never picked on a turn where the
	// high_stakes check fired.
	NeverWithHighStakes []string `yaml:"never_with_high_stakes,omitempty"`
	// SkipForFocus skips the whole check while one of these focus modes
	// is active.
	SkipForFocus []string `yaml:"skip_for_focus,omitempty"`
	// FirstMessageOnly restricts the check to a thread's first message.
	FirstMessageOnly *bool `yaml:"first_message_only,omitempty"`
	// SkipOptionForFocus suppresses one option's injection under the
	// listed focus modes (intent.product under shopper).
	SkipOptionForFocus map[string][]string `yaml:"skip_option_for_focus,omitempty"`
}

// OnlyFirstMessage reports FirstMessageOnly with nil meaning false.
func (r OracleCheckRules) OnlyFirstMessage() bool {
	return r.FirstMessageOnly != nil && *r.FirstMessageOnly
}

// OracleChipRules is one offer chip's policy — just its firing bar.
type OracleChipRules struct {
	Threshold float64 `yaml:"threshold,omitempty"`
}

// DefaultOracle returns a fresh copy of the shipped Oracle tuning — a new
// value each call so a caller mutating its result can't corrupt the
// defaults every later Load merges against.
func DefaultOracle() OracleConfig {
	firstOnly := true
	return OracleConfig{
		Checks: map[string]OracleCheckRules{
			// Brief is the one mode that can make an answer worse if picked
			// wrongly, and Safari takes over the whole thread, so both need
			// a higher bar than the rest.
			"focus": {
				Threshold:           0.70,
				SwitchThreshold:     0.85,
				OptionThresholds:    map[string]float64{"brief": 0.80, "safari": 0.85},
				Sticky:              []string{"safari"},
				NeverWithHighStakes: []string{"brief"},
			},
			"research":    {Threshold: 0.85},
			"high_stakes": {Threshold: 0.75},
			// Shopper mode already carries its own product guidance.
			"intent": {Threshold: 0.65, SkipOptionForFocus: map[string][]string{"product": {"shopper"}}},
			// Safari's Embark step already asks its own clarifying question.
			"clarify": {Threshold: 0.85, FirstMessageOnly: &firstOnly, SkipForFocus: []string{"safari"}},
			"recall":  {Threshold: 0.80},
		},
		Chips: map[string]OracleChipRules{
			"pulsar": {Threshold: 0.80},
			"daily":  {Threshold: 0.80},
			"field":  {Threshold: 0.75},
		},
	}
}

// mergeOracle overlays what config.yaml set onto the defaults, field by
// field: a check the file only gives a threshold for keeps its default
// sticky/skip lists. A list written as an empty `[]` counts as set (nil
// means absent), so an operator can clear a default list on purpose.
// Thresholds outside (0, 1] are a typo, not a preference — an over-1 bar
// silently disables a check forever — so they warn and keep the default.
func mergeOracle(set OracleConfig) OracleConfig {
	out := DefaultOracle()

	for key, rules := range set.Checks {
		base, known := out.Checks[key]
		if !known {
			log.Warn("oracle.checks has a key with no matching check, ignoring", "key", key)
			continue
		}
		base.Threshold = pickThreshold(fmt.Sprintf("oracle.checks.%s.threshold", key), rules.Threshold, base.Threshold)
		base.SwitchThreshold = pickThreshold(fmt.Sprintf("oracle.checks.%s.switch_threshold", key), rules.SwitchThreshold, base.SwitchThreshold)
		if rules.OptionThresholds != nil {
			merged := map[string]float64{}
			for opt, def := range base.OptionThresholds {
				merged[opt] = def
			}
			for opt, v := range rules.OptionThresholds {
				merged[opt] = pickThreshold(fmt.Sprintf("oracle.checks.%s.option_thresholds.%s", key, opt), v, merged[opt])
			}
			base.OptionThresholds = merged
		}
		if rules.Sticky != nil {
			base.Sticky = rules.Sticky
		}
		if rules.NeverWithHighStakes != nil {
			base.NeverWithHighStakes = rules.NeverWithHighStakes
		}
		if rules.SkipForFocus != nil {
			base.SkipForFocus = rules.SkipForFocus
		}
		if rules.FirstMessageOnly != nil {
			base.FirstMessageOnly = rules.FirstMessageOnly
		}
		if rules.SkipOptionForFocus != nil {
			base.SkipOptionForFocus = rules.SkipOptionForFocus
		}
		out.Checks[key] = base
	}

	for key, rules := range set.Chips {
		base, known := out.Chips[key]
		if !known {
			log.Warn("oracle.chips has a key with no matching chip, ignoring", "key", key)
			continue
		}
		base.Threshold = pickThreshold(fmt.Sprintf("oracle.chips.%s.threshold", key), rules.Threshold, base.Threshold)
		out.Chips[key] = base
	}
	return out
}

// pickThreshold returns configured when it's a usable probability, def when
// it's unset (0), and def with a warning when it's out of range.
func pickThreshold(path string, configured, def float64) float64 {
	switch {
	case configured == 0:
		return def
	case configured < 0 || configured > 1:
		log.Warn("threshold must be between 0 and 1, using the default", "path", path, "configured", configured, "default", def)
		return def
	}
	return configured
}
