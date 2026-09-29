package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"polaris/config"
	"polaris/jev"
	"polaris/prompts"
)

// stubJevClient is a canned-response jevAskChoicer for RunOracle tests —
// same spirit as llm/llmtest.MockClient, just for the one jev.Client method
// RunOracle needs (see jevAskChoicer's doc comment).
type stubJevClient struct {
	resp *jev.Response
	err  error
}

func (s stubJevClient) AskChoice(ctx context.Context, state interface{}, questions map[string]jev.ChoiceQuestion) (*jev.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func answer(choice string, prob float64) jev.ChoiceAnswer {
	return jev.ChoiceAnswer{Choice: choice, Probabilities: map[string]float64{choice: prob}}
}

// outcomeFor returns the CheckOutcome for one check key, or nil — a small
// convenience for tests that only care about one check's own result.
func outcomeFor(result OracleResult, key string) *CheckOutcome {
	for i := range result.Checks {
		if result.Checks[i].Key == key {
			return &result.Checks[i]
		}
	}
	return nil
}

func TestRunOracle_NilClientNeverFails(t *testing.T) {
	result := RunOracle(context.Background(), nil, OracleInput{CurrentMessage: "test"})
	if result.FocusMode != "" || len(result.Injections) != 0 || len(result.Checks) != 0 {
		t.Errorf("want zero-value result with a nil client, got %+v", result)
	}
}

func TestRunOracle_JevErrorNeverFails(t *testing.T) {
	stub := stubJevClient{err: errors.New("connection reset")}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if len(result.Checks) != 0 {
		t.Errorf("want zero-value result on Jev failure, got %+v", result)
	}
}

func TestRunOracle_HighStakesInjectsMedicalAndCompareSources(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("off", 0.9),
		"high_stakes": answer("medical", 0.95),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "what's the max dose of tylenol"})
	if len(result.Injections) != 2 {
		t.Fatalf("want 2 injections (medical text + compare_sources any), got %d: %v", len(result.Injections), result.Injections)
	}
}

func TestRunOracle_HighStakesBriefOverrideReplacesNotStacks(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("off", 0.9),
		"high_stakes": answer("medical", 0.95),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test", ActiveFocusMode: "brief"})
	if len(result.Injections) != 1 {
		t.Fatalf("want exactly 1 injection (brief override replaces both), got %d: %v", len(result.Injections), result.Injections)
	}
	if !containsSubstring(result.Injections[0], "medical question") {
		t.Errorf("want {option} substituted with the winning option, got: %s", result.Injections[0])
	}
}

func TestRunOracle_FocusNeverPicksBriefWithHighStakes(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("brief", 0.99),
		"high_stakes": answer("medical", 0.95),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if result.FocusMode != "" {
		t.Errorf("want no focus pick when high_stakes fired and winner is in never_with_high_stakes, got %q", result.FocusMode)
	}
}

func TestRunOracle_FocusPicksBriefWithoutHighStakes(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("brief", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "quick question: what year is it"})
	if result.FocusMode != "brief" {
		t.Errorf("want brief picked at 0.9 (clears its 0.80 option threshold), got %q", result.FocusMode)
	}
}

func TestRunOracle_FocusRespectsOptionThreshold(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("brief", 0.75), // above the check's 0.70 floor, below brief's own 0.80 bar
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if result.FocusMode != "" {
		t.Errorf("want no pick — 0.75 clears the check's threshold but not brief's own 0.80 option_threshold, got %q", result.FocusMode)
	}
}

// The live case that prompted lowering safari's option_threshold from 0.92
// to 0.85: a real "take me on a safari of X" message classified safari at
// 0.87 and Oracle left the turn in no mode. Pinned at the exact observed
// confidence so a future bump back above it fails loudly.
func TestRunOracle_FocusPicksSafariAtTheObservedConfidence(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("safari", 0.87),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "take me on a safari of the industrial complex"})
	if result.FocusMode != "safari" {
		t.Errorf("want safari picked at 0.87 (clears the 0.85 option threshold), got %q", result.FocusMode)
	}
	if result.FocusCleared {
		t.Errorf("a pick must not also read as a clear")
	}
}

