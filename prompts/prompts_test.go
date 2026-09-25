package prompts

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestGet_PulsarWizardMissingFileFallsBackToDefaults guards a real bug:
// fillDefaults never merged PulsarWizard.System/OpenerTask at all, so a
// missing or corrupted prompts.yaml sent gateway/pulsar_wizard.go's
// interview an empty system prompt instead of degrading to the built-in
// default like every other prompt does.
func TestGet_PulsarWizardMissingFileFallsBackToDefaults(t *testing.T) {
	withPromptsFile(t, "")

	got := Get()
	if got.PulsarWizard.System == "" || got.PulsarWizard.System != defaults.PulsarWizard.System {
		t.Errorf("PulsarWizard.System = %q, want the built-in default", got.PulsarWizard.System)
	}
	if got.PulsarWizard.OpenerTask == "" || got.PulsarWizard.OpenerTask != defaults.PulsarWizard.OpenerTask {
		t.Errorf("PulsarWizard.OpenerTask = %q, want the built-in default", got.PulsarWizard.OpenerTask)
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
// time it ran, including one field, PulsarWizard.System/OpenerTask, that
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
