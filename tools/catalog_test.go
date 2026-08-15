package tools

import (
	"os"
	"strings"
	"testing"
)

func TestToolsPrompt_ExcludesGatedToolsWithoutKeys(t *testing.T) {
	prompt := ToolsPrompt(newTestContext())
	if want := "- web_search:"; !strings.Contains(prompt, want) {
		t.Errorf("ToolsPrompt() missing %q:\n%s", want, prompt)
	}
	if strings.Contains(prompt, "- music:") {
		t.Errorf("ToolsPrompt() included music with no LastFMAPIKey configured:\n%s", prompt)
	}
	if strings.Contains(prompt, "- movies:") {
		t.Errorf("ToolsPrompt() included movies with no TMDBAPIKey configured:\n%s", prompt)
	}
}

func TestToolsPrompt_IncludesGatedToolsWithKeys(t *testing.T) {
	ctx := newTestContext()
	ctx.LastFMAPIKey = "test-key"
	ctx.TMDBAPIKey = "test-key"
	prompt := ToolsPrompt(ctx)
	if want := "- music:"; !strings.Contains(prompt, want) {
		t.Errorf("ToolsPrompt() missing %q:\n%s", want, prompt)
	}
	if want := "- movies:"; !strings.Contains(prompt, want) {
		t.Errorf("ToolsPrompt() missing %q:\n%s", want, prompt)
	}
}

func TestToolsPrompt_OrderMatchesCatalogOrder(t *testing.T) {
	ctx := newTestContext()
	ctx.LastFMAPIKey = "test-key"
	ctx.TMDBAPIKey = "test-key"
	prompt := ToolsPrompt(ctx)

	lastIdx := -1
	for _, name := range catalogOrder {
		idx := strings.Index(prompt, "- "+name+":")
		if idx == -1 {
			t.Fatalf("ToolsPrompt() missing %q:\n%s", name, prompt)
		}
		if idx < lastIdx {
			t.Errorf("tool %q appears out of catalogOrder in ToolsPrompt() output:\n%s", name, prompt)
		}
		lastIdx = idx
	}
}

// TestCatalog_AllTwelveFilesLoadAndNamesMatch validates the real
// tools/descriptions/*.yaml files shipped in the repo, not just
// catalogDefaults' fallback text — catalogDescriptionsDir is relative to
// the process's working directory (same hot-reload convention as
// prompt.md/prompts.yaml), which is this package's own directory during
// `go test`, not the repo root, so this chdirs up one level first.
func TestCatalog_AllTwelveFilesLoadAndNamesMatch(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	catalog := loadCatalog()
	for _, name := range catalogOrder {
		entry, ok := catalog[name]
		if !ok {
			t.Errorf("catalog missing entry for %q", name)
			continue
		}
		if entry.Name != name {
			t.Errorf("tools/descriptions/%s.yaml declares name %q, want %q", name, entry.Name, name)
		}
		if entry.Description == "" {
			t.Errorf("%q has an empty description", name)
		}
		if entry.APIDescription == "" {
			t.Errorf("%q has an empty api_description", name)
		}
		if entry == catalogDefaults[name] {
			t.Errorf("%q loaded catalogDefaults' fallback text instead of the real YAML file", name)
		}
	}
}
