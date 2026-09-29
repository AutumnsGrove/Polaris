package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleShow_PathRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleShow(`{}`, ctx, "call-1")
	if !strings.Contains(result, "pass exactly one of path, url, or image_indices") {
		t.Errorf("result = %q, want an exactly-one-source error", result)
	}
}

func TestHandleShow_MoreThanOneSourceRejected(t *testing.T) {
	ctx := newTestContext()
	result := handleShow(`{"path":"a.png","url":"https://example.com/a.jpg"}`, ctx, "call-1")
	if !strings.Contains(result, "pass exactly one of") {
		t.Errorf("result = %q, want an exactly-one-source error", result)
	}
}

func TestHandleShow_RemoteURL(t *testing.T) {
	ctx := newTestContext()
	var got map[string]interface{}
	ctx.Emit = func(event string, payload map[string]interface{}) {
		if event == "tool_result" {
			got = payload
		}
	}
	result := handleShow(`{"url":"https://example.com/a.jpg","caption":"the cat"}`, ctx, "call-1")
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want success", result)
	}
	if got["url"] != "https://example.com/a.jpg" || got["caption"] != "the cat" {
		t.Errorf("tool_result payload = %+v, want the remote url and caption passed straight through", got)
	}
	if url, _ := ctx.ShowSnapshot(); url != "https://example.com/a.jpg" {
		t.Errorf("ShowSnapshot url = %q, want the remote url (Pulsar Daily reads it back)", url)
	}
}

func TestHandleShow_RemoteURLRejectsNonHTTP(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:image/png;base64,AAAA", "/api/workspace/x/y.png", "ftp://example.com/a.jpg"} {
		result := handleShow(`{"url":"`+bad+`"}`, newTestContext(), "call-1")
		if !strings.Contains(result, "absolute http(s) URL") {
			t.Errorf("url %q: result = %q, want it rejected", bad, result)
		}
	}
}

func TestHandleShow_ImageIndicesShowsOnlyThePickedSubset(t *testing.T) {
	ctx := newTestContext()
	for _, n := range []string{"a", "b", "c", "d"} {
		ctx.AddImageCandidate(Card{Title: n, URL: "https://example.com/" + n, ImageURL: "https://example.com/" + n + ".jpg", Kind: "image"})
	}
	var got map[string]interface{}
	ctx.Emit = func(event string, payload map[string]interface{}) {
		if event == "tool_result" {
			got = payload
		}
	}

	result := handleShow(`{"image_indices":[4,2,2]}`, ctx, "call-1")

	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want success", result)
	}
	images, _ := got["images"].([]Card)
	if len(images) != 2 || images[0].Title != "b" || images[1].Title != "d" {
		t.Errorf("images = %+v, want exactly candidates 2 and 4, de-duplicated and in order", images)
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v; show renders inline off its own event and must not also feed the end-of-turn gallery", ctx.Cards)
	}
}

func TestHandleShow_ImageIndicesOutOfRangeAndTooMany(t *testing.T) {
	ctx := newTestContext()
	ctx.AddImageCandidate(Card{Title: "a", URL: "https://example.com/a", ImageURL: "https://example.com/a.jpg"})
	if result := handleShow(`{"image_indices":[3]}`, ctx, "call-1"); !strings.Contains(result, "out of range") {
		t.Errorf("result = %q, want an out-of-range error", result)
	}
	if result := handleShow(`{"image_indices":[1,2,3,4,5,6,7,8,9]}`, ctx, "call-1"); !strings.Contains(result, "too many images") {
		t.Errorf("result = %q, want a too-many error (reject, don't silently truncate)", result)
	}
}

func TestHandleShow_NoWorkspaceConfigured(t *testing.T) {
	ctx := newTestContext() // CodeExecWorkspaceDir/ThreadID left unset
	result := handleShow(`{"path":"chart.png"}`, ctx, "call-1")
	if !strings.Contains(result, "no code-execution workspace configured") {
		t.Errorf("result = %q, want a no-workspace-configured error", result)
	}
}

func TestHandleShow_FileNotFound(t *testing.T) {
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	result := handleShow(`{"path":"missing.png"}`, ctx, "call-1")
	if !strings.Contains(result, `no file "missing.png" in this conversation's workspace`) {
		t.Errorf("result = %q, want a file-not-found error", result)
	}
}

func TestHandleShow_PathEscapesWorkspace(t *testing.T) {
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	result := handleShow(`{"path":"../../etc/passwd"}`, ctx, "call-1")
	if !strings.Contains(result, "escapes the workspace directory") {
		t.Errorf("result = %q, want a path-escape error", result)
	}
}

func TestHandleShow_Success(t *testing.T) {
	workspaceRoot := t.TempDir()
	threadDir := filepath.Join(workspaceRoot, "thread-1")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(threadDir, "chart.png"), []byte("\x89PNG\r\n\x1a\n-fake-"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = workspaceRoot
	ctx.ThreadID = "thread-1"

	var gotEvents []map[string]interface{}
	ctx.Emit = func(event string, data map[string]interface{}) {
		gotEvents = append(gotEvents, map[string]interface{}{"event": event, "data": data})
	}

	result := handleShow(`{"path":"chart.png","caption":"a bar chart"}`, ctx, "call-1")
	if !strings.Contains(result, "chart.png") {
		t.Errorf("result = %q, want it to mention chart.png", result)
	}

	var toolResult map[string]interface{}
	for _, e := range gotEvents {
		if e["event"] == "tool_result" {
			toolResult = e["data"].(map[string]interface{})
		}
	}
	if toolResult == nil {
		t.Fatal("no tool_result event was emitted")
	}
	if toolResult["caption"] != "a bar chart" {
		t.Errorf("tool_result caption = %v, want %q", toolResult["caption"], "a bar chart")
	}
	url, _ := toolResult["url"].(string)
	if !strings.Contains(url, "thread-1") || !strings.Contains(url, "chart.png") {
		t.Errorf("tool_result url = %q, want it to reference thread-1 and chart.png", url)
	}
}

func TestHandleShow_NoCaption(t *testing.T) {
	workspaceRoot := t.TempDir()
	threadDir := filepath.Join(workspaceRoot, "thread-1")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(threadDir, "chart.png"), []byte("\x89PNG\r\n\x1a\n-fake-"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = workspaceRoot
	ctx.ThreadID = "thread-1"

	result := handleShow(`{"path":"chart.png"}`, ctx, "call-1")
	if !strings.Contains(result, "chart.png") {
		t.Errorf("result = %q, want it to mention chart.png", result)
	}
}
