package gateway

import (
	"context"
	"sort"
	"strings"
	"time"

	"polaris/config"
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

// oracleMaxMessageRunes caps each message handed to Jev. Classification only
// needs the gist (the opening of a message carries its intent), and the
// request body is billed per input token and sent to a third party — a pasted
// document must not inflate cost/latency or leak wholesale to a classifier
// that can't use it. Same shape as pulsarSuggestMaxPerMessage.
const oracleMaxMessageRunes = 2000

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
	// FieldOptions, if non-nil, enables the field chip — field name
	// -> description, built from the store's field list at request
	// time (prompts.yaml's field chip ships with no static options). A
	// nil map (no fields, or the thread is already in one) skips the
	// chip check entirely rather than asking Jev to choose among nothing.
	FieldOptions map[string]string
	// Rules is config.yaml's oracle: block (thresholds, sticky/skip lists —
	// see config.OracleConfig), already merged with the shipped defaults by
	// config.Load. The zero value means "use the shipped defaults", so a
	// caller with no config in hand (tests) still gets real behavior.
	Rules config.OracleConfig
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
// its destination icon/verb (Pulsar/Daily/Field).
type Chip struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"` // the field name, only for Key=="field"
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
	FocusMode string `json:"focus_mode,omitempty"`
	// FocusCleared means Oracle retracted the focus mode it had itself set
	// for this thread: the focus check found no mode that clears its bar
	// this turn (an explicit "off" winner, or a winner below the check's
	// threshold), so gateway/turn.go runs the turn with no mode and clears
	// threads.focus_mode. Only ever about a mode Oracle set earlier — a
	// manual pick or the Settings default is left alone (see resolveFocus).
	// Distinct from FocusMode == "": that means "Oracle had no opinion".
	FocusCleared   bool `json:"focus_cleared,omitempty"`
	NoResearchHint bool `json:"no_research_hint,omitempty"`
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
// returns a zero-value OracleResult: Oracle must never be a way for a turn
// to fail, so there is deliberately no error return — gateway/turn.go
// treats an empty result as "proceed exactly as Oracle-off". The failure
// itself is logged here.
func RunOracle(ctx context.Context, client jevAskChoicer, in OracleInput) OracleResult {
	if client == nil {
		return OracleResult{}
	}

	p := prompts.Get()
	rules := in.Rules
	if rules.Checks == nil && rules.Chips == nil {
		rules = config.DefaultOracle()
	}
	questions := make(map[string]jev.ChoiceQuestion, len(p.Oracle.Checks)+len(p.Oracle.Chips))
	// offered is the option set actually sent per question, kept so Jev's
	// answers can be validated against it below — a response is external
	// input, and its Choice ends up persisted (threads.focus_mode) and
	// substituted into prompt text, so it must be one we offered.
	offered := make(map[string][]string, len(p.Oracle.Checks)+len(p.Oracle.Chips))
	for key, check := range p.Oracle.Checks {
		rule, ok := rules.Checks[key]
		if !ok {
			// A check defined in prompts.yaml with no policy in config: with
			// no threshold it would fire on any answer at all, so it stays
			// off until it has one.
			log.Warn("oracle: check has no rules in config, skipping it", "check", key)
			continue
		}
		if rule.OnlyFirstMessage() && !in.IsFirstMessage {
			continue
		}
		if sliceContains(rule.SkipForFocus, in.ActiveFocusMode) {
			continue
		}
		questions[key] = jev.ChoiceQuestion{
			Instructions: p.Oracle.QuestionPreamble + " " + check.Instructions,
			Criteria:     check.Options,
		}
		offered[key] = optionKeys(check.Options)
	}
	for key, chip := range p.Oracle.Chips {
		if _, ok := rules.Chips[key]; !ok {
			log.Warn("oracle: chip has no rules in config, skipping it", "chip", key)
			continue
		}
		options := chip.Options
		if key == "field" {
			if len(in.FieldOptions) == 0 {
				continue
			}
			options = make(map[string]string, len(in.FieldOptions)+1)
			for name, desc := range in.FieldOptions {
				options[name] = desc
			}
			options["none"] = "Doesn't clearly belong to any field."
		}
		questions["chip_"+key] = jev.ChoiceQuestion{
			Instructions: p.Oracle.QuestionPreamble + " " + chip.Instructions,
			Criteria:     options,
		}
		offered["chip_"+key] = optionKeys(options)
	}
	if len(questions) == 0 {
		return OracleResult{}
	}

	current := truncateRunes(in.CurrentMessage, oracleMaxMessageRunes)
	state := "Latest message: " + current
	if in.PrevUserMessage != "" {
		state = "Previous message: " + truncateRunes(in.PrevUserMessage, oracleMaxMessageRunes) + "\n\nLatest message: " + current
	}

	callCtx, cancel := context.WithTimeout(ctx, oracleTimeout)
	defer cancel()
	resp, err := client.AskChoice(callCtx, state, questions)
	if err != nil {
		log.Warn("oracle: jev call failed, proceeding as Oracle-off", "err", err)
		return OracleResult{}
	}

	result := OracleResult{CostUSD: resp.Usage.CostUSD}
	resp.Answers = validAnswers(resp.Answers, offered)

	// high_stakes is resolved first (independent of focus) since the
	// focus check's own NeverWithHighStakes rule needs its winner.
	highStakesOption := ""
	if hs, ok := resp.Answers["high_stakes"]; ok {
		if rule, ok := rules.Checks["high_stakes"]; ok && hs.Choice != "none" && hs.Probabilities[hs.Choice] >= rule.Threshold {
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
		if rule, ok := rules.Checks["focus"]; ok {
			picked, fired := resolveFocus(rule, focusAns, in.PriorOracleFocusMode, highStakesOption)
			result.Checks = append(result.Checks, CheckOutcome{Key: "focus", Winner: focusAns.Choice, Probabilities: focusAns.Probabilities, Fired: fired})
			if fired {
				// picked=="off" is resolveFocus's "retract the mode Oracle
				// itself set" outcome — recorded as its own flag rather than
				// folded into FocusMode so "" stays meaningfully "Oracle had
				// no opinion" everywhere else, and so the why-sheet can say
				// "cleared" instead of "no pick".
				if picked == "off" {
					result.FocusCleared = true
					if !in.IsManualFocus {
						effectiveFocus = ""
					}
				} else {
					result.FocusMode = picked
					if !in.IsManualFocus {
						effectiveFocus = picked
					}
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
			chipRule, ok := rules.Chips[chipKey]
			if !ok {
				continue
			}
			if ans.Choice != "no" && ans.Choice != "none" && ans.Probabilities[ans.Choice] >= chipRule.Threshold {
				label := ""
				if chipKey == "field" {
					label = ans.Choice
				}
				result.Chips = append(result.Chips, Chip{Key: chipKey, Label: label})
			}
			continue
		}

		check, ok := p.Oracle.Checks[key]
		rule, hasRule := rules.Checks[key]
		if !ok || !hasRule {
			continue
		}
		fired := ans.Probabilities[ans.Choice] >= rule.Threshold
		result.Checks = append(result.Checks, CheckOutcome{Key: key, Winner: ans.Choice, Probabilities: ans.Probabilities, Fired: fired})
		if !fired {
			continue
		}
		if key == "research" && ans.Choice == "no" {
			result.NoResearchHint = true
		}
		if sliceContains(rule.SkipOptionForFocus[ans.Choice], effectiveFocus) {
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

	return result
}

// resolveFocus applies the focus check's sticky/never-with-high-stakes/
// option-threshold/switch-threshold rules to its raw Jev answer, and
// decides when Oracle should *retract* a mode it set earlier.
//
// Returns (picked, fired):
//   - ("", false)   nothing changes (keep whatever mode the turn already has)
//   - (mode, true)  Oracle picked that mode
//   - ("off", true) Oracle cleared the mode it had set — see below
func resolveFocus(check config.OracleCheckRules, ans jev.ChoiceAnswer, priorOracleFocus, highStakesOption string) (picked string, fired bool) {
	if ans.Choice == "" {
		return "", false
	}
	if priorOracleFocus != "" && sliceContains(check.Sticky, priorOracleFocus) {
		// Already in a sticky mode for this thread — Oracle doesn't even
		// consider switching away from it, or clearing it (a sticky mode
		// like safari is meant to own the thread to the end).
		return "", false
	}

	// A winner that can't clear this check's own base bar means Oracle
	// isn't confident *any* mode fits this message, and "off" says so
	// outright. When the mode now in effect was Oracle's own earlier pick,
	// that verdict retracts it: leaving a stale mode on a message Oracle
	// doesn't think it suits is the bug this closes (live: a "shopper"
	// thread asked a background-research question, shopper scored 0.00,
	// and the thread stayed in shopper mode anyway).
	//
	// Note the bar compared here is the check's plain Threshold, NOT the
	// higher SwitchThreshold: a near-miss on a *new* mode (clears the base
	// bar but not the switch bar) keeps the existing mode on purpose —
	// clearing there would drop the thread to nothing every time Oracle
	// leaned a different way but not strongly enough to switch, which is
	// exactly what SwitchThreshold exists to avoid. Only a true "no mode
	// fits" (off, or below the base bar) retracts.
	if priorOracleFocus != "" &&
		(ans.Choice == "off" || ans.Probabilities[ans.Choice] < check.Threshold) {
		return "off", true
	}
	if ans.Choice == "off" {
		return "", false
	}
	if highStakesOption != "" && sliceContains(check.NeverWithHighStakes, ans.Choice) {
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
	if extra := check.Inject["any"]; extra != "" {
		out = append(out, extra)
	}
	return out
}

// optionKeys lists a question's option ids (its Criteria keys).
func optionKeys(options map[string]string) []string {
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	return keys
}

// validAnswers drops any answer Jev returned for a question we didn't ask, or
// whose Choice isn't one of the options we offered for it (logged, since a
// well-behaved Jev never does this). Without it a bogus "focus" winner would
// be written to threads.focus_mode and echoed into the ## Oracle section via
// {option}, and a bogus field chip winner would render as a chip label.
func validAnswers(answers map[string]jev.ChoiceAnswer, offered map[string][]string) map[string]jev.ChoiceAnswer {
	out := make(map[string]jev.ChoiceAnswer, len(answers))
	for key, ans := range answers {
		if !sliceContains(offered[key], ans.Choice) {
			log.Warn("oracle: dropping jev answer outside the offered options", "question", key, "choice", truncateRunes(ans.Choice, 60))
			continue
		}
		out[key] = ans
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
