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
	path := writeTestConfig(t, "")

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
`)
	if _, err := Load(path, nil); err == nil {
		t.Fatal("expected an error when model registry is empty")
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

func TestLoad_AppliesModelOverrides(t *testing.T) {
	path := writeTestConfig(t, `
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
