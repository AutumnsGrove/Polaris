package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"polaris/store"
)

func TestFieldPromptBlock(t *testing.T) {
	if got := fieldPromptBlock(nil, []string{"a"}); got != "" {
		t.Errorf("nil field should produce no block, got %q", got)
	}

	p := &store.Field{Name: "Budget", CustomInstructions: "  Answer in metric.  "}
	bare := fieldPromptBlock(p, nil)
	if !strings.Contains(bare, "## Field: Budget") || !strings.Contains(bare, "Answer in metric.") {
		t.Errorf("block missing name/instructions: %q", bare)
	}
	if strings.Contains(bare, "/field") {
		t.Errorf("a field with no shared files shouldn't talk about a shared directory: %q", bare)
	}

	withFiles := fieldPromptBlock(p, []string{"a.csv", "b.md"})
	for _, want := range []string{"a.csv, b.md", "read-only", "/field", "save_to_field"} {
		if !strings.Contains(withFiles, want) {
			t.Errorf("block missing %q: %q", want, withFiles)
		}
	}

	// A pool that accumulates across many threads must not balloon the prompt.
	many := make([]string, maxFieldPromptFiles+7)
	for i := range many {
		many[i] = fmt.Sprintf("f%03d.txt", i)
	}
	capped := fieldPromptBlock(p, many)
	if strings.Contains(capped, fmt.Sprintf("f%03d.txt", maxFieldPromptFiles)) || !strings.Contains(capped, "and 7 more") {
		t.Errorf("file list not capped with an overflow note: %q", capped)
	}
}

func TestJoinCustomInstructions(t *testing.T) {
	cases := []struct{ global, block, want string }{
		{"G", "", "G"},
		{"", "P", "P"},
		{"  ", "P", "P"},
		{"G", "P", "G\n\nP"},
	}
	for _, c := range cases {
		if got := joinCustomInstructions(c.global, c.block); got != c.want {
			t.Errorf("join(%q,%q) = %q, want %q", c.global, c.block, got, c.want)
		}
	}
}

func TestListFieldFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p1")
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"b.txt", "a.txt", ".code_exec_x.py"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := listFieldFiles(root, "p1")
	if strings.Join(got, ",") != "a.txt,b.txt" {
		t.Errorf("got %v, want sorted regular non-dot files only", got)
	}
	if listFieldFiles(root, "never-created") != nil || listFieldFiles("", "p1") != nil {
		t.Error("a missing directory or unconfigured workspace should list nothing")
	}
}

// fieldTurnServer is a fake LLM that records every request body.
func fieldTurnServer(t *testing.T) (srv *httptest.Server, bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var recorded []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recorded = append(recorded, string(body))
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n", chunk)
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), recorded...)
	}
}

