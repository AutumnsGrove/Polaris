package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHandleExportMemories(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateMemory("user-timezone", "user", "the user's timezone", "US/Pacific", "2026-01-15"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	if err := h.db.CreateMemory("feedback-terse", "feedback", "keep replies short", "the user prefers terse replies", ""); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	resp, err := http.Get(h.url("/api/memories/export"))
	if err != nil {
		t.Fatalf("GET /api/memories/export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	text := string(body)

	// Dated memory keeps its real date; undated falls back to "unknown" —
	// the same convention memory_import_system's dump format uses, so a
	// round trip through export then import reads naturally.
	if !strings.Contains(text, "[2026-01-15] user-timezone: the user's timezone") {
		t.Errorf("export = %q, want the dated memory formatted with its occurred_at", text)
	}
	if !strings.Contains(text, "US/Pacific") {
		t.Errorf("export = %q, want the full content included, not just the description", text)
	}
	if !strings.Contains(text, "[unknown] feedback-terse:") {
		t.Errorf("export = %q, want the undated memory labeled unknown", text)
	}
	if !strings.Contains(text, "## User") || !strings.Contains(text, "## Feedback") {
		t.Errorf("export = %q, want type section headers", text)
	}
}

func TestHandleExportMemories_Empty(t *testing.T) {
	h := newTestHarness(t, "")
	resp, err := http.Get(h.url("/api/memories/export"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 even with nothing saved", resp.StatusCode)
	}
}
