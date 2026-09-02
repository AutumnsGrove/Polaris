package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

var testRegistry = []ModelConfig{
	{
		ID:          "test-model",
		Name:        "Test Model",
		Model:       "test/model",
		Provider:    []string{"test"},
		Temperature: 0.4,
		MaxTokens:   1000,
	},
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeTestConfig(t, `openrouter:
  api_key: "sk-test"
`)

	cfg, err := Load(path, testRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Server.Port != 8899 {
		t.Errorf("Server.Port = %d, want 8899", cfg.Server.Port)
	}
	if cfg.OpenRouter.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("OpenRouter.BaseURL = %q, want the OpenRouter default", cfg.OpenRouter.BaseURL)
	}
	if cfg.Database.Path != "./polaris.db" {
		t.Errorf("Database.Path = %q, want ./polaris.db", cfg.Database.Path)
	}
	if cfg.Backup.Dir != "backups" {
		t.Errorf("Backup.Dir = %q, want backups (derived from Database.Path's directory)", cfg.Backup.Dir)
	}
	if cfg.Backup.RetentionDays != 30 {
		t.Errorf("Backup.RetentionDays = %d, want 30", cfg.Backup.RetentionDays)
	}
	if cfg.ContextWindowTokens != 100_000 {
		t.Errorf("ContextWindowTokens = %d, want 100000", cfg.ContextWindowTokens)
	}
	if cfg.MaxAgentTurns != 50 {
		t.Errorf("MaxAgentTurns = %d, want 50", cfg.MaxAgentTurns)
	}
	// DefaultModel unset in the fixture — must fall back to the first
	// (only) model, not an empty string that ModelByID would then have
	// to silently paper over.
	if cfg.DefaultModel != "test-model" {
		t.Errorf("DefaultModel = %q, want it to default to the first model's ID", cfg.DefaultModel)
	}
}

func TestLoad_NoRegistryIsAnError(t *testing.T) {
	path := writeTestConfig(t, `server:
  port: 8899
openrouter:
  api_key: "sk-test"
`)
	if _, err := Load(path, nil); err == nil {
		t.Fatal("expected an error when model registry is empty")
	}
}

func TestLoad_MissingAPIKeyIsAnError(t *testing.T) {
	// Virtually every feature depends on this — catching it at Load time
	// means a misconfigured deployment fails fast at startup instead of
	// surfacing as an opaque 401 from OpenRouter deep inside a request.
	path := writeTestConfig(t, "")
	if _, err := Load(path, testRegistry); err == nil {
		t.Fatal("expected an error when openrouter.api_key is unset")
	}
}

func TestLoad_MissingFileIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"), testRegistry); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestLoad_ExpandsEnvVars(t *testing.T) {
	t.Setenv("POLARIS_TEST_API_KEY", "sk-test-123")
	path := writeTestConfig(t, `openrouter:
  api_key: "${POLARIS_TEST_API_KEY}"
`)

	cfg, err := Load(path, testRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.OpenRouter.APIKey != "sk-test-123" {
		t.Errorf("APIKey = %q, want the expanded env var", cfg.OpenRouter.APIKey)
	}
}

// TestLoad_EnvVarSetButConfigYamlMissingBlockLeavesKeyEmpty locks in the
// exact shape of a real production incident: BRAVE_API_KEY/PARALLEL_API_KEY
// were live in the container's environment, but config.yaml (gitignored,
// hand-maintained, never synced by git pull) had no brave:/parallel: block
// referencing them via ${...}. os.ExpandEnv only substitutes placeholders
// that literally appear in the file, so the env var being set didn't help —
// cfg.Brave.APIKey stayed empty, brave.NewClient("") returned nil, and
// web_search's SearXNG -> Brave -> Parallel -> Tavily chain silently
// skipped straight to Tavily with no error anywhere. Load must not error in
// this case (the key is genuinely optional) — the warning that catches this
// now lives right after this check in Load; this test documents why the
// warning exists and that Load stays lenient rather than failing.
func TestLoad_EnvVarSetButConfigYamlMissingBlockLeavesKeyEmpty(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "brave-live-key-in-env")
	path := writeTestConfig(t, `openrouter:
  api_key: "sk-test"
`)

	cfg, err := Load(path, testRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Brave.APIKey != "" {
		t.Errorf("Brave.APIKey = %q, want empty — config.yaml never referenced BRAVE_API_KEY", cfg.Brave.APIKey)
	}
}

func TestLoad_AppliesModelOverrides(t *testing.T) {
	path := writeTestConfig(t, `
openrouter:
  api_key: "sk-test"
model_overrides:
  test-model:
    temperature: 0.7
    max_tokens: 2000
`)

	cfg, err := Load(path, testRegistry)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	model := cfg.ModelByID("test-model")
	if model.Temperature != 0.7 {
		t.Errorf("Temperature = %f, want 0.7 from override", model.Temperature)
	}
	if model.MaxTokens != 2000 {
		t.Errorf("MaxTokens = %d, want 2000 from override", model.MaxTokens)
	}
}

