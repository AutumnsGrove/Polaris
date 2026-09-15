package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polaris/config"
	"polaris/models"
)

// minimalTestPDFBase64 is the same byte-accurate minimal single-page PDF
// fixture tools/web_read_test.go uses — ledongthuc/pdf needs a real xref
// table and trailer, not just a "%PDF" header.
const minimalTestPDFBase64 = "JVBERi0xLjEKJcKlwrHDqwoKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCgoyIDAgb2JqCjw8IC9UeXBlIC9QYWdlcyAvS2lkcyBbMyAwIFJdIC9Db3VudCAxIC9NZWRpYUJveCBbMCAwIDMwMCAxNDRdID4+CmVuZG9iagoKMyAwIG9iago8PCAvVHlwZSAvUGFnZSAvUGFyZW50IDIgMCBSIC9SZXNvdXJjZXMgPDwgL0ZvbnQgPDwgL0YxIDw8IC9UeXBlIC9Gb250IC9TdWJ0eXBlIC9UeXBlMSAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuID4+ID4+ID4+IC9Db250ZW50cyA0IDAgUiA+PgplbmRvYmoKCjQgMCBvYmoKPDwgL0xlbmd0aCAzOSA+PgpzdHJlYW0KQlQgL0YxIDE4IFRmIDAgMCBUZCAoSGVsbG8gV29ybGQpIFRqIEVUCmVuZHN0cmVhbQplbmRvYmoKCnhyZWYKMCA1CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAxOCAwMDAwMCBuIAowMDAwMDAwMDY4IDAwMDAwIG4gCjAwMDAwMDAxNTAgMDAwMDAgbiAKMDAwMDAwMDMwNCAwMDAwMCBuIAp0cmFpbGVyCjw8IC9Sb290IDEgMCBSIC9TaXplIDUgPj4Kc3RhcnR4cmVmCjM5NAolJUVPRg=="

func mustDecodePDF(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(minimalTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding fixture PDF: %v", err)
	}
	return data
}

func TestResolveAttachments_NoAttachmentsPassesContentThrough(t *testing.T) {
	cfg := &config.Config{}
	got, resolved := resolveAttachments(cfg, ClientMessage{Content: "hello"}, "thread-1", nil)
	if got != "hello" {
		t.Errorf("got %q, want unchanged content", got)
	}
	if resolved != nil {
		t.Errorf("resolved = %+v, want nil with no attachments", resolved)
	}
}

func TestResolveOneAttachment_InvalidIDIsRejected(t *testing.T) {
	cfg := &config.Config{}
	_, err := resolveOneAttachment(cfg, AttachmentRef{ID: "../../etc/passwd"}, "thread-1")
	if err == nil {
		t.Fatal("expected an error for a non-UUID attachment id")
	}
}

