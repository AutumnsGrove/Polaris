package prompts

import (
	"os"
	"path/filepath"
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
