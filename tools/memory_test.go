package tools

import (
	"strings"
	"testing"

	"polaris/store"
)

// fakeMemoryStore is a minimal in-memory stand-in for store.Store's memory
// methods — enough to exercise handleMemory's validation and dispatch logic
// without a real SQLite database. Mirrors UpdateMemory's real semantics
// (empty field = leave unchanged) since that's exactly the behavior
// handleMemoryEdit depends on to stay race-free — see store/memory.go's
// UpdateMemory doc comment.
type fakeMemoryStore struct {
	rows map[string]store.Memory
}

func newFakeMemoryStore() *fakeMemoryStore {
	return &fakeMemoryStore{rows: map[string]store.Memory{}}
}

func (f *fakeMemoryStore) wireInto(ctx *Context) {
	ctx.ListMemories = func() ([]store.MemoryIndexEntry, error) {
		entries := make([]store.MemoryIndexEntry, 0, len(f.rows))
		for _, m := range f.rows {
			entries = append(entries, store.MemoryIndexEntry{Name: m.Name, Type: m.Type, Description: m.Description, OccurredAt: m.OccurredAt})
		}
		return entries, nil
	}
	ctx.GetMemory = func(name string) (*store.Memory, error) {
		m, ok := f.rows[name]
		if !ok {
			return nil, store.ErrMemoryNotFound
		}
		return &m, nil
	}
	ctx.WriteMemory = func(name, memType, description, content, occurredAt string) error {
		if _, exists := f.rows[name]; exists {
			return store.ErrMemoryExists
		}
		f.rows[name] = store.Memory{Name: name, Type: memType, Description: description, Content: content, OccurredAt: occurredAt}
		return nil
	}
	ctx.EditMemory = func(name, memType, description, content, occurredAt string) error {
		m, ok := f.rows[name]
		if !ok {
			return store.ErrMemoryNotFound
		}
		if memType != "" {
			m.Type = memType
		}
		if description != "" {
			m.Description = description
		}
		if content != "" {
			m.Content = content
		}
		if occurredAt != "" {
			m.OccurredAt = occurredAt
		}
		f.rows[name] = m
		return nil
	}
	ctx.ForgetMemory = func(name string) error {
		if _, ok := f.rows[name]; !ok {
			return store.ErrMemoryNotFound
		}
		delete(f.rows, name)
		return nil
	}
}

func newMemoryTestContext() (*Context, *fakeMemoryStore) {
	ctx := newTestContext()
	fs := newFakeMemoryStore()
	fs.wireInto(ctx)
	return ctx, fs
}

func TestHandleMemory_WriteThenView(t *testing.T) {
	ctx, _ := newMemoryTestContext()

	result := Dispatch("memory", `{"action":"write","name":"user-timezone","type":"user","description":"the user's timezone","content":"US/Pacific"}`, ctx, "test-call")
	if !strings.Contains(result, "saved memory") {
		t.Fatalf("write result = %q", result)
	}
	// The write result itself must show what was actually saved — not
	// just a bare confirmation — since this is exactly what a user
	// expanding the tool-call block in the chat transcript sees (see
	// ToolEvent.svelte). Regression test for a real gap: this used to
	// return only `saved memory "user-timezone"` with no way to tell what
	// that memory actually contained short of a separate view call.
	if !strings.Contains(result, "US/Pacific") {
		t.Errorf("write result = %q, want it to contain the full content, not just a bare confirmation", result)
	}

	result = Dispatch("memory", `{"action":"view","name":"user-timezone"}`, ctx, "test-call")
	if !strings.Contains(result, "US/Pacific") {
		t.Errorf("view result = %q, want it to contain the full content", result)
	}
}

func TestHandleMemory_WriteRejectsDuplicateName(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	Dispatch("memory", `{"action":"write","name":"dup","type":"project","description":"d","content":"c"}`, ctx, "test-call")
	result := Dispatch("memory", `{"action":"write","name":"dup","type":"project","description":"d2","content":"c2"}`, ctx, "test-call")
	if !strings.Contains(result, "already exists") {
		t.Errorf("result = %q, want an already-exists error", result)
	}
}

