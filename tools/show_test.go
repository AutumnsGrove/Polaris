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
	if !strings.Contains(result, "path is required") {
		t.Errorf("result = %q, want a path-required error", result)
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
