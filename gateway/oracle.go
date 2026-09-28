package gateway

import (
	"context"
	"sort"
	"strings"
	"time"

	"polaris/jev"
	"polaris/prompts"
)

// oracleTimeout bounds RunOracle's own Jev call independently of
// jev.Client's 20s default. The live spike (docs/plans/oracle-mode.md's
// "Live spike results") measured real p95 10.2s and a 14% timeout/520
// rate against that 20s ceiling — nowhere near the vendor's claimed
// 70-500ms — and Oracle sits in front of the turn's first token, so it
// needs a much tighter budget than "eventually give up."
const oracleTimeout = 2500 * time.Millisecond

// jevAskChoicer is the one jev.Client method RunOracle needs — a seam so
// tests can inject a stub instead of a live *jev.Client, same spirit as
// llm/llmtest.MockClient elsewhere in this codebase. *jev.Client satisfies
// this without any change on its side.
type jevAskChoicer interface {
	AskChoice(ctx context.Context, state interface{}, questions map[string]jev.ChoiceQuestion) (*jev.Response, error)
}

// OracleInput is everything about this turn RunOracle can't determine on
// its own — see gateway/turn.go's call site for how each field is derived.
type OracleInput struct {
	CurrentMessage  string
	PrevUserMessage string // "" if none
	IsFirstMessage  bool
	// ActiveFocusMode is whatever focus mode is already decided for this
	// turn before Oracle runs — the operator's manual per-message pick if
	// IsManualFocus, otherwise the Settings default (or ""). Used for
	// by_focus/skip_for_focus/skip_option_for_focus lookups.
	ActiveFocusMode string
	// IsManualFocus means the operator picked ActiveFocusMode for this
	// specific message in the composer — "manual always wins" (see the
	// plan doc), so RunOracle still computes and reports its own focus
	// pick (for the "why" sheet) but never lets it become the turn's
	// effective mode. gateway/turn.go enforces the actual field write;
	// this only affects which mode injections are resolved against.
	IsManualFocus bool
	// PriorOracleFocusMode is non-empty only when a previous turn in this
	// same thread had Oracle itself pick the active focus mode (as
	// opposed to a manual/default pick) — used for Sticky and
	// SwitchThreshold. Empty means nothing to be sticky about or switch
	// away from.
	PriorOracleFocusMode string
	// ProjectOptions, if non-nil, enables the project chip — project name
	// -> description, built from the store's project list at request
	// time (prompts.yaml's project chip ships with no static options). A
	// nil map (no projects, or the thread is already in one) skips the
	// chip check entirely rather than asking Jev to choose among nothing.
	ProjectOptions map[string]string
}

// CheckOutcome is one check's raw result, kept regardless of whether it
// fired — the "why" sheet's collapsed list needs the ones that didn't
// fire too, not just the ones that did.
type CheckOutcome struct {
	Key           string             `json:"key"`
	Winner        string             `json:"winner"`
	Probabilities map[string]float64 `json:"probabilities"`
	Fired         bool               `json:"fired"`
	// Nudge is this specific check's own resolved injection text (see
	// resolveInjections), "" for a check with no inject map at all (focus
	// itself; a mode pick isn't a "nudge") or one that fired but this
	// option/focus combination has nothing to inject. Kept separate from
	// OracleResult.Injections (every fired check's text already flattened
	// into one ordered list for the system prompt's {items} substitution)
	// since the turn-info sheet needs to show which nudge came from which
	// check, not just the combined prompt text.
	Nudge string `json:"nudge,omitempty"`
}

// Chip is one offer surfaced under the reply — the frontend maps Key to
// its destination icon/verb (Pulsar/Daily/Project).
type Chip struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"` // the project name, only for Key=="project"
}

// OracleResult is RunOracle's whole verdict. It has no side effects —
// gateway/turn.go stays in control of actually applying FocusMode,
// NoResearchHint, and Injections rather than RunOracle mutating turn
// state itself, which is also what keeps this function unit-testable
// without a real turn to run.
type OracleResult struct {
	// FocusMode is Oracle's own pick, if any check fired one — always
	// populated for transparency even when IsManualFocus means
	// gateway/turn.go won't apply it as the turn's actual mode.
	FocusMode      string `json:"focus_mode,omitempty"`
	NoResearchHint bool   `json:"no_research_hint,omitempty"`
	// Injections is every fired check's resolved text, in the order the
	// ## Oracle section should list them — one paragraph each.
	Injections []string       `json:"injections,omitempty"`
	Checks     []CheckOutcome `json:"checks,omitempty"`
	Chips      []Chip         `json:"chips,omitempty"`
	CostUSD    float64        `json:"cost_usd,omitempty"`
}

