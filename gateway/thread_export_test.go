package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHandleThreadExport_MarkdownDefault(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateThread("t1", "My Title", "model-x", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "What is oolong?", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	if _, err := h.db.AddMessage("t1", "assistant", "A semi-oxidized tea.", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/t1/export"))
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "my-title") || !strings.Contains(cd, ".md") {
		t.Errorf("Content-Disposition = %q, want a slugged .md attachment", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "# My Title") || !strings.Contains(string(body), "A semi-oxidized tea.") {
		t.Errorf("body missing the transcript content:\n%s", body)
	}
}

func TestHandleThreadExport_JSONWithTrail(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateThread("t1", "Traced", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "q", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	aid, err := h.db.AddMessage("t1", "assistant", "a", "[]", "[]", 0, "turn-1")
	if err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}
	if err := h.db.SetMessageTranscript(aid, `[{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"web_search","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"hits"}]`); err != nil {
		t.Fatalf("SetMessageTranscript: %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/t1/export?format=json&trail=1"))
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"polaris_export": 1`, `"web_search"`, `"trace"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("JSON missing %s:\n%s", want, body)
		}
	}
}

func TestHandleThreadExport_DryRunReturnsEstimateAndAvailability(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateThread("t1", "T", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "q", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	resp, err := http.Get(h.url("/api/threads/t1/export?dry_run=1"))
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("dry run should not set Content-Disposition, got %q", cd)
	}
	var out struct {
		Tokens       int `json:"tokens"`
		Availability struct {
			Turns int `json:"turns"`
		} `json:"availability"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding dry-run response: %v", err)
	}
	if out.Tokens <= 0 {
		t.Errorf("tokens = %d, want > 0", out.Tokens)
	}
	if out.Availability.Turns != 1 {
		t.Errorf("availability.turns = %d, want 1", out.Availability.Turns)
	}
}

func TestHandleThreadExport_UnknownAndGhostAre404(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateGhostThread("g1", "", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}

	for _, id := range []string{"nope", "g1"} {
		resp, err := http.Get(h.url("/api/threads/" + id + "/export"))
		if err != nil {
			t.Fatalf("GET export %s: %v", id, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", id, resp.StatusCode)
		}
	}
}

// TestHandleThreadExport_PrivacyDropsMemoryFromTrail exercises the whole
// path: the tool catalog's export_privacy flag reaches the builder and the
// memory call leaves the rendered file. With private off the same call is
// present, so the difference is the toggle, not a missing transcript.
func TestHandleThreadExport_PrivacyDropsMemoryFromTrail(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateThread("t1", "Privacy", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "q", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	aid, err := h.db.AddMessage("t1", "assistant", "answer", "[]", "[]", 0, "turn-1")
	if err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}
	if err := h.db.SetMessageTranscript(aid, `[{"role":"assistant","content":"","tool_calls":[{"id":"m1","function":{"name":"memory","arguments":"{\"action\":\"write\"}"}}]},{"role":"tool","tool_call_id":"m1","content":"Saved memory: secret"}]`); err != nil {
		t.Fatalf("SetMessageTranscript: %v", err)
	}

	privateResp, err := http.Get(h.url("/api/threads/t1/export?trail=1"))
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer privateResp.Body.Close()
	privateBody, _ := io.ReadAll(privateResp.Body)
	if strings.Contains(string(privateBody), "Saved memory: secret") {
		t.Errorf("private export leaked the memory tool result:\n%s", privateBody)
	}

	openResp, err := http.Get(h.url("/api/threads/t1/export?trail=1&private=0"))
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer openResp.Body.Close()
	openBody, _ := io.ReadAll(openResp.Body)
	if !strings.Contains(string(openBody), "Saved memory: secret") {
		t.Errorf("private=0 export should include the memory call:\n%s", openBody)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"What is oolong?":     "what-is-oolong",
		"  Spaces  &  More  ": "spaces-more",
		"日本語":                 "",
		"Trailing--dashes---": "trailing-dashes",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
