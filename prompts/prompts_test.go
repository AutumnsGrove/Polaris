package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// withPromptsFile points path at a temp file for the duration of one test
// and resets the package cache, so each test starts from a clean slate
// regardless of what earlier tests (or a real prompts.yaml in the working
// directory) loaded.
func withPromptsFile(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "prompts.yaml")
	if contents != "" {
		if err := os.WriteFile(dest, []byte(contents), 0o644); err != nil {
			t.Fatalf("writing test prompts.yaml: %v", err)
		}
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(orig)
		mu.Lock()
		cached = nil
		mu.Unlock()
	})

	mu.Lock()
	cached = nil
	mu.Unlock()
}

func TestGet_MissingFileFallsBackToDefaults(t *testing.T) {
	withPromptsFile(t, "")

	got := Get()
	if got.Agent.VoiceModeInstruction != defaults.Agent.VoiceModeInstruction {
		t.Errorf("VoiceModeInstruction = %q, want the built-in default", got.Agent.VoiceModeInstruction)
	}
	if got.Vision.DescribeImage != defaults.Vision.DescribeImage {
		t.Errorf("DescribeImage = %q, want the built-in default", got.Vision.DescribeImage)
	}
}

// wizardKinds is every target the wizard system must know how to interview
// for — kept literal here (not derived from defaults) so a target dropped
// from buildDefaults fails these tests instead of silently shrinking the
// set they check.
var wizardKinds = []string{"pulsar_routine", "pulsar_daily_block", "pulsar_daily_custom_block", "field_instructions"}

// TestGet_WizardMissingFileFallsBackToDefaults guards a real bug (from
// before the wizard was consolidated): fillDefaults never merged the
// wizard's prompts at all, so a missing or corrupted prompts.yaml sent
// gateway/wizard.go's interview an empty system prompt instead of
// degrading to the built-in default like every other prompt does.
func TestGet_WizardMissingFileFallsBackToDefaults(t *testing.T) {
	withPromptsFile(t, "")

	got := Get()
	if got.Wizard.Contract == "" || got.Wizard.Revision == "" {
		t.Errorf("Wizard.Contract/Revision empty on a missing prompts.yaml: %q / %q", got.Wizard.Contract, got.Wizard.Revision)
	}
	for _, kind := range wizardKinds {
		if !got.HasWizardTarget(kind) {
			t.Errorf("HasWizardTarget(%q) = false, want true", kind)
			continue
		}
		if got.WizardSystem(kind, "X") == "" {
			t.Errorf("WizardSystem(%q) is empty", kind)
		}
		if got.WizardOpenerTask(kind) == "" {
			t.Errorf("WizardOpenerTask(%q) is empty", kind)
		}
	}
	if got.HasWizardTarget("nonsense") {
		t.Error("HasWizardTarget(\"nonsense\") = true, want false")
	}
}

// TestGet_WizardPartialOverrideKeepsOtherFields: a prompts.yaml that only
// overrides one target's finish must keep the built-in text for that
// target's other fields and for every other target — the per-field merge
// fillWizardDefaults does, since a whole-struct "is it zero" check on a
// map value would drop the rest of the target.
func TestGet_WizardPartialOverrideKeepsOtherFields(t *testing.T) {
	withPromptsFile(t, "wizard:\n  targets:\n    field_instructions:\n      finish: CUSTOM FINISH\n")

	got := Get()
	sys := got.WizardSystem("field_instructions", "Trip")
	if !strings.Contains(sys, "CUSTOM FINISH") {
		t.Errorf("override not applied: %q", sys)
	}
	if !strings.Contains(sys, `Field named "Trip"`) {
		t.Errorf("built-in intro (with {label} filled) lost by a finish-only override: %q", sys)
	}
	if got.WizardOpenerTask("field_instructions") == "" {
		t.Error("field_instructions opener lost by a finish-only override")
	}
	if got.WizardSystem("pulsar_routine", "") == "" {
		t.Error("an untouched target lost its prompts")
	}
}