// End to end through a real WebSocket turn and the real request body: a
// thread born in a field is bound to it, its instructions reach the
// model (and keep reaching it on a continuation, re-read off the root row),
// memory_mode=none withholds the memory tool, and an ordinary thread gets
// none of it.
func TestWebSocket_FieldThread_InstructionsMemoryAndBinding(t *testing.T) {
	srv, bodies := fieldTurnServer(t)
	h := newTestHarness(t, srv.URL)

	alpha, err := h.db.CreateField(store.Field{
		Name: "Alpha", CustomInstructions: "ALWAYS-ANSWER-IN-HAIKU", MemoryMode: store.FieldMemoryNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := h.db.CreateField(store.Field{Name: "Beta", CustomInstructions: "BETA-RULES"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialWS(t, h)

	send := func(m map[string]interface{}) []map[string]interface{} {
		t.Helper()
		m["type"], m["model"] = "message", "test-model"
		if err := conn.WriteJSON(m); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		return readEventsUntilDone(t, conn, 5*time.Second)
	}
	threadOf := func(evs []map[string]interface{}) string {
		id, _ := evs[len(evs)-1]["thread_id"].(string)
		if id == "" {
			t.Fatal("done event carried no thread_id")
		}
		return id
	}

	// Turn 1: new thread inside Alpha.
	alphaThread := threadOf(send(map[string]interface{}{"content": "hi", "field_id": alpha.ID}))
	th, err := h.db.GetThread(alphaThread)
	if err != nil || th.FieldID == nil || *th.FieldID != alpha.ID {
		t.Fatalf("thread not bound to its field at creation: %+v err %v", th, err)
	}
	// Turn 2: continuation with NO field_id — must still be in Alpha.
	send(map[string]interface{}{"content": "and again", "thread_id": alphaThread})
	// Turn 3: a field with default memory keeps the memory tool.
	send(map[string]interface{}{"content": "hi", "field_id": beta.ID})
	// Turn 4: ordinary thread.
	send(map[string]interface{}{"content": "hi"})

	all := bodies()
	var chatBodies []string
	for _, b := range all {
		if strings.Contains(b, `"tools"`) { // skip title-generation calls
			chatBodies = append(chatBodies, b)
		}
	}
	if len(chatBodies) != 4 {
		t.Fatalf("tool-bearing requests = %d, want 4 (one per turn)", len(chatBodies))
	}
	names := toolBearingRequestToolNames(t, chatBodies)

	for i, label := range []string{"alpha turn 1", "alpha continuation"} {
		if !strings.Contains(chatBodies[i], "ALWAYS-ANSWER-IN-HAIKU") || !strings.Contains(chatBodies[i], "## Field: Alpha") {
			t.Errorf("%s: field instructions never reached the model", label)
		}
		if contains(names[i], "memory") {
			t.Errorf("%s: memory tool offered despite memory_mode=none: %v", label, names[i])
		}
		if contains(names[i], "save_to_field") {
			// No workspace configured in the default test config, so the tool
			// is (correctly) not offered — see catalog.go's field_workspace.
			t.Errorf("%s: save_to_field offered with no code_exec workspace configured", label)
		}
	}
	if !strings.Contains(chatBodies[2], "BETA-RULES") || strings.Contains(chatBodies[2], "ALWAYS-ANSWER-IN-HAIKU") {
		t.Error("beta turn should carry beta's instructions and only those")
	}
	if !contains(names[2], "memory") {
		t.Errorf("beta (default memory mode) lost the memory tool: %v", names[2])
	}
	if strings.Contains(chatBodies[3], "## Field:") || strings.Contains(chatBodies[3], "BETA-RULES") {
		t.Error("an ordinary thread got field content")
	}
	if !contains(names[3], "memory") {
		t.Errorf("ordinary thread lost the memory tool: %v", names[3])
	}
}

// A stale picker (field deleted since the page loaded) must error cleanly
// BEFORE a thread row exists, not leave an orphan behind.
func TestWebSocket_FieldThread_UnknownFieldErrorsWithoutCreatingThread(t *testing.T) {
	srv, _ := fieldTurnServer(t)
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "test-model", "field_id": "no-such-field",
	}); err != nil {
		t.Fatal(err)
	}
	evs := readEventsUntilDone(t, conn, 5*time.Second)
	last := evs[len(evs)-1]
	if last["type"] != "error" {
		t.Fatalf("last event = %v, want an error", last)
	}
	threads, err := h.db.ListThreads(10)
	if err != nil || len(threads) != 0 {
		t.Errorf("a failed field bind left %d thread(s) behind (err %v)", len(threads), err)
	}
}

// --- REST routes ---

func doJSON(t *testing.T, method, url string, body interface{}) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func uploadFieldFile(t *testing.T, url, filename, contentType, content string) (int, string) {
	t.Helper()
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	hdr.Set("Content-Type", contentType)
	part, _ := mw.CreatePart(hdr)
	part.Write([]byte(content))
	mw.Close()
	req, _ := http.NewRequest("POST", url, strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFieldsAPI_CRUDAndValidation(t *testing.T) {
	h := newTestHarness(t, "")

	for name, body := range map[string]map[string]interface{}{
		"blank name":       {"name": "  "},
		"missing name":     {"description": "x"},
		"unknown model":    {"name": "p", "default_model": "no-such-model"},
		"bad memory mode":  {"name": "p", "memory_mode": "field_scoped"},
		"bad focus mode":   {"name": "p", "default_focus_mode": "nonsense"},
		"bad color":        {"name": "p", "color": "javascript:alert(1)"},
		"instructions cap": {"name": "p", "custom_instructions": strings.Repeat("x", maxCustomInstructionsChars+1)},
	} {
		if code, _ := doJSON(t, "POST", h.url("/api/fields"), body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}

	code, out := doJSON(t, "POST", h.url("/api/fields"), map[string]interface{}{
		"name": "Alpha", "default_focus_mode": "off", "color": "technology", "favorite": true,
	})
	if code != http.StatusOK {
		t.Fatalf("create: %d %s", code, out)
	}
	var p store.Field
	json.Unmarshal(out, &p)
	if p.ID == "" || !p.ConstellationVisible || p.MemoryMode != "default" || !p.Favorite {
		t.Errorf("created field = %+v, want constellation_visible defaulting ON, memory default, favorite kept", p)
	}

	// PATCH is partial: touching one field leaves the rest alone.
	code, out = doJSON(t, "PATCH", h.url("/api/fields/"+p.ID), map[string]interface{}{"memory_mode": "none"})
	var patched store.Field
	json.Unmarshal(out, &patched)
	if code != http.StatusOK || patched.MemoryMode != "none" || patched.Color != "technology" || !patched.Favorite {
		t.Errorf("patch: %d %+v", code, patched)
	}

	var list []store.Field
	_, out = doJSON(t, "GET", h.url("/api/fields"), nil)
	json.Unmarshal(out, &list)
	if len(list) != 1 {
		t.Errorf("list = %d fields, want 1", len(list))
	}

	if code, _ := doJSON(t, "PATCH", h.url("/api/fields/nope"), map[string]interface{}{"name": "x"}); code != http.StatusNotFound {
		t.Errorf("patch missing field: %d, want 404", code)
	}
	if code, _ := doJSON(t, "GET", h.url("/api/fields/nope"), nil); code != http.StatusNotFound {
		t.Errorf("get missing field: %d, want 404", code)
	}
	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+p.ID), nil); code != http.StatusNoContent {
		t.Errorf("delete: %d, want 204", code)
	}
	if code, _ := doJSON(t, "GET", h.url("/api/fields/"+p.ID), nil); code != http.StatusNotFound {
		t.Errorf("get after delete: %d, want 404", code)
	}
	// An empty list must serialize as [] so the frontend never sees null.
	if _, out = doJSON(t, "GET", h.url("/api/fields"), nil); strings.TrimSpace(string(out)) != "[]" {
		t.Errorf("empty list = %s, want []", out)
	}
}

func TestFieldsAPI_FilePool(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)
	p, _ := h.db.CreateField(store.Field{Name: "p"})
	filesURL := h.url("/api/fields/" + p.ID + "/files")

	if code, body := uploadFieldFile(t, filesURL, "notes.md", "text/markdown", "first"); code != http.StatusOK || !strings.Contains(body, `"notes.md"`) {
		t.Fatalf("upload: %d %s", code, body)
	}
	// A duplicate name renames — the same never-overwrite rule save_to_field follows.
	if code, body := uploadFieldFile(t, filesURL, "notes.md", "text/markdown", "second"); code != http.StatusOK || !strings.Contains(body, `"notes-2.md"`) {
		t.Errorf("duplicate upload: %d %s, want it renamed to notes-2.md", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(workspaceDir, p.ID, "notes.md")); string(b) != "first" {
		t.Errorf("the original was overwritten: %q", b)
	}

	// The upload allowlist is shared with chat attachments; a dotfile is
	// refused. A path in the client's filename must never escape the
	// field's directory — pinned as an outcome (nothing lands outside),
	// not as one line's behavior: Go's mime/multipart already reduces the
	// filename to its base name before the handler sees it, and the
	// handler's own filepath.Base is defense in depth behind that.
	if code, _ := uploadFieldFile(t, filesURL, "run.exe", "application/x-msdownload", "MZ"); code != http.StatusBadRequest {
		t.Errorf("executable upload: %d, want 400", code)
	}
	if code, body := uploadFieldFile(t, filesURL, "../../evil.txt", "text/plain", "x"); code != http.StatusOK || !strings.Contains(body, `"evil.txt"`) {
		t.Errorf("path-bearing filename: %d %s, want only the base name kept", code, body)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "evil.txt")); err == nil {
		t.Error("an upload escaped the field directory")
	}
	if code, _ := uploadFieldFile(t, filesURL, ".hidden", "text/plain", "x"); code != http.StatusBadRequest {
		t.Errorf("dotfile upload: %d, want 400", code)
	}
	if code, _ := uploadFieldFile(t, h.url("/api/fields/nope/files"), "a.txt", "text/plain", "x"); code != http.StatusNotFound {
		t.Errorf("upload to a missing field: %d, want 404", code)
	}

	var detail struct {
		Files []FieldFile `json:"files"`
	}
	_, out := doJSON(t, "GET", h.url("/api/fields/"+p.ID), nil)
	json.Unmarshal(out, &detail)
	if len(detail.Files) != 3 {
		t.Errorf("detail lists %d files, want 3: %+v", len(detail.Files), detail.Files)
	}

	// The pool is readable through the existing workspace route, keyed by
	// the field id — no new serving code needed for the detail view's links.
	resp, err := http.Get(h.url("/api/workspace/" + p.ID + "/notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(served) != "first" {
		t.Errorf("serving a pool file: %d %q", resp.StatusCode, served)
	}

	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+p.ID+"/files/%2e%2e"), nil); code != http.StatusNotFound {
		t.Errorf("delete of ..: %d, want 404", code)
	}
	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+p.ID+"/files/notes.md"), nil); code != http.StatusNoContent {
		t.Errorf("delete file: %d, want 204", code)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, p.ID, "notes.md")); err == nil {
		t.Error("file still on disk after delete")
	}
	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+p.ID+"/files/notes.md"), nil); code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", code)
	}
}

