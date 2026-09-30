package gateway

import (
	"encoding/json"
	"io"
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

// The Field page's "tell it what to remember" box: the instruction-driven
// tool loop must write into THIS field's store (never global, never a
// sibling's) and hand back only this field's refreshed list.
func TestFieldMemoryChat_WritesToFieldStoreOnly(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBody(`{"index":0,"id":"call_1","type":"function","function":{"name":"memory",` +
			`"arguments":"{\"action\":\"write\",\"name\":\"codename\",\"type\":\"project\",\"description\":\"codename is ORCA-7\",\"content\":\"ORCA-7\"}"}}`),
		textSSEBody("Saved the codename."),
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	a, _ := h.db.CreateField(store.Field{Name: "a", MemoryMode: store.FieldMemoryBoth})
	b, _ := h.db.CreateField(store.Field{Name: "b", MemoryMode: store.FieldMemoryBoth})
	_ = h.db.CreateMemory("global-one", "user", "g", "g", "")

	code, out := doJSON(t, "POST", h.url("/api/fields/"+a.ID+"/memories/chat"), map[string]string{"instruction": "remember the codename is ORCA-7"})
	if code != http.StatusOK {
		t.Fatalf("chat: %d %s", code, out)
	}
	var resp struct {
		Message  string         `json:"message"`
		Memories []store.Memory `json:"memories"`
	}
	_ = json.Unmarshal(out, &resp)
	if resp.Message != "Saved the codename." || len(resp.Memories) != 1 || resp.Memories[0].Name != "codename" {
		t.Errorf("response = %+v, want only a's new memory (no global entry)", resp)
	}
	if g, _ := h.db.ListMemories(); len(g) != 1 || g[0].Name != "global-one" {
		t.Errorf("global list changed by a field chat: %+v", g)
	}
	if bl, _ := h.db.ListFieldMemories(b.ID); len(bl) != 0 {
		t.Errorf("sibling field gained entries: %+v", bl)
	}
	if code, _ := doJSON(t, "POST", h.url("/api/fields/"+a.ID+"/memories/chat"), map[string]string{"instruction": "  "}); code != http.StatusBadRequest {
		t.Errorf("blank instruction: %d, want 400", code)
	}
	if code, _ := doJSON(t, "POST", h.url("/api/fields/nope/memories/chat"), map[string]string{"instruction": "x"}); code != http.StatusNotFound {
		t.Errorf("unknown field: %d, want 404", code)
	}
}

// Import (another AI's dump) into a Field writes that field's own store only,
// and export contains only that field's memories — same as the global pair.
func TestFieldMemoryImportExport_ScopedToTheField(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBody(`{"index":0,"id":"call_1","type":"function","function":{"name":"memory",` +
			`"arguments":"{\"action\":\"write\",\"name\":\"user-role\",\"type\":\"user\",\"description\":\"backend engineer\",\"content\":\"backend engineer\"}"}}`),
		textSSEBody("Imported 1 memory."),
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	a, _ := h.db.CreateField(store.Field{Name: "Alpha", MemoryMode: store.FieldMemoryFieldOnly})
	b, _ := h.db.CreateField(store.Field{Name: "Bravo", MemoryMode: store.FieldMemoryFieldOnly})
	_ = h.db.CreateMemory("global-one", "user", "g", "g", "")
	_ = h.db.CreateFieldMemory(b.ID, "bravo-only", "project", "b", "b", "")

	code, out := doJSON(t, "POST", h.url("/api/fields/"+a.ID+"/memories/import"), map[string]string{"dump": "backend engineer"})
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, out)
	}
	var resp struct {
		Message  string         `json:"message"`
		Memories []store.Memory `json:"memories"`
	}
	_ = json.Unmarshal(out, &resp)
	if len(resp.Memories) != 1 || resp.Memories[0].Name != "user-role" {
		t.Errorf("import response = %+v, want only a's imported memory", resp.Memories)
	}
	if g, _ := h.db.ListMemories(); len(g) != 1 || g[0].Name != "global-one" {
		t.Errorf("a field import changed the global list: %+v", g)
	}
	if bl, _ := h.db.ListFieldMemories(b.ID); len(bl) != 1 || bl[0].Name != "bravo-only" {
		t.Errorf("a field import touched a sibling: %+v", bl)
	}
	if code, _ := doJSON(t, "POST", h.url("/api/fields/"+a.ID+"/memories/import"), map[string]string{"dump": "  "}); code != http.StatusBadRequest {
		t.Errorf("blank dump: %d, want 400", code)
	}

	resp2, err := http.Get(h.url("/api/fields/" + a.ID + "/memories/export"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	text := string(body)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(text, "user-role") || !strings.Contains(text, "Alpha") {
		t.Errorf("export = %d %q, want a's memory and its name", resp2.StatusCode, text)
	}
	if strings.Contains(text, "global-one") || strings.Contains(text, "bravo-only") {
		t.Errorf("export leaked another store: %q", text)
	}
	if cd := resp2.Header.Get("Content-Disposition"); !strings.Contains(cd, "polaris-field-memories-") {
		t.Errorf("Content-Disposition = %q", cd)
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