// RunOracle fires every enabled check (plus chips) as one Jev AskChoice
// call and applies each answer's threshold/sticky/focus rules — see
// docs/plans/oracle-mode.md. Jev unconfigured, erroring, or timing out
// returns a zero-value OracleResult and a nil error: Oracle must never be
// a way for a turn to fail, so gateway/turn.go can treat every non-nil-
// error-free return here as "proceed exactly as Oracle-off" without
// inspecting anything.
func RunOracle(ctx context.Context, client jevAskChoicer, in OracleInput) (OracleResult, error) {
	if client == nil {
		return OracleResult{}, nil
	}

	p := prompts.Get()
	questions := make(map[string]jev.ChoiceQuestion, len(p.Oracle.Checks)+len(p.Oracle.Chips))
	for key, check := range p.Oracle.Checks {
		if check.FirstMessageOnly && !in.IsFirstMessage {
			continue
		}
		if sliceContains(check.SkipForFocus, in.ActiveFocusMode) {
			continue
		}
		questions[key] = jev.ChoiceQuestion{
			Instructions: p.Oracle.QuestionPreamble + " " + check.Instructions,
			Criteria:     check.Options,
		}
	}
	for key, chip := range p.Oracle.Chips {
		options := chip.Options
		if key == "project" {
			if len(in.ProjectOptions) == 0 {
				continue
			}
			options = make(map[string]string, len(in.ProjectOptions)+1)
			for name, desc := range in.ProjectOptions {
				options[name] = desc
			}
			options["none"] = "Doesn't clearly belong to any project."
		}
		questions["chip_"+key] = jev.ChoiceQuestion{
			Instructions: p.Oracle.QuestionPreamble + " " + chip.Instructions,
			Criteria:     options,
		}
	}
	if len(questions) == 0 {
		return OracleResult{}, nil
	}

	state := "Latest message: " + in.CurrentMessage
	if in.PrevUserMessage != "" {
		state = "Previous message: " + in.PrevUserMessage + "\n\nLatest message: " + in.CurrentMessage
	}

	callCtx, cancel := context.WithTimeout(ctx, oracleTimeout)
	defer cancel()
	resp, err := client.AskChoice(callCtx, state, questions)
	if err != nil {
		log.Warn("oracle: jev call failed, proceeding as Oracle-off", "err", err)
		return OracleResult{}, nil
	}

	result := OracleResult{CostUSD: resp.Usage.CostUSD}

	// high_stakes is resolved first (independent of focus) since the
	// focus check's own NeverWithHighStakes rule needs its winner.
	highStakesOption := ""
	if hs, ok := resp.Answers["high_stakes"]; ok {
		if check, ok := p.Oracle.Checks["high_stakes"]; ok && hs.Choice != "none" && hs.Probabilities[hs.Choice] >= check.Threshold {
			highStakesOption = hs.Choice
		}
	}

	// effectiveFocus is whichever mode this turn's answer will actually
	// be shaped by — starts at whatever was already decided (manual or
	// default) and only moves to Oracle's own pick when that pick isn't
	// overridden by a manual choice. Every other check's by_focus/
	// skip_option_for_focus lookup below uses this, not ActiveFocusMode
	// directly, so injections match reality even when Oracle picks a mode
	// itself.
	effectiveFocus := in.ActiveFocusMode
	if focusAns, ok := resp.Answers["focus"]; ok {
		if check, ok := p.Oracle.Checks["focus"]; ok {
			picked, fired := resolveFocus(check, focusAns, in.PriorOracleFocusMode, highStakesOption)
			result.Checks = append(result.Checks, CheckOutcome{Key: "focus", Winner: focusAns.Choice, Probabilities: focusAns.Probabilities, Fired: fired})
			if fired {
				result.FocusMode = picked
				if !in.IsManualFocus {
					effectiveFocus = picked
				}
			}
		}
	}

	keys := make([]string, 0, len(resp.Answers))
	for k := range resp.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if key == "focus" {
			continue
		}
		ans := resp.Answers[key]

		if chipKey, isChip := strings.CutPrefix(key, "chip_"); isChip {
			chip, ok := p.Oracle.Chips[chipKey]
			if !ok {
				continue
			}
			if ans.Choice != "no" && ans.Choice != "none" && ans.Probabilities[ans.Choice] >= chip.Threshold {
				label := ""
				if chipKey == "project" {
					label = ans.Choice
				}
				result.Chips = append(result.Chips, Chip{Key: chipKey, Label: label})
			}
			continue
		}

		check, ok := p.Oracle.Checks[key]
		if !ok {
			continue
		}
		fired := ans.Probabilities[ans.Choice] >= check.Threshold
		result.Checks = append(result.Checks, CheckOutcome{Key: key, Winner: ans.Choice, Probabilities: ans.Probabilities, Fired: fired})
		if !fired {
			continue
		}
		if key == "research" && ans.Choice == "no" {
			result.NoResearchHint = true
		}
		if sliceContains(check.SkipOptionForFocus[ans.Choice], effectiveFocus) {
			continue
		}
		injs := resolveInjections(check, ans.Choice, effectiveFocus)
		result.Injections = append(result.Injections, injs...)
		if len(injs) > 0 {
			result.Checks[len(result.Checks)-1].Nudge = strings.Join(injs, " ")
		}
	}

	// Checks was appended focus-first then in sorted-key order — re-sort
	// by key alone so the "why" sheet's listing is fully deterministic
	// regardless of Jev's answer-map iteration order.
	sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].Key < result.Checks[j].Key })

	return result, nil
}

