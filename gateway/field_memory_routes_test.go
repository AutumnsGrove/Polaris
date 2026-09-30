package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"polaris/store"
)

func TestFieldMemoryAPI_ListEditForgetScopedPerField(t *testing.T) {
	h := newTestHarness(t, "")
	a, _ := h.db.CreateField(store.Field{Name: "a", MemoryMode: store.FieldMemoryFieldOnly})
	b, _ := h.db.CreateField(store.Field{Name: "b", MemoryMode: store.FieldMemoryFieldOnly})
	_ = h.db.CreateFieldMemory(a.ID, "note", "project", "a desc", "a body", "")
	_ = h.db.CreateFieldMemory(b.ID, "note", "project", "b desc", "b body", "")

	list := func(id string) []store.Memory {
		code, out := doJSON(t, "GET", h.url("/api/fields/"+id+"/memories"), nil)
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", id, code, out)
		}
		var ms []store.Memory
		_ = json.Unmarshal(out, &ms)
		return ms
	}
	if ms := list(a.ID); len(ms) != 1 || ms[0].Content != "a body" {
		t.Errorf("a's list = %+v", ms)
	}

	// Editing through a's route touches only a's row, even with b holding the same name.
	code, _ := doJSON(t, "PATCH", h.url("/api/fields/"+a.ID+"/memories/note"), map[string]string{"content": "a edited"})
	if code != http.StatusNoContent {
		t.Fatalf("patch: %d", code)
	}
	if ms := list(a.ID); ms[0].Content != "a edited" {
		t.Errorf("edit not applied: %+v", ms)
	}
	if ms := list(b.ID); ms[0].Content != "b body" {
		t.Errorf("editing a's memory changed b's: %+v", ms)
	}

	if code, _ := doJSON(t, "PATCH", h.url("/api/fields/"+a.ID+"/memories/note"), map[string]string{"type": "bogus"}); code != http.StatusBadRequest {
		t.Errorf("bad type: %d, want 400", code)
	}
	if code, _ := doJSON(t, "PATCH", h.url("/api/fields/"+a.ID+"/memories/nope"), map[string]string{"content": "x"}); code != http.StatusNotFound {
		t.Errorf("missing memory: %d, want 404", code)
	}
	if code, _ := doJSON(t, "GET", h.url("/api/fields/no-such-field/memories"), nil); code != http.StatusNotFound {
		t.Errorf("unknown field: %d, want 404", code)
	}

	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+a.ID+"/memories/note"), nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if len(list(a.ID)) != 0 || len(list(b.ID)) != 1 {
		t.Errorf("delete hit the wrong field: a=%d b=%d", len(list(a.ID)), len(list(b.ID)))
	}
	// The global list is never touched by any of this.
	if g, _ := h.db.ListMemories(); len(g) != 0 {
		t.Errorf("global memories gained entries: %+v", g)
	}
}

func TestFieldsAPI_MemoryModeValidation(t *testing.T) {
	h := newTestHarness(t, "")
	for _, m := range []string{"default", "field_only", "both", "none"} {
		if code, out := doJSON(t, "POST", h.url("/api/fields"), map[string]interface{}{"name": "f-" + m, "memory_mode": m}); code != http.StatusOK {
			t.Errorf("mode %q rejected: %d %s", m, code, out)
		}
	}
	for _, m := range []string{"field_scoped", "project_scoped", "all"} {
		if code, _ := doJSON(t, "POST", h.url("/api/fields"), map[string]interface{}{"name": "x", "memory_mode": m}); code != http.StatusBadRequest {
			t.Errorf("mode %q accepted, want 400", m)
		}
	}
}

// End to end through a real turn: what each memory mode actually puts in the
// request the model receives, and the global Memory switch's effect on it.
func TestWebSocket_FieldMemoryModes(t *testing.T) {
	srv, bodies := fieldTurnServer(t)
	h := newTestHarness(t, srv.URL)

	mk := func(name, mode string) *store.Field {
		f, err := h.db.CreateField(store.Field{Name: name, MemoryMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		_ = h.db.CreateFieldMemory(f.ID, "note-"+name, "project", "FIELDMEM-"+name, "body", "")
		return f
	}
	_ = h.db.CreateMemory("g-note", "user", "GLOBALMEM-marker", "body", "")
	both, fieldOnly, def := mk("both", store.FieldMemoryBoth), mk("only", store.FieldMemoryFieldOnly), mk("def", store.FieldMemoryDefault)

	conn := dialWS(t, h)
	turn := func(fieldID string) string {
		t.Helper()
		m := map[string]interface{}{"type": "message", "model": "test-model", "content": "hi"}
		if fieldID != "" {
			m["field_id"] = fieldID
		}
		if err := conn.WriteJSON(m); err != nil {
			t.Fatal(err)
		}
		readEventsUntilDone(t, conn, 5*time.Second)
		all := bodies()
		for i := len(all) - 1; i >= 0; i-- {
			if strings.Contains(all[i], `"tools"`) {
				return all[i]
			}
		}
		t.Fatal("no tool-bearing request recorded")
		return ""
	}
	has := func(body, s string) bool { return strings.Contains(body, s) }

	// Global Memory switch ON.
	if b := turn(both.ID); !has(b, "FIELDMEM-both") || !has(b, "GLOBALMEM-marker") || !has(b, "note-both (field)") || !has(b, "g-note (global)") {
		t.Errorf("both: expected field + global entries, tagged")
	}
	if b := turn(fieldOnly.ID); !has(b, "FIELDMEM-only") || has(b, "GLOBALMEM-marker") || has(b, "FIELDMEM-both") {
		t.Errorf("field_only: expected only its own entry")
	}
	if b := turn(def.ID); !has(b, "GLOBALMEM-marker") || has(b, "FIELDMEM-def") {
		t.Errorf("default: expected global only, never the field's own store")
	}
	if b := turn(""); !has(b, "GLOBALMEM-marker") || has(b, "FIELDMEM-") {
		t.Errorf("ordinary thread: expected global only, no field entries")
	}

	// Global Memory switch OFF: both shrinks to field-only, default sees nothing.
	if err := h.db.SetSetting(settingMemoryEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if b := turn(both.ID); !has(b, "FIELDMEM-both") || has(b, "GLOBALMEM-marker") {
		t.Errorf("both + global off: expected field entries only")
	}
	if b := turn(fieldOnly.ID); !has(b, "FIELDMEM-only") {
		t.Errorf("field_only + global off: lost its own store")
	}
	if b := turn(def.ID); has(b, "GLOBALMEM-marker") || has(b, "FIELDMEM-") {
		t.Errorf("default + global off: expected no memory at all")
	}
}
