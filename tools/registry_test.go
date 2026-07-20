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

func TestDefs_ReturnsAllFourTools(t *testing.T) {
	defs := Defs()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	for _, want := range []string{"think", "web_search", "web_read", "nearby_search"} {
		if !names[want] {
			t.Errorf("Defs() missing %q, got %v", want, names)
		}
	}
	if len(defs) != 4 {
		t.Errorf("got %d tool defs, want exactly 4", len(defs))
	}
}