// TestResolveAttachment_MovesFileIntoThreadWorkspace is the core behavior
// this unification exists for: an upload no longer gets read once and
// deleted (see docs/plans/workspace-store-unification.md) — it moves into
// the thread's persistent code_exec workspace directory under a freshly
// generated short ID, the same directory code_exec/fetch_url already
// write into, so a later turn's code_exec call can still open it.
func TestResolveAttachments_MovesFileIntoThreadWorkspace(t *testing.T) {
	stagingDir := t.TempDir()
	workspaceDir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440001"
	if err := os.WriteFile(filepath.Join(stagingDir, id), []byte("pdf-bytes-stand-in"), 0o644); err != nil {
		t.Fatalf("writing staged upload: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = stagingDir
	cfg.CodeExec.WorkspaceDir = workspaceDir

	msg := ClientMessage{
		Content:     "summarize this",
		Attachments: []AttachmentRef{{ID: id, Filename: "report.pdf", ContentType: "application/pdf"}},
	}
	got, resolved := resolveAttachments(cfg, msg, "thread-1", func(ref AttachmentRef, err error) {
		t.Fatalf("unexpected resolve failure for %q: %v", ref.Filename, err)
	})
	if len(resolved) != 1 {
		t.Fatalf("got %d resolved attachments, want 1: %+v", len(resolved), resolved)
	}
	filename := resolved[0].WorkspaceFileID
	if filename == "" {
		t.Fatal("WorkspaceFileID is empty, want a generated workspace filename")
	}
	if !strings.HasSuffix(filename, ".pdf") {
		t.Errorf("filename = %q, want it to end in .pdf (from the original filename)", filename)
	}
	if !bytes.Contains([]byte(got), []byte("summarize this")) {
		t.Errorf("got %q, want it to still contain the original message", got)
	}
	if !bytes.Contains([]byte(got), []byte(filename)) {
		t.Errorf("got %q, want it to mention the generated filename %q", got, filename)
	}

	destPath := filepath.Join(workspaceDir, "thread-1", filename)
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("file wasn't moved into the workspace at %q: %v", destPath, err)
	}
	if string(data) != "pdf-bytes-stand-in" {
		t.Errorf("workspace file contents = %q, want the original upload's bytes", data)
	}
	if _, err := os.Stat(filepath.Join(stagingDir, id)); !os.IsNotExist(err) {
		t.Errorf("staged upload still exists (stat err = %v), want it moved, not copied", err)
	}
}

// TestResolveAttachments_NoteMentionsOriginalFilename guards a real,
// live-found gap: the model only ever saw the generated workspace
// filename (e.g. "s47cat72nw.jpg"), never the file's actual name, so it
// had no way to refer back to it the way the human who uploaded it would
// recognize — a chip reading "arrow-transparent.jpg" next to an answer
// talking about "s47cat72nw.jpg" with no visible connection between them.
// The workspace filename itself must stay a short generated ID (not the
// original name) — see shortFileIDAlphabet's doc comment on why: it has
// to be reliably retyped by the model in a tool call, which an arbitrary
// uploaded filename (spaces, unicode, long names) can't guarantee.
func TestResolveAttachments_NoteMentionsOriginalFilename(t *testing.T) {
	stagingDir := t.TempDir()
	workspaceDir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440020"
	if err := os.WriteFile(filepath.Join(stagingDir, id), []byte("bytes"), 0o644); err != nil {
		t.Fatalf("writing staged upload: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = stagingDir
	cfg.CodeExec.WorkspaceDir = workspaceDir

	msg := ClientMessage{
		Content:     "take a look at this",
		Attachments: []AttachmentRef{{ID: id, Filename: "arrow-transparent.jpg", ContentType: "image/jpeg"}},
	}
	got, resolved := resolveAttachments(cfg, msg, "thread-1", func(ref AttachmentRef, err error) {
		t.Fatalf("unexpected resolve failure: %v", err)
	})
	if len(resolved) != 1 {
		t.Fatalf("got %d resolved attachments, want 1", len(resolved))
	}
	if !strings.Contains(got, "arrow-transparent.jpg") {
		t.Errorf("got %q, want the note to mention the original filename %q", got, "arrow-transparent.jpg")
	}
	if !strings.Contains(got, resolved[0].WorkspaceFileID) {
		t.Errorf("got %q, want the note to still mention the workspace filename %q (the model's tool-call handle)", got, resolved[0].WorkspaceFileID)
	}
}

// TestResolveAttachments_MultipleFilesAllResolve covers issue #71's core
// case: several attachments on one turn, each getting its own pointer
// note and its own workspace file, not just the first one.
func TestResolveAttachments_MultipleFilesAllResolve(t *testing.T) {
	stagingDir := t.TempDir()
	workspaceDir := t.TempDir()
	idA := "550e8400-e29b-41d4-a716-446655440010"
	idB := "550e8400-e29b-41d4-a716-446655440011"
	if err := os.WriteFile(filepath.Join(stagingDir, idA), []byte("pdf-bytes"), 0o644); err != nil {
		t.Fatalf("writing staged upload A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, idB), []byte("csv-bytes"), 0o644); err != nil {
		t.Fatalf("writing staged upload B: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = stagingDir
	cfg.CodeExec.WorkspaceDir = workspaceDir

	msg := ClientMessage{
		Content: "compare these",
		Attachments: []AttachmentRef{
			{ID: idA, Filename: "report.pdf", ContentType: "application/pdf"},
			{ID: idB, Filename: "data.csv", ContentType: "text/csv"},
		},
	}
	got, resolved := resolveAttachments(cfg, msg, "thread-1", func(ref AttachmentRef, err error) {
		t.Fatalf("unexpected resolve failure for %q: %v", ref.Filename, err)
	})
	if len(resolved) != 2 {
		t.Fatalf("got %d resolved attachments, want 2: %+v", len(resolved), resolved)
	}
	for _, r := range resolved {
		if !bytes.Contains([]byte(got), []byte(r.WorkspaceFileID)) {
			t.Errorf("got %q, want it to mention %q's generated filename %q", got, r.Filename, r.WorkspaceFileID)
		}
		if _, err := os.Stat(filepath.Join(workspaceDir, "thread-1", r.WorkspaceFileID)); err != nil {
			t.Errorf("%q wasn't moved into the workspace: %v", r.Filename, err)
		}
	}
}

// TestResolveAttachments_OneFailureDoesNotDropTheOthers guards the
// partial-failure tolerance handleTurn already had for a single
// attachment (log and continue without it) — now applied per-attachment
// instead of failing (or silently losing) the rest of the turn's uploads.
func TestResolveAttachments_OneFailureDoesNotDropTheOthers(t *testing.T) {
	stagingDir := t.TempDir()
	workspaceDir := t.TempDir()
	goodID := "550e8400-e29b-41d4-a716-446655440012"
	missingID := "550e8400-e29b-41d4-a716-446655440013"
	if err := os.WriteFile(filepath.Join(stagingDir, goodID), []byte("csv-bytes"), 0o644); err != nil {
		t.Fatalf("writing staged upload: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = stagingDir
	cfg.CodeExec.WorkspaceDir = workspaceDir

	msg := ClientMessage{
		Content: "look at both",
		Attachments: []AttachmentRef{
			{ID: missingID, Filename: "gone.pdf", ContentType: "application/pdf"}, // never written to stagingDir
			{ID: goodID, Filename: "data.csv", ContentType: "text/csv"},
		},
	}
	var failed []string
	got, resolved := resolveAttachments(cfg, msg, "thread-1", func(ref AttachmentRef, err error) {
		failed = append(failed, ref.Filename)
	})
	if len(failed) != 1 || failed[0] != "gone.pdf" {
		t.Errorf("onError calls = %v, want exactly one call for gone.pdf", failed)
	}
	if len(resolved) != 1 || resolved[0].Filename != "data.csv" {
		t.Fatalf("resolved = %+v, want exactly one entry for data.csv", resolved)
	}
	if !bytes.Contains([]byte(got), []byte("data.csv")) && !bytes.Contains([]byte(got), []byte(resolved[0].WorkspaceFileID)) {
		t.Errorf("got %q, want it to mention the successfully resolved file", got)
	}
}

func TestResolveAttachments_FallsBackToContentTypeExtensionWhenFilenameHasNone(t *testing.T) {
	stagingDir := t.TempDir()
	workspaceDir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440003"
	if err := os.WriteFile(filepath.Join(stagingDir, id), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing staged upload: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = stagingDir
	cfg.CodeExec.WorkspaceDir = workspaceDir

	msg := ClientMessage{
		Content:     "what's in this photo",
		Attachments: []AttachmentRef{{ID: id, ContentType: "image/png"}},
	}
	_, resolved := resolveAttachments(cfg, msg, "thread-1", func(ref AttachmentRef, err error) {
		t.Fatalf("unexpected resolve failure: %v", err)
	})
	if len(resolved) != 1 {
		t.Fatalf("got %d resolved attachments, want 1", len(resolved))
	}
	filename := resolved[0].WorkspaceFileID
	if !strings.HasSuffix(filename, ".png") {
		t.Errorf("filename = %q, want a content-type-derived .png extension", filename)
	}
	destPath := filepath.Join(workspaceDir, "thread-1", filename)
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("file not found at %q: %v", destPath, err)
	}
}

func TestResolveOneAttachment_MissingFileReturnsError(t *testing.T) {
	cfg := &config.Config{}
	cfg.Attachments.Dir = t.TempDir()
	cfg.CodeExec.WorkspaceDir = t.TempDir()

	_, err := resolveOneAttachment(cfg, AttachmentRef{
		ID:          "550e8400-e29b-41d4-a716-446655440002",
		ContentType: "application/pdf",
	}, "thread-1")
	if err == nil {
		t.Fatal("expected an error when the attachment file doesn't exist on disk")
	}
}

// setWorkspaceDirKeepingAttachments rewrites h's config.yaml with both
// attachments.dir (h.attachmentsDir, unchanged) and code_exec.workspace_dir
// set — unlike the shared setWorkspaceDir helper (workspace_test.go),
// which drops attachments.dir back to config.Load's default since its own
// tests never need an upload's staging path to survive the rewrite.
func setWorkspaceDirKeepingAttachments(t *testing.T, h *testHarness, workspaceDir string) {
	t.Helper()
	dir := filepath.Dir(h.cfgPath)
	contents := fmt.Sprintf(`
server:
  port: 0
openrouter:
  api_key: "test-key"
  base_url: %q
database:
  path: %q
attachments:
  dir: %q
default_model: "mimo-pro"
code_exec:
  workspace_dir: %q
`, h.llmBaseURL, filepath.Join(dir, "test.db"), h.attachmentsDir, workspaceDir)
	if err := os.WriteFile(h.cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("rewriting test config: %v", err)
	}
}

func multipartUploadBody(t *testing.T, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("creating multipart part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("writing multipart data: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return body, w.FormDataContentType()
}

func TestHandleUpload_SavesFileAndReturnsID(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, contentType := multipartUploadBody(t, "report.pdf", "application/pdf", mustDecodePDF(t))
	resp, err := http.Post(h.url("/api/upload"), contentType, body)
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.ID == "" {
		t.Fatal("response has no ID")
	}
	if out.Filename != "report.pdf" {
		t.Errorf("Filename = %q, want %q", out.Filename, "report.pdf")
	}
	if out.ContentType != "application/pdf" {
		t.Errorf("ContentType = %q, want %q", out.ContentType, "application/pdf")
	}

	cfg, err := config.Load(h.cfgPath, models.Registry)
	if err != nil {
		t.Fatalf("loading test config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Attachments.Dir, out.ID)); err != nil {
		t.Errorf("uploaded file not found on disk: %v", err)
	}
}

func TestHandleUpload_RejectsUnsupportedContentType(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	body, contentType := multipartUploadBody(t, "script.sh", "application/x-sh", []byte("#!/bin/sh\necho hi"))
	resp, err := http.Post(h.url("/api/upload"), contentType, body)
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unsupported content type", resp.StatusCode)
	}
}

// TestHandleUpload_AcceptsTextAndDataFormats guards the widened allowlist
// added alongside workspace unification: an upload no longer needs to feed
// a PDF/vision pipeline to be useful — code_exec can already
// open()/pd.read_csv()/json.load() any of these directly. json/csv/txt/xml
// all resolve correctly via Go's own mime.TypeByExtension; .md specifically
// exercises extensionContentTypeOverride, since neither Go's built-in
// table nor a typical container's /etc/mime.types knows it (confirmed
// missing on the dev machine this was written on).
func TestHandleUpload_AcceptsTextAndDataFormats(t *testing.T) {
	cases := []struct {
		filename    string
		contentType string // what the "browser" sends; "" lets the server guess via extension
	}{
		{"notes.md", "application/octet-stream"}, // the case with no reliable mime.TypeByExtension answer
		{"data.json", ""},
		{"table.csv", ""},
		{"readme.txt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			h := newTestHarness(t, "http://127.0.0.1:1")
			body, multipartContentType := multipartUploadBody(t, tc.filename, tc.contentType, []byte("hello"))
			resp, err := http.Post(h.url("/api/upload"), multipartContentType, body)
			if err != nil {
				t.Fatalf("POST /api/upload: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
			}
		})
	}
}

// TestHandleTurn_MovesAttachmentIntoWorkspace supersedes the old
// TestHandleTurn_RemovesAttachmentFileAfterUse: uploads no longer get read
// once and deleted (see docs/plans/workspace-store-unification.md) — the
// staged file must be gone from the upload staging area, and land instead
// in the thread's persistent workspace directory, addressable by the short
// id resolveAttachment generated, so a later turn's code_exec call can
// still open it.
func TestHandleTurn_MovesAttachmentIntoWorkspace(t *testing.T) {
	srv := fakeLLMServer(t, "any", "here's a summary")
	h := newTestHarness(t, srv.URL)
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	// Not the shared setWorkspaceDir helper: its config.yaml rewrite omits
	// the attachments section entirely, which would silently move
	// h.attachmentsDir out from under this test (config.Load falling back
	// to its own default dir instead) — this test needs both configured
	// at once, since it exercises the handoff between them.
	setWorkspaceDirKeepingAttachments(t, h, workspaceDir)

	body, contentType := multipartUploadBody(t, "report.pdf", "application/pdf", mustDecodePDF(t))
	resp, err := http.Post(h.url("/api/upload"), contentType, body)
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer resp.Body.Close()
	var uploaded UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decoding upload response: %v", err)
	}

	stagedPath := filepath.Join(h.attachmentsDir, uploaded.ID)
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("uploaded file missing before the turn even ran: %v", err)
	}

	conn := dialWS(t, h)
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "summarize this", "model": "test-model",
		"attachments": []map[string]string{
			{"id": uploaded.ID, "filename": "report.pdf", "content_type": "application/pdf"},
		},
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)

	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Errorf("staged upload still exists (stat err = %v), want it moved out of staging", err)
	}

	var threadID string
	for _, e := range events {
		if id, ok := e["thread_id"].(string); ok && id != "" {
			threadID = id
			break
		}
	}
	if threadID == "" {
		t.Fatal("no event carried a thread_id — can't locate this turn's workspace directory")
	}

	entries, err := os.ReadDir(filepath.Join(workspaceDir, threadID))
	if err != nil {
		t.Fatalf("reading thread workspace dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("workspace dir has %d entries, want exactly 1 (the moved upload): %+v", len(entries), entries)
	}
	if got := filepath.Ext(entries[0].Name()); got != ".pdf" {
		t.Errorf("moved file's extension = %q, want .pdf (from the original filename)", got)
	}
}

func TestPruneOldAttachments_RemovesOnlyFilesOlderThanMaxAge(t *testing.T) {
	dir := t.TempDir()

	oldPath := filepath.Join(dir, "old-abandoned-upload")
	if err := os.WriteFile(oldPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing old file: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatalf("backdating old file: %v", err)
	}

	freshPath := filepath.Join(dir, "fresh-upload-about-to-be-sent")
	if err := os.WriteFile(freshPath, []byte("y"), 0o644); err != nil {
		t.Fatalf("writing fresh file: %v", err)
	}

	if err := PruneOldAttachments(dir, 24*time.Hour); err != nil {
		t.Fatalf("PruneOldAttachments returned error: %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file still exists (stat err = %v), want it pruned", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh file was removed, want it kept: %v", err)
	}
}

func TestPruneOldAttachments_MissingDirIsNotAnError(t *testing.T) {
	if err := PruneOldAttachments(filepath.Join(t.TempDir(), "does-not-exist"), 24*time.Hour); err != nil {
		t.Errorf("PruneOldAttachments returned error for a missing dir: %v", err)
	}
}