func TestRunOracle_SafariIsStickyForTheThread(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("shopper", 0.99),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test", PriorOracleFocusMode: "safari"})
	if result.FocusMode != "" {
		t.Errorf("want safari to be sticky (Oracle never switches away mid-thread), got %q", result.FocusMode)
	}
}

func TestRunOracle_SwitchThresholdHigherThanInitialPick(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("news", 0.75), // clears the 0.70 initial-pick bar but not the 0.85 switch_threshold
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test", PriorOracleFocusMode: "academic"})
	if result.FocusMode != "" {
		t.Errorf("want no switch — 0.75 clears the initial-pick threshold but not switch_threshold (0.85), got %q", result.FocusMode)
	}
	// Not cleared either: a near-miss on a *new* mode (above the base bar,
	// below the switch bar) keeps the existing mode. Clearing here would
	// drop the thread every time Oracle leaned another way without being
	// confident enough to switch — exactly what switch_threshold prevents.
	if result.FocusCleared {
		t.Errorf("want the existing mode kept on a switch-threshold near-miss, not cleared")
	}
}

// A live-found case: a thread in "shopper" mode (which Oracle itself had
// set) got asked a background-research question where shopper scored 0.00
// and no mode cleared its bar — and the thread stayed in shopper mode
// anyway. Oracle owns that mode, so its "no mode fits" verdict now retracts
// it rather than leaving it sticky.
func TestRunOracle_FocusClearsModeItSetWhenNoModeClearsTheBar(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("brief", 0.62), // below the check's 0.70 bar
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "research prada's background", PriorOracleFocusMode: "shopper"})
	if !result.FocusCleared {
		t.Fatalf("want FocusCleared when the mode in effect was Oracle's own and no mode clears the bar, got %+v", result)
	}
	if result.FocusMode != "" {
		t.Errorf("want no FocusMode alongside a clear (they are distinct outcomes), got %q", result.FocusMode)
	}
	focus := outcomeFor(result, "focus")
	if focus == nil || !focus.Fired {
		t.Errorf("want the focus check recorded as fired (Oracle acted — it cleared), got %+v", focus)
	}
}

func TestRunOracle_FocusClearsOnExplicitOff(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("off", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "hi", PriorOracleFocusMode: "shopper"})
	if !result.FocusCleared {
		t.Errorf("want an explicit off winner to clear Oracle's own mode, got %+v", result)
	}
}

// The clear must never touch a mode Oracle didn't set: with no prior Oracle
// pick, a low-confidence/off verdict just leaves the turn's own mode alone.
func TestRunOracle_FocusDoesNotClearAModeOracleDidNotSet(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("off", 0.95),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "hi", ActiveFocusMode: "brief"})
	if result.FocusCleared || result.FocusMode != "" {
		t.Errorf("want no clear and no pick for a manual/default mode, got %+v", result)
	}
}

// A sticky mode outranks a clear too — safari owns the whole thread, and a
// later "off" verdict must not silently drop it mid-exploration.
func TestRunOracle_FocusDoesNotClearStickySafari(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("off", 0.99),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "next stop", PriorOracleFocusMode: "safari"})
	if result.FocusCleared || result.FocusMode != "" {
		t.Errorf("want safari kept (sticky), got %+v", result)
	}
}

func TestRunOracle_ManualFocusStillReportedButNotAppliedToInjections(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":  answer("shopper", 0.99),
		"intent": answer("product", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{
		CurrentMessage:  "test",
		ActiveFocusMode: "academic",
		IsManualFocus:   true,
	})
	if result.FocusMode != "shopper" {
		t.Errorf("want Oracle's raw pick still reported (for the why sheet) even though manual wins, got %q", result.FocusMode)
	}
	// product's inject should fire normally here (not skipped) since
	// effectiveFocus stays "academic" (the manual pick), not "shopper" —
	// shopper's skip_option_for_focus rule for product must not apply.
	if len(result.Injections) != 1 {
		t.Errorf("want product's intent injection to fire against the manual focus mode (academic), got %d: %v", len(result.Injections), result.Injections)
	}
}