// resolveFocus applies the focus check's sticky/never-with-high-stakes/
// option-threshold/switch-threshold rules to its raw Jev answer. Returns
// ("", false) whenever nothing should change — the caller decides what
// "nothing changes" means for the turn (keep the prior/default mode).
func resolveFocus(check prompts.OracleCheck, ans jev.ChoiceAnswer, priorOracleFocus, highStakesOption string) (picked string, fired bool) {
	if ans.Choice == "" || ans.Choice == "off" {
		return "", false
	}
	if highStakesOption != "" && sliceContains(check.NeverWithHighStakes, ans.Choice) {
		return "", false
	}
	if priorOracleFocus != "" && sliceContains(check.Sticky, priorOracleFocus) {
		// Already in a sticky mode for this thread — Oracle doesn't even
		// consider switching away from it.
		return "", false
	}

	bar := check.Threshold
	if t, ok := check.OptionThresholds[ans.Choice]; ok && t > bar {
		bar = t
	}
	if priorOracleFocus != "" && priorOracleFocus != ans.Choice && check.SwitchThreshold > bar {
		bar = check.SwitchThreshold
	}

	if ans.Probabilities[ans.Choice] < bar {
		return "", false
	}
	return ans.Choice, true
}

// resolveInjections returns the paragraph(s) a fired check's winning
// option contributes to the ## Oracle section, honoring ByFocus — which
// REPLACES the normal Inject text for that focus mode rather than
// stacking on top of it (see the plan doc's "A variant replaces the
// default text; it doesn't stack on top of it"). ByFocus is looked up by
// the winning option first, then the "any" sentinel (an override that
// applies no matter which option fired, e.g. high_stakes.by_focus.brief).
// Outside any ByFocus override, Inject["any"] (if present) is appended as
// its own second paragraph alongside the option's own text — e.g.
// high_stakes' compare_sources hint, which applies "in addition to
// whichever option fired," never as a replacement.
func resolveInjections(check prompts.OracleCheck, option, focusMode string) []string {
	if byFocus, ok := check.ByFocus[focusMode]; ok {
		if text, ok := byFocus[option]; ok {
			return []string{strings.ReplaceAll(text, "{option}", option)}
		}
		if text, ok := byFocus["any"]; ok {
			return []string{strings.ReplaceAll(text, "{option}", option)}
		}
	}
	text := check.Inject[option]
	if text == "" {
		return nil
	}
	out := []string{text}
	if any := check.Inject["any"]; any != "" {
		out = append(out, any)
	}
	return out
}

func sliceContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
