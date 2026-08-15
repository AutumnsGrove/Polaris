package tools

import (
	"context"
	"testing"
)

func newTestContext() *Context {
	return &Context{
		Ctx:  context.Background(),
		Emit: func(string, map[string]interface{}) {},
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	result := Dispatch("not_a_real_tool", "{}", newTestContext())
	if result != "error: unknown tool not_a_real_tool" {
		t.Errorf("result = %q, want the unknown-tool error", result)
	}
}

func TestDispatch_KnownTool(t *testing.T) {
	// "think" self-registers via init() in think.go — exercised here
	// through the registry rather than calling handleThink directly, to
	// cover Dispatch's lookup path too.
	result := Dispatch("think", `{"thought":"testing"}`, newTestContext())
	if result != "noted" {
		t.Errorf("result = %q, want %q", result, "noted")
	}
}

func TestContext_AddCitation_DeduplicatesByURL(t *testing.T) {
	ctx := newTestContext()
	ctx.AddCitation(Citation{Title: "First", URL: "https://example.com/a"})
	ctx.AddCitation(Citation{Title: "Second", URL: "https://example.com/b"})
	// Same URL again, different title (a search hit that then got read in
	// full) — must not produce a duplicate badge.
	ctx.AddCitation(Citation{Title: "First (reread)", URL: "https://example.com/a"})

	if len(ctx.Citations) != 2 {
		t.Fatalf("got %d citations, want 2: %+v", len(ctx.Citations), ctx.Citations)
	}
	if ctx.Citations[0].Title != "First" {
		t.Errorf("first citation's title was overwritten: %+v", ctx.Citations[0])
	}
}

func TestDefs_ReturnsAllTwelveToolsWhenAllKeysConfigured(t *testing.T) {
	ctx := newTestContext()
	ctx.LastFMAPIKey = "test-key"
	ctx.TMDBAPIKey = "test-key"
	defs := Defs(ctx)
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	for _, want := range []string{
		"think", "web_search", "web_read", "nearby_search", "youtube_transcript",
		"weather", "reference_lookup", "github_repo", "dictionary", "music", "books", "movies",
	} {
		if !names[want] {
			t.Errorf("Defs() missing %q, got %v", want, names)
		}
	}
	if len(defs) != 12 {
		t.Errorf("got %d tool defs, want exactly 12", len(defs))
	}
}

func TestDefs_ExcludesMusicAndMoviesWithoutKeys(t *testing.T) {
	defs := Defs(newTestContext())
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	if names["music"] {
		t.Error("Defs() included music with no LastFMAPIKey configured")
	}
	if names["movies"] {
		t.Error("Defs() included movies with no TMDBAPIKey configured")
	}
	for _, want := range []string{
		"think", "web_search", "web_read", "nearby_search", "youtube_transcript",
		"weather", "reference_lookup", "github_repo", "dictionary", "books",
	} {
		if !names[want] {
			t.Errorf("Defs() missing %q, got %v", want, names)
		}
	}
	if len(defs) != 10 {
		t.Errorf("got %d tool defs, want exactly 10", len(defs))
	}
}

func TestDefs_OrderIsStable(t *testing.T) {
	ctx := newTestContext()
	ctx.LastFMAPIKey = "test-key"
	ctx.TMDBAPIKey = "test-key"
	want := []string{
		"think", "web_search", "web_read", "nearby_search", "youtube_transcript",
		"weather", "reference_lookup", "github_repo", "dictionary", "music", "books", "movies",
	}
	for i := 0; i < 2; i++ {
		defs := Defs(ctx)
		if len(defs) != len(want) {
			t.Fatalf("call %d: got %d defs, want %d", i, len(defs), len(want))
		}
		for j, d := range defs {
			if d.Function.Name != want[j] {
				t.Errorf("call %d: position %d = %q, want %q", i, j, d.Function.Name, want[j])
			}
		}
	}
}