func TestModelByID_KnownID(t *testing.T) {
	cfg := &Config{
		DefaultModel: "a",
		Models: []ModelConfig{
			{ID: "a", Name: "Model A"},
			{ID: "b", Name: "Model B"},
		},
	}
	if got := cfg.ModelByID("b"); got.ID != "b" {
		t.Errorf("ModelByID(\"b\") = %+v, want ID b", got)
	}
}

func TestModelByID_UnknownIDFallsBackToDefault(t *testing.T) {
	cfg := &Config{
		DefaultModel: "b",
		Models: []ModelConfig{
			{ID: "a", Name: "Model A"},
			{ID: "b", Name: "Model B"},
		},
	}
	if got := cfg.ModelByID("does-not-exist"); got.ID != "b" {
		t.Errorf("ModelByID(unknown) = %+v, want fallback to default b", got)
	}
}

func TestModelByID_EmptyIDFallsBackToDefault(t *testing.T) {
	cfg := &Config{
		DefaultModel: "b",
		Models: []ModelConfig{
			{ID: "a", Name: "Model A"},
			{ID: "b", Name: "Model B"},
		},
	}
	if got := cfg.ModelByID(""); got.ID != "b" {
		t.Errorf("ModelByID(\"\") = %+v, want fallback to default b", got)
	}
}

func TestModelByID_UnknownDefaultFallsBackToFirstModel(t *testing.T) {
	// If DefaultModel itself doesn't match anything (a stale settings-panel
	// override after config.yaml dropped that model, say), ModelByID must
	// still return something rather than a zero-value ModelConfig.
	cfg := &Config{
		DefaultModel: "no-longer-exists",
		Models: []ModelConfig{
			{ID: "a", Name: "Model A"},
			{ID: "b", Name: "Model B"},
		},
	}
	if got := cfg.ModelByID(""); got.ID != "a" {
		t.Errorf("ModelByID(\"\") with unresolvable default = %+v, want fallback to first model a", got)
	}
}

func TestMultimodalModel_ReturnsFirstMultimodalEntry(t *testing.T) {
	cfg := &Config{
		Models: []ModelConfig{
			{ID: "text-only", Multimodal: false},
			{ID: "vision-a", Multimodal: true},
			{ID: "vision-b", Multimodal: true},
		},
	}
	got, ok := cfg.MultimodalModel()
	if !ok {
		t.Fatal("MultimodalModel() ok = false, want true")
	}
	if got.ID != "vision-a" {
		t.Errorf("MultimodalModel() = %+v, want the first multimodal entry (vision-a)", got)
	}
}

func TestMultimodalModel_NoneConfigured(t *testing.T) {
	cfg := &Config{
		Models: []ModelConfig{
			{ID: "a", Multimodal: false},
			{ID: "b", Multimodal: false},
		},
	}
	if _, ok := cfg.MultimodalModel(); ok {
		t.Error("MultimodalModel() ok = true, want false when no model is marked multimodal")
	}
}

// TestResearchWorkerModel_ReturnsFirstMarkedEntry mirrors
// TestMultimodalModel_ReturnsFirstMultimodalEntry above — same lookup
// shape (config.ResearchWorkerModel / ModelConfig.ResearchWorker), reused
// on purpose for Tier 2's sub-agent worker model (see
// docs/plans/deep-research-two-tier.md's "Sub-agents" section).
func TestResearchWorkerModel_ReturnsFirstMarkedEntry(t *testing.T) {
	cfg := &Config{
		Models: []ModelConfig{
			{ID: "other", ResearchWorker: false},
			{ID: "worker-a", ResearchWorker: true},
			{ID: "worker-b", ResearchWorker: true},
		},
	}
	got, ok := cfg.ResearchWorkerModel()
	if !ok {
		t.Fatal("ResearchWorkerModel() ok = false, want true")
	}
	if got.ID != "worker-a" {
		t.Errorf("ResearchWorkerModel() = %+v, want the first marked entry (worker-a)", got)
	}
}

// TestResearchWorkerModel_NoneConfiguredFallsBackToDefault covers the case
// no model is explicitly marked research_worker: true — unlike
// MultimodalModel (where "no vision model configured" is a real failure
// the caller must surface), a sub-agent still needs *some* model to run
// on, so this falls back to the registry's normal default rather than
// returning ok=false.
func TestResearchWorkerModel_NoneConfiguredFallsBackToDefault(t *testing.T) {
	cfg := &Config{
		DefaultModel: "b",
		Models: []ModelConfig{
			{ID: "a", ResearchWorker: false},
			{ID: "b", ResearchWorker: false},
		},
	}
	got, ok := cfg.ResearchWorkerModel()
	if !ok {
		t.Fatal("ResearchWorkerModel() ok = false, want true (falls back to default)")
	}
	if got.ID != "b" {
		t.Errorf("ResearchWorkerModel() = %+v, want fallback to DefaultModel (b)", got)
	}
}