// Deleting a field removes its shared directory and orphans its threads —
// and a thread's OWN workspace files (which never lived in the field's
// directory) are untouched.
func TestFieldsAPI_DeleteRemovesPoolButNotThreadFiles(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)
	p, _ := h.db.CreateField(store.Field{Name: "p"})
	h.db.CreateThread("t1", "kept", "m", "web")
	h.db.SetThreadField("t1", &p.ID)

	for _, f := range []string{filepath.Join(p.ID, "shared.txt"), filepath.Join("t1", "mine.txt")} {
		os.MkdirAll(filepath.Join(workspaceDir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(workspaceDir, f), []byte("x"), 0o644)
	}

	if code, _ := doJSON(t, "DELETE", h.url("/api/fields/"+p.ID), nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, p.ID)); err == nil {
		t.Error("the field's shared directory survived deletion")
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "t1", "mine.txt")); err != nil {
		t.Errorf("a thread's own file was lost with its field: %v", err)
	}
	th, err := h.db.GetThread("t1")
	if err != nil || th.FieldID != nil {
		t.Errorf("thread not orphaned cleanly: %+v err %v", th, err)
	}
}

func TestFieldsAPI_SetThreadField(t *testing.T) {
	h := newTestHarness(t, "")
	p, _ := h.db.CreateField(store.Field{Name: "p"})
	h.db.CreateThread("t1", "t", "m", "web")
	url := h.url("/api/threads/t1/field")

	if code, _ := doJSON(t, "PUT", url, map[string]interface{}{"field_id": p.ID}); code != http.StatusNoContent {
		t.Fatalf("move in: %d", code)
	}
	if th, _ := h.db.GetThread("t1"); th.FieldID == nil || *th.FieldID != p.ID {
		t.Errorf("thread not in field after move: %+v", th)
	}
	if code, _ := doJSON(t, "PUT", url, map[string]interface{}{"field_id": "gone"}); code != http.StatusNotFound {
		t.Errorf("move to a missing field: %d, want 404", code)
	}
	if code, _ := doJSON(t, "PUT", h.url("/api/threads/nope/field"), map[string]interface{}{"field_id": p.ID}); code != http.StatusNotFound {
		t.Errorf("move a missing thread: %d, want 404", code)
	}
	if code, _ := doJSON(t, "PUT", url, map[string]interface{}{"field_id": nil}); code != http.StatusNoContent {
		t.Fatalf("move out: %d", code)
	}
	if th, _ := h.db.GetThread("t1"); th.FieldID != nil {
		t.Errorf("thread still in a field after move-out: %v", *th.FieldID)
	}
}