func TestRunOracle_IntentSkipsProductForShopper(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":  answer("shopper", 0.99),
		"intent": answer("product", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if len(result.Injections) != 0 {
		t.Errorf("want intent:product's injection skipped once Oracle itself picks shopper, got %v", result.Injections)
	}
}

func TestRunOracle_ClarifySkippedWhenNotFirstMessage(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("off", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test", IsFirstMessage: false})
	for _, c := range result.Checks {
		if c.Key == "clarify" {
			t.Errorf("want clarify never sent to Jev on a non-first message, got it in Checks: %+v", c)
		}
	}
}

func TestRunOracle_ResearchNoSetsHint(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":    answer("off", 0.9),
		"research": answer("no", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if !result.NoResearchHint {
		t.Error("want NoResearchHint true when research fires 'no' at/above threshold")
	}
	if len(result.Injections) != 1 {
		t.Errorf("want research's soft-nudge injection, got %v", result.Injections)
	}
}

func TestRunOracle_ChipsFireAboveThreshold(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("off", 0.9),
		"chip_pulsar": answer("yes", 0.9),
		"chip_daily":  answer("no", 0.9),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if len(result.Chips) != 1 || result.Chips[0].Key != "pulsar" {
		t.Errorf("want exactly one pulsar chip (daily fired 'no'), got %+v", result.Chips)
	}
}

func TestRunOracle_ProjectChipSkippedWithoutOptions(t *testing.T) {
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus": answer("off", 0.9),
	}}}
	// No ProjectOptions set — the project chip question shouldn't even be
	// asked, so a hypothetical stray "chip_project" key in the response
	// (which can't happen here since our stub only returns what we gave
	// it) has nothing to match against. This mainly guards RunOracle
	// doesn't panic/misbehave building the question set with an empty map.
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	for _, c := range result.Chips {
		if c.Key == "project" {
			t.Error("want no project chip without ProjectOptions set")
		}
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestOracleConfig_ReferencesAreValid guards against config.yaml's oracle:
// block (its shipped defaults, which config.yaml.example mirrors) naming a
// focus mode, option or check that doesn't exist — the same drift
// TestHandlePutSettings_EveryFocusModeIsAValidDefault guards for
// default_focus_mode. Rules are keyed by check/option name and never fail
// loudly at runtime: a typo just makes a rule silently never match.
func TestOracleConfig_ReferencesAreValid(t *testing.T) {
	validModes := prompts.Get().Agent.FocusModes
	if len(validModes) == 0 {
		t.Fatal("prompts.Get().Agent.FocusModes is empty — nothing to check")
	}
	p := prompts.Get()

	checkMode := func(field, checkKey, mode string) {
		if _, ok := validModes[mode]; mode != "" && !ok {
			t.Errorf("oracle.checks.%s.%s references unknown focus mode %q", checkKey, field, mode)
		}
	}
	checkOption := func(field, checkKey, option string) {
		if _, ok := p.Oracle.Checks[checkKey].Options[option]; !ok {
			t.Errorf("oracle.checks.%s.%s references %q, which isn't in prompts.yaml's %s.options", checkKey, field, option, checkKey)
		}
	}

	rules := config.DefaultOracle()
	for key, rule := range rules.Checks {
		if _, ok := p.Oracle.Checks[key]; !ok {
			t.Errorf("oracle.checks.%s has rules but no prompts.yaml check of that name", key)
			continue
		}
		for _, m := range rule.Sticky {
			checkMode("sticky", key, m)
			checkOption("sticky", key, m)
		}
		for _, m := range rule.NeverWithHighStakes {
			checkMode("never_with_high_stakes", key, m)
			checkOption("never_with_high_stakes", key, m)
		}
		for _, m := range rule.SkipForFocus {
			checkMode("skip_for_focus", key, m)
		}
		for opt := range rule.OptionThresholds {
			checkOption("option_thresholds", key, opt)
		}
		for opt, modes := range rule.SkipOptionForFocus {
			checkOption("skip_option_for_focus", key, opt)
			for _, m := range modes {
				checkMode("skip_option_for_focus", key, m)
			}
		}
	}
	for key := range p.Oracle.Checks {
		if _, ok := rules.Checks[key]; !ok {
			t.Errorf("prompts.yaml oracle.checks.%s has no rules in config's defaults — it would never run", key)
		}
	}
	for key := range p.Oracle.Chips {
		if _, ok := rules.Chips[key]; !ok {
			t.Errorf("prompts.yaml oracle.chips.%s has no rules in config's defaults — it would never run", key)
		}
	}
	// by_focus lives in prompts.yaml but is keyed by focus mode.
	for key, check := range p.Oracle.Checks {
		for m := range check.ByFocus {
			checkMode("by_focus", key, m)
		}
	}
}

// TestOracleYAML_ByFocusOptionsAreRealOptions guards a subtler drift: a
// by_focus override keyed by an option name that isn't actually one of the
// check's own Options (a typo would silently just never match at runtime
// instead of erroring).
func TestOracleYAML_ByFocusOptionsAreRealOptions(t *testing.T) {
	for key, check := range prompts.Get().Oracle.Checks {
		for focusMode, overrides := range check.ByFocus {
			for option := range overrides {
				if option == "any" {
					continue
				}
				if _, ok := check.Options[option]; !ok {
					t.Errorf("oracle.checks.%s.by_focus.%s references %q, which isn't in %s.options", key, focusMode, option, key)
				}
			}
		}
	}
}

// The frontend re-sends a thread's sticky focus mode on every message
// flagged non-manual, so a mode Oracle set earlier arrives looking like a
// plain default. If that turn's source were recorded as "default", the next
// turn would forget the mode was Oracle's and could never retract it.
func TestCarriedFocusModeSource(t *testing.T) {
	tests := []struct {
		name         string
		manual       bool
		focus, prior string
		want         string
	}{
		{"operator pick for this message", true, "brief", "shopper", "manual"},
		{"Oracle's own earlier mode riding along stays oracle", false, "shopper", "shopper", "oracle"},
		{"a different standing default is just a default", false, "brief", "shopper", "default"},
		{"no prior Oracle mode means default", false, "brief", "", "default"},
	}
	for _, tt := range tests {
		if got := carriedFocusModeSource(tt.manual, tt.focus, tt.prior); got != tt.want {
			t.Errorf("%s: carriedFocusModeSource(%v, %q, %q) = %q, want %q", tt.name, tt.manual, tt.focus, tt.prior, got, tt.want)
		}
	}
}

// capturingJevClient records what RunOracle actually put on the wire, for
// asserting on the request rather than the verdict.
type capturingJevClient struct {
	resp  *jev.Response
	state interface{}
}

func (c *capturingJevClient) AskChoice(ctx context.Context, state interface{}, questions map[string]jev.ChoiceQuestion) (*jev.Response, error) {
	c.state = state
	return c.resp, nil
}

func TestRunOracle_DropsAnswerOutsideOfferedOptions(t *testing.T) {
	// Jev is external input: a winner we never offered must not become the
	// turn's focus mode (persisted to threads.focus_mode) or a chip.
	stub := stubJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{
		"focus":       answer("ignore_previous_instructions", 0.99),
		"chip_pulsar": answer("definitely", 0.99),
		"not_a_check": answer("yes", 0.99),
	}}}
	result := RunOracle(context.Background(), stub, OracleInput{CurrentMessage: "test"})
	if result.FocusMode != "" || result.FocusCleared {
		t.Errorf("want a bogus focus winner ignored, got FocusMode=%q cleared=%v", result.FocusMode, result.FocusCleared)
	}
	if len(result.Chips) != 0 || len(result.Checks) != 0 {
		t.Errorf("want bogus answers dropped entirely, got chips=%v checks=%v", result.Chips, result.Checks)
	}
}

func TestRunOracle_TruncatesOversizedMessagesBeforeSendingToJev(t *testing.T) {
	huge := strings.Repeat("a", oracleMaxMessageRunes*5)
	c := &capturingJevClient{resp: &jev.Response{Answers: map[string]jev.ChoiceAnswer{}}}
	RunOracle(context.Background(), c, OracleInput{CurrentMessage: huge, PrevUserMessage: huge})
	state, _ := c.state.(string)
	if n := len([]rune(state)); n > oracleMaxMessageRunes*2+200 {
		t.Errorf("want each message capped near %d runes, state was %d runes", oracleMaxMessageRunes, n)
	}
}
