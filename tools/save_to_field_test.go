package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fieldTestContext returns a Context inside a field, with a real temp
// workspace root and the thread's own directory already created.
func fieldTestContext(t *testing.T) (ctx *Context, root string) {
	t.Helper()
	root = t.TempDir()
	ctx = newTestContext()
	ctx.CodeExecWorkspaceDir = root
	ctx.ThreadID = "thread-1"
	ctx.FieldID = "proj-1"
	if err := os.MkdirAll(filepath.Join(root, "thread-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return ctx, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogEntry_Offered_FieldWorkspace(t *testing.T) {
	entry := catalogEntry{Name: "save_to_field", Requires: "field_workspace"}

	ordinary := newTestContext()
	ordinary.CodeExecWorkspaceDir = "/ws"
	if entry.offered(ordinary) {
		t.Error("save_to_field offered on a thread with no field — it must be absent from the tool list, not just refused")
	}

	inField := newTestContext()
	inField.CodeExecWorkspaceDir = "/ws"
	inField.FieldID = "p"
	if !entry.offered(inField) {
		t.Error("save_to_field not offered on a field thread with a workspace")
	}

	// A field id with no workspace root configured would offer a tool that
	// can only fail.
	noWorkspace := newTestContext()
	noWorkspace.FieldID = "p"
	if entry.offered(noWorkspace) {
		t.Error("save_to_field offered with no workspace configured")
	}
}

func TestSaveToField_CopiesAndKeepsOriginal(t *testing.T) {
	ctx, root := fieldTestContext(t)
	writeFile(t, filepath.Join(root, "thread-1", "report.csv"), "a,b\n1,2\n")

	result := handleSaveToField(`{"filename":"report.csv"}`, ctx, "c1")
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("unexpected error: %s", result)
	}
	got, err := os.ReadFile(filepath.Join(root, "proj-1", "report.csv"))
	if err != nil || string(got) != "a,b\n1,2\n" {
		t.Errorf("field copy = %q, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "thread-1", "report.csv")); err != nil {
		t.Errorf("the thread's own copy should remain (copy, not move): %v", err)
	}
}

// A later promote must never destroy an earlier thread's contribution — the
// whole reason the pool is safe to share.
func TestSaveToField_NameCollisionRenamesInsteadOfOverwriting(t *testing.T) {
	ctx, root := fieldTestContext(t)
	writeFile(t, filepath.Join(root, "proj-1", "notes.md"), "ORIGINAL")
	writeFile(t, filepath.Join(root, "thread-1", "notes.md"), "second")

	result := handleSaveToField(`{"filename":"notes.md"}`, ctx, "c1")
	if !strings.Contains(result, `"notes-2.md"`) {
		t.Errorf("result should name the auto-renamed file, got %q", result)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "proj-1", "notes.md")); string(got) != "ORIGINAL" {
		t.Errorf("the original was clobbered: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "proj-1", "notes-2.md")); string(got) != "second" {
		t.Errorf("renamed copy = %q", got)
	}

	// And a third goes to -3, not back onto -2.
	writeFile(t, filepath.Join(root, "thread-1", "notes.md"), "third")
	handleSaveToField(`{"filename":"notes.md"}`, ctx, "c2")
	if got, _ := os.ReadFile(filepath.Join(root, "proj-1", "notes-3.md")); string(got) != "third" {
		t.Errorf("third copy = %q, want it in notes-3.md", got)
	}
}

func TestSaveToField_Rejections(t *testing.T) {
	ctx, root := fieldTestContext(t)
	writeFile(t, filepath.Join(root, "thread-1", "ok.txt"), "x")
	writeFile(t, filepath.Join(root, "secret.txt"), "outside the thread's dir")
	writeFile(t, filepath.Join(root, "proj-1", "shared.txt"), "already shared")
	if err := os.Mkdir(filepath.Join(root, "thread-1", "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The sandbox can plant a symlink whose target the HOST resolves against
	// the host filesystem — following it would copy a host file into the pool.
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(root, "thread-1", "link.txt")); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, filename string }{
		{"traversal", "../secret.txt"},
		{"symlink", "link.txt"},
		{"directory", "adir"},
		{"missing", "nope.txt"},
		{"empty", ""},
		// Reading falls back to the field dir, but promoting must not:
		// copying a shared file onto itself under a new name is never intended.
		{"file that only exists in the field", "shared.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"filename": c.filename})
			if result := handleSaveToField(string(args), ctx, "c"); !strings.HasPrefix(result, "error:") {
				t.Errorf("got %q, want an error", result)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "proj-1", "secret.txt")); err == nil {
		t.Error("a traversal/symlink attempt leaked a file into the field pool")
	}
	if _, err := os.Stat(filepath.Join(root, "proj-1", "link.txt")); err == nil {
		t.Error("a symlink was followed into the field pool")
	}
}

func TestSaveToField_NotInAField(t *testing.T) {
	ctx, _ := fieldTestContext(t)
	ctx.FieldID = ""
	if result := handleSaveToField(`{"filename":"x"}`, ctx, "c"); !strings.HasPrefix(result, "error:") {
		t.Errorf("got %q, want an error when the thread has no field", result)
	}
}

func TestResolveWorkspaceFilePath_FieldFallback(t *testing.T) {
	ctx, root := fieldTestContext(t)
	writeFile(t, filepath.Join(root, "proj-1", "shared.csv"), "shared")
	writeFile(t, filepath.Join(root, "proj-1", "both.txt"), "shared version")
	writeFile(t, filepath.Join(root, "thread-1", "both.txt"), "own version")

	got, err := resolveWorkspaceFilePath(ctx, "shared.csv")
	if err != nil || got != filepath.Join(root, "proj-1", "shared.csv") {
		t.Errorf("shared file: got %q, err %v, want the field's path", got, err)
	}
	// The thread's own file shadows the shared original of the same name.
	got, err = resolveWorkspaceFilePath(ctx, "both.txt")
	if err != nil || got != filepath.Join(root, "thread-1", "both.txt") {
		t.Errorf("own-vs-shared: got %q, err %v, want the thread's own path", got, err)
	}
	if _, err := resolveWorkspaceFilePath(ctx, "../secret"); err == nil {
		t.Error("traversal out of the workspace was allowed")
	}

	// An ordinary thread must not see a field's files at all, even when a
	// field directory happens to exist beside it.
	ctx.FieldID = ""
	if _, err := resolveWorkspaceFilePath(ctx, "shared.csv"); err == nil {
		t.Error("a non-field thread resolved a file out of a field directory")
	}
}

func TestCodeExecRequest_FieldDirOmittedWhenEmpty(t *testing.T) {
	b, _ := json.Marshal(codeExecRequest{ID: "x", HostWorkspaceDir: "/h/t"})
	if strings.Contains(string(b), "field_host_workspace_dir") {
		t.Errorf("an ordinary request should carry no field field at all: %s", b)
	}
	b, _ = json.Marshal(codeExecRequest{ID: "x", HostWorkspaceDir: "/h/t", FieldHostWorkspaceDir: "/h/p"})
	if !strings.Contains(string(b), `"field_host_workspace_dir":"/h/p"`) {
		t.Errorf("field request missing its field (codeexec.sh reads this exact key): %s", b)
	}
}
