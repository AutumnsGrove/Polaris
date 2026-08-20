package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

func TestResolveAttachment_NoAttachmentPassesContentThrough(t *testing.T) {
	cfg := &config.Config{}
	got, data, cost, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, ClientMessage{Content: "hello"}, nil)
	if err != nil {
		t.Fatalf("resolveAttachment returned error: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want unchanged content", got)
	}
	if data != nil {
		t.Errorf("attachmentData = %v, want nil with no attachment", data)
	}
	if cost != 0 {
		t.Errorf("cost = %v, want 0 with no attachment", cost)
	}
}

func TestResolveAttachment_InvalidIDIsRejected(t *testing.T) {
	cfg := &config.Config{}
	_, _, _, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, ClientMessage{Content: "hi", AttachmentID: "../../etc/passwd"}, nil)
	if err == nil {
		t.Fatal("expected an error for a non-UUID attachment id")
	}
}

func TestResolveAttachment_PDFTextIsAppended(t *testing.T) {
	dir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440001"
	if err := os.WriteFile(filepath.Join(dir, id), mustDecodePDF(t), 0o644); err != nil {
		t.Fatalf("writing fake pdf: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = dir

	msg := ClientMessage{
		Content:               "summarize this",
		AttachmentID:          id,
		AttachmentFilename:    "report.pdf",
		AttachmentContentType: "application/pdf",
	}
	got, data, cost, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, msg, nil)
	if err != nil {
		t.Fatalf("resolveAttachment returned error: %v", err)
	}
	if !bytes.Contains([]byte(got), []byte("summarize this")) {
		t.Errorf("got %q, want it to still contain the original message", got)
	}
	if !bytes.Contains([]byte(got), []byte("[Attached file: report.pdf]")) {
		t.Errorf("got %q, want it to name the attached file", got)
	}
	if !bytes.Contains([]byte(got), []byte("Hello World")) {
		t.Errorf("got %q, want it to contain the PDF's actual extracted text", got)
	}
	if len(data) == 0 {
		t.Error("attachmentData is empty, want the raw PDF bytes for read_attachment to use")
	}
	if cost != 0 {
		t.Errorf("cost = %v, want 0 for a PDF (no model call)", cost)
	}
}

func TestResolveAttachment_MissingFileReturnsError(t *testing.T) {
	cfg := &config.Config{}
	cfg.Attachments.Dir = t.TempDir()

	_, _, _, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, ClientMessage{
		Content:               "hi",
		AttachmentID:          "550e8400-e29b-41d4-a716-446655440002",
		AttachmentContentType: "application/pdf",
	}, nil)
	if err == nil {
		t.Fatal("expected an error when the attachment file doesn't exist on disk")
	}
}

func TestResolveAttachment_ImageIsDescribedByMultimodalModel(t *testing.T) {
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"A red bicycle leaning against a brick wall."}}],"usage":{"cost":0.002}}`))
	}))
	defer visionSrv.Close()

	dir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440003"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing fake image: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = dir
	cfg.OpenRouter.BaseURL = visionSrv.URL
	cfg.Models = []config.ModelConfig{
		{ID: "mimo", Name: "MiMo", Model: "xiaomi/mimo-v2.5", Multimodal: true},
	}

	msg := ClientMessage{
		Content:               "what's in this photo",
		AttachmentID:          id,
		AttachmentFilename:    "bike.jpg",
		AttachmentContentType: "image/jpeg",
	}
	// The selected model (config.ModelConfig{}, i.e. not multimodal) can't see
	// the image itself, so this exercises the cfg.MultimodalModel() fallback.
	got, _, cost, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, msg, nil)
	if err != nil {
		t.Fatalf("resolveAttachment returned error: %v", err)
	}
	if !bytes.Contains([]byte(got), []byte("what's in this photo")) {
		t.Errorf("got %q, want it to still contain the original message", got)
	}
	if !bytes.Contains([]byte(got), []byte("red bicycle")) {
		t.Errorf("got %q, want it to contain the vision model's description", got)
	}
	if cost != 0.002 {
		t.Errorf("cost = %v, want 0.002 from the vision model's usage.cost", cost)
	}
}

// TestResolveAttachment_MultimodalSelectedModelDescribesItsOwnImage guards
// against the routing gap found in review: resolveAttachment used to always
// hand image description off to cfg.MultimodalModel() (the first
// Multimodal-flagged entry in the registry), even when the thread's own
// selected model was itself vision-capable — so adding a second multimodal
// model to the registry would silently never be preferred for its own
// thread. The selected model must win when it's multimodal itself.
func TestResolveAttachment_MultimodalSelectedModelDescribesItsOwnImage(t *testing.T) {
	var requestedModel string
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding vision request body: %v", err)
		}
		requestedModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"A red bicycle leaning against a brick wall."}}],"usage":{"cost":0.002}}`))
	}))
	defer visionSrv.Close()

	dir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440005"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing fake image: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = dir
	cfg.OpenRouter.BaseURL = visionSrv.URL
	// A different multimodal model sits earlier in the registry than the
	// one actually selected for this thread — if the old first-match
	// fallback fired, the request would go to "registry-mimo" instead.
	cfg.Models = []config.ModelConfig{
		{ID: "registry-mimo", Name: "Registry MiMo", Model: "xiaomi/mimo-v2.5", Multimodal: true},
	}
	selectedModel := config.ModelConfig{ID: "luna-vision", Name: "Luna Vision", Model: "openai/luna-vision", Multimodal: true}

	msg := ClientMessage{
		Content:               "what's in this photo",
		AttachmentID:          id,
		AttachmentFilename:    "bike.jpg",
		AttachmentContentType: "image/jpeg",
	}
	got, _, _, err := resolveAttachment(context.Background(), cfg, selectedModel, msg, nil)
	if err != nil {
		t.Fatalf("resolveAttachment returned error: %v", err)
	}
	if !bytes.Contains([]byte(got), []byte("red bicycle")) {
		t.Errorf("got %q, want it to contain the vision model's description", got)
	}
	if requestedModel != selectedModel.Model {
		t.Errorf("requestedModel = %q, want the selected model (%q), not a registry fallback", requestedModel, selectedModel.Model)
	}
}

