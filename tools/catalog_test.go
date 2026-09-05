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
	ctx.AttachmentData = []byte("pdf bytes")
	ctx.RequestLocation = func() (string, bool) { return "", false }
	ctx.WriteMemory = func(name, memType, description, content string) error { return nil }
	ctx.DeepResearch = true
	ctx.SpawnResearchers = func(ctx *Context, tasks []SubAgentTask) []SubAgentReport { return nil }
	ctx.PulsarWizard = true
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

// TestCatalog_AllFilesLoadAndNamesMatch validates the real
// tools/descriptions/*.yaml files shipped in the repo, not just
// catalogDefaults' fallback text — catalogDescriptionsDir is relative to
// the process's working directory (same hot-reload convention as
// prompt.md/prompts.yaml), which is this package's own directory during
// `go test`, not the repo root, so this chdirs up one level first.
func TestCatalog_AllFilesLoadAndNamesMatch(t *testing.T) {
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

// TestCatalogDefaults_HasEntryForEveryToolInOrder guards the fallback path
// TestCatalog_AllFilesLoadAndNamesMatch deliberately bypasses (that test
// chdirs to the repo root so every real tools/descriptions/*.yaml loads,
// never exercising catalogDefaults at all). Any environment where a
// description file is genuinely missing — every `go test` run in this
// package included, since catalogDescriptionsDir is relative to the
// process's CWD and never resolves during a normal test — falls back to
// catalogDefaults[name], and a name absent from that map silently
// resolves to a zero-value catalogEntry: Name "" fails to match
// ctx.DisabledTools/its own Category, so offered()'s very first check
// ("if ctx.DisabledTools[e.Name]") never matches and the tool becomes
// permanently un-disableable with a blank description — exactly what
// happened to "calculator" until this test was added.
func TestCatalogDefaults_HasEntryForEveryToolInOrder(t *testing.T) {
	for _, name := range catalogOrder {
		entry, ok := catalogDefaults[name]
		if !ok {
			t.Errorf("catalogDefaults has no fallback entry for %q — it will silently resolve to a zero-value "+
				"catalogEntry (Name \"\") whenever its YAML file can't be loaded, defeating DisabledTools/Category "+
				"gating for that tool entirely", name)
			continue
		}
		if entry.Name != name {
			t.Errorf("catalogDefaults[%q].Name = %q, want %q — offered()'s gating keys off Name, so a mismatched "+
				"fallback Name silently breaks it", name, entry.Name, name)
		}
	}
}

func TestCatalogEntry_Offered(t *testing.T) {
	withKeys := newTestContext()
	withKeys.LastFMAPIKey = "x"
	withKeys.TMDBAPIKey = "x"
	withoutKeys := newTestContext()
	withAttachment := newTestContext()
	withAttachment.AttachmentData = []byte("pdf bytes")

	cases := []struct {
		name  string
		entry catalogEntry
		ctx   *Context
		want  bool
	}{
		{"unset requires is always offered", catalogEntry{Requires: ""}, withoutKeys, true},
		{"lastfm_api_key offered when configured", catalogEntry{Requires: "lastfm_api_key"}, withKeys, true},
		{"lastfm_api_key excluded when missing", catalogEntry{Requires: "lastfm_api_key"}, withoutKeys, false},
		{"tmdb_api_key offered when configured", catalogEntry{Requires: "tmdb_api_key"}, withKeys, true},
		{"tmdb_api_key excluded when missing", catalogEntry{Requires: "tmdb_api_key"}, withoutKeys, false},
		{"attachment offered when this turn has one", catalogEntry{Requires: "attachment"}, withAttachment, true},
		{"attachment excluded when this turn has none", catalogEntry{Requires: "attachment"}, withoutKeys, false},
		{"unrecognized requires fails closed", catalogEntry{Name: "typo_tool", Requires: "last_fm_api_key"}, withKeys, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.entry.offered(c.ctx); got != c.want {
				t.Errorf("offered() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCatalogEntry_Offered_DeepResearch covers spawn_researchers' gating —
// its Requires: "deep_research" case (catalog.go's offered()) checks both
// ctx.DeepResearch AND ctx.SpawnResearchers being wired, not just one:
// Deep Research alone shouldn't offer the tool if nothing ever wired the
// closure (e.g. a config path that forgot to), and a wired closure alone
// shouldn't offer it outside Deep Research mode (e.g. Tier 1's Researcher
// focus mode, which must stay single-agent).
func TestCatalogEntry_Offered_DeepResearch(t *testing.T) {
	spawner := func(ctx *Context, tasks []SubAgentTask) []SubAgentReport { return nil }

	cases := []struct {
		name string
		ctx  *Context
		want bool
	}{
		{
			"offered when DeepResearch is on and SpawnResearchers is wired",
			&Context{DeepResearch: true, SpawnResearchers: spawner},
			true,
		},
		{
			"excluded when SpawnResearchers is nil even under Deep Research",
			&Context{DeepResearch: true, SpawnResearchers: nil},
			false,
		},
		{
			"excluded when Deep Research is off even if SpawnResearchers is wired",
			&Context{DeepResearch: false, SpawnResearchers: spawner},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entry := catalogEntry{Name: "spawn_researchers", Requires: "deep_research"}
			if got := entry.offered(c.ctx); got != c.want {
				t.Errorf("offered() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCatalogEntry_Offered_SubAgentRole covers Tier 2 of
// docs/plans/deep-research-two-tier.md's tool-scoping requirement: a
// sub-agent Context (SubAgentRole set) is restricted to web_search,
// web_read, and think regardless of what Requires/keys/Category gating
// would otherwise allow — a control tool like memory or an
// otherwise-fully-configured recommendation tool like movies must still be
// excluded.
func TestCatalogEntry_Offered_SubAgentRole(t *testing.T) {
	subAgent := newTestContext()
	subAgent.SubAgentRole = "researcher"
	subAgent.TMDBAPIKey = "x" // configured, but must still be excluded below

	normal := newTestContext()
	normal.TMDBAPIKey = "x"

	cases := []struct {
		name  string
		entry catalogEntry
		ctx   *Context
		want  bool
	}{
		{"web_search offered under sub-agent role", catalogEntry{Name: "web_search"}, subAgent, true},
		{"web_read offered under sub-agent role", catalogEntry{Name: "web_read"}, subAgent, true},
		{"think offered under sub-agent role", catalogEntry{Name: "think"}, subAgent, true},
		{"reference_lookup offered under sub-agent role", catalogEntry{Name: "reference_lookup"}, subAgent, true},
		{"movies excluded under sub-agent role despite configured key", catalogEntry{Name: "movies", Requires: "tmdb_api_key"}, subAgent, false},
		{"memory excluded under sub-agent role", catalogEntry{Name: "memory"}, subAgent, false},
		{"movies offered normally with configured key (control)", catalogEntry{Name: "movies", Requires: "tmdb_api_key"}, normal, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.entry.offered(c.ctx); got != c.want {
				t.Errorf("offered() = %v, want %v", got, c.want)
			}
		})
	}
}