// TestWizardSystem_Assembly: the shared contract/revision appear exactly
// once in every target's prompt, {label} never leaks through, only the
// custom-block target carries a Guidance paragraph, and the tool name the
// prompts tell the model to call is the one the catalog actually offers.
func TestWizardSystem_Assembly(t *testing.T) {
	withPromptsFile(t, "")
	got := Get()

	for _, kind := range wizardKinds {
		sys := got.WizardSystem(kind, "Some Label")
		if strings.Count(sys, got.Wizard.Contract) != 1 || strings.Count(sys, got.Wizard.Revision) != 1 {
			t.Errorf("%s: shared contract/revision should each appear exactly once", kind)
		}
		if strings.Contains(sys, "{label}") {
			t.Errorf("%s: unsubstituted {label} in %q", kind, sys)
		}
		if !strings.Contains(sys, "finalize_wizard_prompt") || strings.Contains(sys, "finalize_pulsar_prompt") {
			t.Errorf("%s: must name finalize_wizard_prompt (the registered tool), not the old name", kind)
		}
	}
	if !strings.Contains(got.WizardSystem("pulsar_daily_custom_block", "B"), "steer the user toward ONE clear focus") {
		t.Error("custom block lost its guidance paragraph")
	}
	if strings.Contains(got.WizardSystem("pulsar_daily_block", "B"), "steer the user toward ONE clear focus") {
		t.Error("fixed block must not carry the custom block's guidance")
	}
	// A caller that forgot the label must not leak the placeholder.
	if s := got.WizardSystem("field_instructions", "  "); strings.Contains(s, "{label}") || !strings.Contains(s, "untitled") {
		t.Errorf("blank label not handled: %q", s)
	}
}

func TestGet_WeaverMissingFileFallsBackToDefaults(t *testing.T) {
	withPromptsFile(t, "")

	got := Get()
	if got.Weaver.System != defaults.Weaver.System {
		t.Errorf("Weaver.System = %q, want the built-in default", got.Weaver.System)
	}
	if got.Weaver.RevisitInstruction != defaults.Weaver.RevisitInstruction {
		t.Errorf("Weaver.RevisitInstruction = %q, want the built-in default", got.Weaver.RevisitInstruction)
	}
}

func TestGet_WeaverPartialOverrideFillsRestFromDefaults(t *testing.T) {
	withPromptsFile(t, `weaver:
  revisit_instruction: "custom revisit prompt %s"
`)

	got := Get()
	if got.Weaver.RevisitInstruction != "custom revisit prompt %s" {
		t.Errorf("Weaver.RevisitInstruction = %q, want the override", got.Weaver.RevisitInstruction)
	}
	if got.Weaver.System != defaults.Weaver.System {
		t.Errorf("Weaver.System = %q, want the built-in default (not overridden)", got.Weaver.System)
	}
}

func TestGet_RealPromptsYAML_WeaverSectionLoads(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(orig)
		mu.Lock()
		cached = nil
		mu.Unlock()
	})
	mu.Lock()
	cached = nil
	mu.Unlock()

	got := Get()
	if got.Weaver.System == "" {
		t.Error("real prompts.yaml's weaver.system loaded empty")
	}
	if got.Weaver.RevisitInstruction == "" {
		t.Error("real prompts.yaml's weaver.revisit_instruction loaded empty")
	}
}

// TestDefaults_MatchRealPromptsYAML guards against prompts.yaml and
// buildDefaults() drifting apart. prompts.yaml is edited directly (hot-
// reloaded, no rebuild) for day-to-day prompt tuning, so it's the one that
// actually reflects what's live — but buildDefaults() is what a corrupted,
// deleted, or pre-upgrade prompts.yaml falls back to, and it only gets
// touched by hand. A prompts.yaml edit that isn't mirrored into
// prompts.go leaves the fallback silently serving stale prompts. This
// found 10 real mismatches (fixed alongside adding this test) the first
// time it ran, including one field, the wizard's system prompt/opener, that
// fillDefaults never merged from defaults at all — a missing prompts.yaml
// would have sent that wizard call an empty system prompt.
func TestDefaults_MatchRealPromptsYAML(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(orig)
		mu.Lock()
		cached = nil
		mu.Unlock()
	})
	mu.Lock()
	cached = nil
	mu.Unlock()

	got := Get()
	diffStringFields(t, "Set", reflect.ValueOf(*got), reflect.ValueOf(defaults))
}