// TestResolveAttachment_ImageEmitsSyntheticToolCall guards the fix for a
// found-in-the-wild UX gap: describing an image runs entirely before
// agent.Run even starts, so the frontend showed nothing at all — not even
// a spinner — for however long the vision-model call took. resolveAttachment
// now wraps that call in a synthetic tool_call/tool_result pair (tool name
// "describe_image") through the same emit callback handleTurn uses for
// real tool calls, so it shows up on the timeline instead of a blank wait.
func TestResolveAttachment_ImageEmitsSyntheticToolCall(t *testing.T) {
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"A red bicycle leaning against a brick wall."}}],"usage":{"cost":0.002}}`))
	}))
	defer visionSrv.Close()

	dir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440006"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing fake image: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = dir
	cfg.OpenRouter.BaseURL = visionSrv.URL
	cfg.Models = []config.ModelConfig{
		{ID: "mimo", Name: "MiMo", Model: "xiaomi/mimo-v2.5", Multimodal: true},
	}

	msg := ClientMessage{
		Content:               "what's in this photo",
		AttachmentID:          id,
		AttachmentFilename:    "bike.jpg",
		AttachmentContentType: "image/jpeg",
	}

	var events []map[string]interface{}
	emit := func(eventType string, payload map[string]interface{}) {
		payload["_type"] = eventType
		events = append(events, payload)
	}

	if _, _, _, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, msg, emit); err != nil {
		t.Fatalf("resolveAttachment returned error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d emitted events, want 2 (tool_call + tool_result): %+v", len(events), events)
	}
	if events[0]["_type"] != "tool_call" || events[0]["tool"] != "describe_image" {
		t.Errorf("first event = %+v, want a describe_image tool_call", events[0])
	}
	if args, ok := events[0]["args"].(map[string]interface{}); !ok || args["filename"] != "bike.jpg" {
		t.Errorf("tool_call args = %+v, want filename bike.jpg", events[0]["args"])
	}
	if events[1]["_type"] != "tool_result" || events[1]["tool"] != "describe_image" {
		t.Errorf("second event = %+v, want a describe_image tool_result", events[1])
	}
	if result, _ := events[1]["result"].(string); !strings.Contains(result, "red bicycle") {
		t.Errorf("tool_result result = %q, want it to contain the vision model's description", result)
	}
}

func TestResolveAttachment_ImageWithNoMultimodalModelConfiguredErrors(t *testing.T) {
	dir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440004"
	if err := os.WriteFile(filepath.Join(dir, id), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing fake image: %v", err)
	}

	cfg := &config.Config{}
	cfg.Attachments.Dir = dir
	cfg.Models = []config.ModelConfig{{ID: "deepseek", Multimodal: false}}

	_, _, _, err := resolveAttachment(context.Background(), cfg, config.ModelConfig{}, ClientMessage{
		Content:               "what is this",
		AttachmentID:          id,
		AttachmentContentType: "image/png",
	}, nil)
	if err == nil {
		t.Fatal("expected an error when no multimodal model is configured")
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

// TestHandleTurn_RemovesAttachmentFileAfterUse guards against a
// found-in-audit bug: uploaded files were never deleted anywhere — not
// on thread/message deletion, and (the actual root cause) not even right
// after the one turn that used them, despite nothing ever reading the raw
// file again afterward. Every attachment ever sent used to stay on disk
// forever.
func TestHandleTurn_RemovesAttachmentFileAfterUse(t *testing.T) {
	srv := fakeLLMServer(t, "any", "here's a summary")
	h := newTestHarness(t, srv.URL)

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

	attachmentPath := filepath.Join(h.attachmentsDir, uploaded.ID)
	if _, err := os.Stat(attachmentPath); err != nil {
		t.Fatalf("uploaded file missing before the turn even ran: %v", err)
	}

	conn := dialWS(t, h)
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "summarize this", "model": "test-model",
		"attachment_id": uploaded.ID, "attachment_filename": "report.pdf", "attachment_content_type": "application/pdf",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	if _, err := os.Stat(attachmentPath); !os.IsNotExist(err) {
		t.Errorf("attachment file still exists after the turn that used it (stat err = %v), want it removed", err)
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
