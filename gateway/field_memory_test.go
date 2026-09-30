package gateway

import (
	"errors"
	"strings"
	"testing"

	"polaris/store"
	"polaris/tools"
)

func TestResolveMemoryAccess(t *testing.T) {
	f := func(mode string) *store.Field { return &store.Field{MemoryMode: mode} }
	for _, tc := range []struct {
		name     string
		field    *store.Field
		globalOn bool
		want     memoryAccess
	}{
		{"no field, global on", nil, true, memoryGlobal},
		{"no field, global off", nil, false, memoryNone},
		{"default, global on", f("default"), true, memoryGlobal},
		{"default, global off", f("default"), false, memoryNone},
		{"field_only, global on", f("field_only"), true, memoryFieldOnly},
		{"field_only ignores the global switch", f("field_only"), false, memoryFieldOnly},
		{"both, global on", f("both"), true, memoryBoth},
		{"both falls back to field-only when global is off", f("both"), false, memoryFieldOnly},
		{"none, global on", f("none"), true, memoryNone},
		{"none, global off", f("none"), false, memoryNone},
	} {
		if got := resolveMemoryAccess(tc.field, tc.globalOn); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func newFieldMemoryFixture(t *testing.T) (*store.Store, *store.Field, *store.Field) {
	t.Helper()
	db := openTestStoreForConstellation(t)
	a, err := db.CreateField(store.Field{Name: "a", MemoryMode: store.FieldMemoryBoth})
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateField(store.Field{Name: "b", MemoryMode: store.FieldMemoryFieldOnly})
	if err != nil {
		t.Fatal(err)
	}
	return db, a, b
}

func TestFieldOnlyClosures_IsolatedFromGlobalAndSiblings(t *testing.T) {
	db, a, b := newFieldMemoryFixture(t)
	_ = db.CreateMemory("g", "user", "global one", "g", "")

	ca := newMemoryClosures(db, a, memoryFieldOnly)
	cb := newMemoryClosures(db, b, memoryFieldOnly)
	if err := ca.write("secret", "project", "a only", "body", ""); err != nil {
		t.Fatal(err)
	}

	if list, _ := cb.list(); len(list) != 0 {
		t.Errorf("sibling field sees %+v, want nothing", list)
	}
	if list, _ := ca.list(); len(list) != 1 || list[0].Name != "secret" || list[0].Scope != "" {
		t.Errorf("field-only list = %+v, want just its own untagged entry (no global)", list)
	}
	if _, err := db.GetMemory("secret"); !errors.Is(err, store.ErrMemoryNotFound) {
		t.Errorf("field write leaked into the global list: %v", err)
	}
	if _, err := ca.get("g"); !errors.Is(err, store.ErrMemoryNotFound) {
		t.Errorf("field-only turn can read a global memory: %v", err)
	}
}

func TestBothClosures_LayeredReadsFieldOnlyWrites(t *testing.T) {
	db, a, _ := newFieldMemoryFixture(t)
	_ = db.CreateMemory("g", "user", "global one", "gbody", "")
	c := newMemoryClosures(db, a, memoryBoth)

	if err := c.write("f", "project", "field one", "fbody", ""); err != nil {
		t.Fatal(err)
	}
	// Reads: both stores, field first, each tagged.
	list, _ := c.list()
	if len(list) != 2 || list[0].Name != "f" || list[0].Scope != store.MemoryScopeField ||
		list[1].Name != "g" || list[1].Scope != store.MemoryScopeGlobal {
		t.Errorf("merged list = %+v, want field entry then global entry, tagged", list)
	}
	if m, err := c.get("g"); err != nil || m.Content != "gbody" {
		t.Errorf("get global via both = %+v, %v", m, err)
	}
	// Writes never reach global.
	if _, err := db.GetMemory("f"); !errors.Is(err, store.ErrMemoryNotFound) {
		t.Errorf("both-mode write landed in global: %v", err)
	}
	// Global is read-only: edit/forget get the specific error, not "not found".
	if err := c.edit("g", "", "changed", "", ""); !errors.Is(err, store.ErrGlobalMemoryReadOnly) {
		t.Errorf("edit global = %v, want ErrGlobalMemoryReadOnly", err)
	}
	if err := c.forget("g"); !errors.Is(err, store.ErrGlobalMemoryReadOnly) {
		t.Errorf("forget global = %v, want ErrGlobalMemoryReadOnly", err)
	}
	if m, _ := db.GetMemory("g"); m == nil || m.Description != "global one" {
		t.Errorf("global memory was modified from a field: %+v", m)
	}
	if err := c.edit("missing", "", "x", "", ""); !errors.Is(err, store.ErrMemoryNotFound) {
		t.Errorf("edit of a name in neither store = %v, want ErrMemoryNotFound", err)
	}
}

func TestBothClosures_WriteRejectsGlobalNameCollision(t *testing.T) {
	db, a, _ := newFieldMemoryFixture(t)
	_ = db.CreateMemory("user-timezone", "user", "global tz", "UTC", "")
	c := newMemoryClosures(db, a, memoryBoth)

	if err := c.write("user-timezone", "user", "field tz", "EST", ""); !errors.Is(err, store.ErrMemoryNameInGlobal) {
		t.Fatalf("write colliding with a global name = %v, want ErrMemoryNameInGlobal", err)
	}
	if list, _ := db.ListFieldMemories(a.ID); len(list) != 0 {
		t.Errorf("rejected write still saved: %+v", list)
	}
	// Field-only mode can't see global, so the same name is fine there.
	other := newMemoryClosures(db, a, memoryFieldOnly)
	if err := other.write("user-timezone", "user", "field tz", "EST", ""); err != nil {
		t.Errorf("field-only write of a name that exists globally: %v", err)
	}
	// With the field twin now present, a bare view in both mode returns the field's.
	if m, _ := c.get("user-timezone"); m == nil || m.Content != "EST" {
		t.Errorf("shadowed view = %+v, want the field's entry", m)
	}
}

// The tool-facing behavior: error wording the model actually reads, and the
// index tag only appearing when two stores are merged.
func TestMemoryTool_FieldMessages(t *testing.T) {
	db, a, _ := newFieldMemoryFixture(t)
	_ = db.CreateMemory("g", "user", "global one", "gbody", "")
	c := newMemoryClosures(db, a, memoryBoth)
	ctx := &tools.Context{
		Emit:         func(string, map[string]interface{}) {},
		ListMemories: c.list, GetMemory: c.get,
		WriteMemory: c.write, EditMemory: c.edit, ForgetMemory: c.forget,
	}
	call := func(args string) string { return tools.Dispatch("memory", args, ctx, "call-1") }

	if out := call(`{"action":"write","name":"g","type":"user","description":"d","content":"c"}`); !strings.Contains(out, "different name") {
		t.Errorf("collision message = %q", out)
	}
	if out := call(`{"action":"forget","name":"g"}`); !strings.Contains(out, "read-only") {
		t.Errorf("forget-global message = %q", out)
	}
	if out := tools.MemoryIndexPrompt(ctx); !strings.Contains(out, "g (global)") {
		t.Errorf("merged index missing the global tag: %q", out)
	}
	plain := newMemoryClosures(db, a, memoryGlobal)
	if out := tools.MemoryIndexPrompt(&tools.Context{ListMemories: plain.list}); strings.Contains(out, "(global)") || strings.Contains(out, "(field)") {
		t.Errorf("unmerged index should carry no scope tags: %q", out)
	}
}
