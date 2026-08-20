package gateway

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// postAskMultipart builds and sends a multipart/form-data POST to
// /api/ask — the inline-upload path decodeAskRequest adds, mirroring what
// `curl -F file=@modelcard.pdf -F content="..."` sends. filename == ""
// means no "file" part at all (a text-only multipart request).
func postAskMultipart(t *testing.T, h *testHarness, fields map[string]string, filename, fileContentType string, fileData []byte) (*http.Response, AskResponse) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)

	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("writing field %q: %v", k, err)
		}
	}

	if filename != "" {
		part, err := w.CreatePart(map[string][]string{
			"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
			"Content-Type":        {fileContentType},
		})
		if err != nil {
			t.Fatalf("creating file part: %v", err)
		}
		if _, err := part.Write(fileData); err != nil {
			t.Fatalf("writing file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	resp, err := http.Post(h.url("/api/ask"), w.FormDataContentType(), body)
	if err != nil {
		t.Fatalf("POST /api/ask: %v", err)
	}
	defer resp.Body.Close()

	var out AskResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// TestHandleAsk_MultipartWithPDFFile_AnswersAndRemovesUpload is the core
// case this exists for: attach a file and ask a question in one HTTP call
// instead of POSTing to /api/upload first and threading its ID through
// separately.
func TestHandleAsk_MultipartWithPDFFile_AnswersAndRemovesUpload(t *testing.T) {
	srv := fakeLLMServer(t, "any", "here's a summary")
	h := newTestHarness(t, srv.URL)

	resp, out := postAskMultipart(t, h, map[string]string{
		"content": "summarize this",
		"model":   "test-model",
	}, "report.pdf", "application/pdf", mustDecodePDF(t))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Answer != "here's a summary" {
		t.Errorf("Answer = %q, want the model's answer", out.Answer)
	}
	if out.ThreadID == "" {
		t.Fatal("ThreadID is empty, want a generated thread id")
	}

	// Same cleanup guarantee as the WebSocket attachment path (see
	// TestHandleTurn_RemovesAttachmentFileAfterUse in attachments_test.go)
	// — the file saveUploadedFile wrote should be gone once the one turn
	// that consumed it finishes, not left behind on disk.
	entries, err := os.ReadDir(h.attachmentsDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading attachments dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("attachments dir has %d leftover file(s), want 0 after the turn consumed the upload", len(entries))
	}
}

// TestHandleAsk_MultipartWithoutFile_TextOnlyStillWorks confirms a
// multipart request with no "file" part behaves exactly like a plain JSON
// request — decodeAskRequest treats a missing file as "no attachment",
// not an error.
func TestHandleAsk_MultipartWithoutFile_TextOnlyStillWorks(t *testing.T) {
	srv := fakeLLMServer(t, "any", "plain answer")
	h := newTestHarness(t, srv.URL)

	resp, out := postAskMultipart(t, h, map[string]string{
		"content": "no attachment here",
		"model":   "test-model",
	}, "", "", nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Answer != "plain answer" {
		t.Errorf("Answer = %q, want the model's answer", out.Answer)
	}
}

func TestHandleAsk_MultipartUnsupportedFileType_ReturnsBadRequest(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, _ := postAskMultipart(t, h, map[string]string{"content": "hi"}, "script.sh", "application/x-sh", []byte("#!/bin/sh\necho hi"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unsupported file type", resp.StatusCode)
	}
}

func TestHandleAsk_MultipartEmptyContent_ReturnsBadRequest(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, _ := postAskMultipart(t, h, map[string]string{"content": ""}, "", "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty content", resp.StatusCode)
	}
}

func TestFormBool(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"yes", false},
	}
	for _, c := range cases {
		r, err := http.NewRequest("POST", "/", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		r.Form = url.Values{"x": {c.val}}
		if got := formBool(r, "x"); got != c.want {
			t.Errorf("formBool(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}