// diffStringFields walks two identically-shaped structs (string and
// map[string]string leaves only — everything prompts.Set actually
// contains) and reports every leaf where they disagree.
func diffStringFields(t *testing.T, path string, got, want reflect.Value) {
	t.Helper()
	switch got.Kind() {
	case reflect.Struct:
		for i := 0; i < got.NumField(); i++ {
			name := got.Type().Field(i).Name
			diffStringFields(t, path+"."+name, got.Field(i), want.Field(i))
		}
	case reflect.String:
		if got.String() != want.String() {
			t.Errorf("%s: prompts.yaml and buildDefaults() have drifted — update buildDefaults() to match the live prompts.yaml value:\n--- prompts.yaml ---\n%s\n--- buildDefaults() ---\n%s", path, got.String(), want.String())
		}
	case reflect.Map:
		for _, k := range got.MapKeys() {
			gv, wv := got.MapIndex(k), want.MapIndex(k)
			if !wv.IsValid() {
				t.Errorf("%s[%v]: present in prompts.yaml but missing from buildDefaults()", path, k)
				continue
			}
			// A struct-valued map (wizard.targets) has to be walked, not
			// compared: reflect's String() on a struct is just its type
			// name, so comparing it would report "equal" for any two
			// targets whatever their text — a drift check that can't fail.
			if gv.Kind() == reflect.Struct {
				diffStringFields(t, fmt.Sprintf("%s[%v]", path, k), gv, wv)
				continue
			}
			if gv.String() != wv.String() {
				t.Errorf("%s[%v]: prompts.yaml and buildDefaults() have drifted — update buildDefaults() to match the live prompts.yaml value:\n--- prompts.yaml ---\n%s\n--- buildDefaults() ---\n%s", path, k, gv.String(), wv.String())
			}
		}
	}
}

func TestGet_PartialOverrideFillsRestFromDefaults(t *testing.T) {
	withPromptsFile(t, `vision:
  describe_image: "custom image prompt"
`)

	got := Get()
	if got.Vision.DescribeImage != "custom image prompt" {
		t.Errorf("DescribeImage = %q, want the override", got.Vision.DescribeImage)
	}
	// Everything else in the file wasn't set — must still fall back, not
	// come back blank.
	if got.Agent.VoiceModeInstruction != defaults.Agent.VoiceModeInstruction {
		t.Errorf("VoiceModeInstruction = %q, want it to fall back to the default when unset", got.Agent.VoiceModeInstruction)
	}
	if got.Turn.TitleSystem != defaults.Turn.TitleSystem {
		t.Errorf("TitleSystem = %q, want it to fall back to the default when unset", got.Turn.TitleSystem)
	}
}

func TestGet_PartialFocusModeOverrideKeepsOtherModes(t *testing.T) {
	withPromptsFile(t, `agent:
  focus_modes:
    brief: "custom brief instruction"
`)

	got := Get()
	if got.Agent.FocusModes["brief"] != "custom brief instruction" {
		t.Errorf("FocusModes[brief] = %q, want the override", got.Agent.FocusModes["brief"])
	}
	if got.Agent.FocusModes["academic"] != defaults.Agent.FocusModes["academic"] {
		t.Errorf("FocusModes[academic] = %q, want it to fall back to the default", got.Agent.FocusModes["academic"])
	}
}

func TestGet_MalformedYAMLFallsBackToDefaults(t *testing.T) {
	withPromptsFile(t, "agent:\n  voice_mode_instruction: [this is not valid\n")

	got := Get()
	if got.Agent.VoiceModeInstruction != defaults.Agent.VoiceModeInstruction {
		t.Errorf("VoiceModeInstruction = %q, want the built-in default after a parse failure", got.Agent.VoiceModeInstruction)
	}
}

func TestGet_ReloadsAfterFileChanges(t *testing.T) {
	withPromptsFile(t, `vision:
  describe_image: "first version"
`)
	if got := Get().Vision.DescribeImage; got != "first version" {
		t.Fatalf("DescribeImage = %q, want %q", got, "first version")
	}

	// Rewrite with new content and a distinctly later mtime — Get should
	// pick up the change rather than serving the cached value forever.
	newContent := "vision:\n  describe_image: \"second version\"\n"
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		t.Fatalf("rewriting prompts.yaml: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if got := Get().Vision.DescribeImage; got != "second version" {
		t.Errorf("DescribeImage after reload = %q, want %q", got, "second version")
	}
}