func TestHandleMemory_WriteRejectsInvalidName(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	result := Dispatch("memory", `{"action":"write","name":"Not Kebab Case","type":"user","description":"d","content":"c"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for a non-slug name", result)
	}
}

func TestHandleMemory_WriteRejectsOverlongDescription(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	long := strings.Repeat("a", MaxMemoryDescriptionChars+1)
	result := Dispatch("memory", `{"action":"write","name":"too-long","type":"user","description":"`+long+`","content":"c"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for an over-cap description", result)
	}
}

func TestHandleMemory_EditIsPartial(t *testing.T) {
	ctx, fs := newMemoryTestContext()
	Dispatch("memory", `{"action":"write","name":"partial","type":"user","description":"orig desc","content":"orig content"}`, ctx, "test-call")

	// Only description supplied — content must survive untouched, exercising
	// the same empty-means-unchanged contract store.UpdateMemory relies on
	// to stay race-free (see store/memory.go).
	result := Dispatch("memory", `{"action":"edit","name":"partial","description":"new desc"}`, ctx, "test-call")
	if fs.rows["partial"].Content != "orig content" {
		t.Errorf("Content = %q after description-only edit, want it untouched", fs.rows["partial"].Content)
	}
	if fs.rows["partial"].Description != "new desc" {
		t.Errorf("Description = %q after edit, want %q", fs.rows["partial"].Description, "new desc")
	}
	// The edit result must reflect the resolved row (description just
	// changed, content untouched from the original write) — not just the
	// fields this one call happened to pass, which would silently omit
	// content from the confirmation entirely.
	if !strings.Contains(result, "new desc") || !strings.Contains(result, "orig content") {
		t.Errorf("edit result = %q, want it to contain both the new description and the untouched content", result)
	}
}

func TestHandleMemory_EditRejectsOverlongDescription(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	Dispatch("memory", `{"action":"write","name":"partial","type":"user","description":"orig","content":"orig"}`, ctx, "test-call")
	long := strings.Repeat("a", MaxMemoryDescriptionChars+1)
	result := Dispatch("memory", `{"action":"edit","name":"partial","description":"`+long+`"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for an over-cap description", result)
	}
}

func TestHandleMemory_EditMissingNameReturnsNotFound(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	result := Dispatch("memory", `{"action":"edit","name":"nope","description":"x"}`, ctx, "test-call")
	if !strings.Contains(result, "no memory named") {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

func TestHandleMemory_Forget(t *testing.T) {
	ctx, fs := newMemoryTestContext()
	Dispatch("memory", `{"action":"write","name":"temp","type":"project","description":"d","content":"c"}`, ctx, "test-call")
	result := Dispatch("memory", `{"action":"forget","name":"temp"}`, ctx, "test-call")
	if !strings.Contains(result, "forgot memory") {
		t.Errorf("result = %q", result)
	}
	if _, ok := fs.rows["temp"]; ok {
		t.Error("memory still present after forget")
	}
}

func TestHandleMemory_ViewListAndPromptShareFormatting(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	Dispatch("memory", `{"action":"write","name":"a","type":"user","description":"desc a","content":"c"}`, ctx, "test-call")

	viewResult := Dispatch("memory", `{"action":"view"}`, ctx, "test-call")
	promptResult := MemoryIndexPrompt(ctx)

	// Both must render the one entry with the same per-line shape — the
	// exact bug this test guards against is memory(view) and the
	// always-injected {memories} block silently drifting to different
	// punctuation for identical data (see formatMemoryIndex).
	wantLine := "- [user] a: desc a"
	if viewResult != wantLine {
		t.Errorf("view result = %q, want %q", viewResult, wantLine)
	}
	if promptResult != wantLine {
		t.Errorf("MemoryIndexPrompt result = %q, want %q", promptResult, wantLine)
	}
}

func TestHandleMemory_WriteWithOccurredAtAppearsInIndexAndView(t *testing.T) {
	ctx, fs := newMemoryTestContext()
	result := Dispatch("memory", `{"action":"write","name":"dated","type":"project","description":"d","content":"c","occurred_at":"2026-03-01"}`, ctx, "test-call")
	if !strings.Contains(result, "2026-03-01") {
		t.Errorf("write result = %q, want it to contain the occurred_at date", result)
	}
	if fs.rows["dated"].OccurredAt != "2026-03-01" {
		t.Errorf("OccurredAt = %q, want 2026-03-01", fs.rows["dated"].OccurredAt)
	}

	viewResult := Dispatch("memory", `{"action":"view"}`, ctx, "test-call")
	wantLine := "- [project, 2026-03-01] dated: d"
	if viewResult != wantLine {
		t.Errorf("view result = %q, want %q", viewResult, wantLine)
	}
	if got := MemoryIndexPrompt(ctx); got != wantLine {
		t.Errorf("MemoryIndexPrompt = %q, want %q", got, wantLine)
	}
}

func TestHandleMemory_WriteRejectsMalformedOccurredAt(t *testing.T) {
	ctx, _ := newMemoryTestContext()
	result := Dispatch("memory", `{"action":"write","name":"dated","type":"project","description":"d","content":"c","occurred_at":"not-a-date"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for a malformed occurred_at", result)
	}
}

func TestHandleMemory_UnavailableWhenNotWiredIntoContext(t *testing.T) {
	ctx := newTestContext() // no memory closures set
	result := Dispatch("memory", `{"action":"write","name":"x","type":"user","description":"d","content":"c"}`, ctx, "test-call")
	if !strings.Contains(result, "not available") {
		t.Errorf("result = %q, want a not-available error", result)
	}
	if got := MemoryIndexPrompt(ctx); got != "" {
		t.Errorf("MemoryIndexPrompt = %q, want empty when memory isn't wired in", got)
	}
}
