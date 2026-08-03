package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHandleHealthz_OK(t *testing.T) {
	h := newTestHarness(t, "")

	resp, err := http.Get(h.url("/healthz"))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want %q", body["status"], "ok")
	}
}

func TestHandleHealthz_DBClosed(t *testing.T) {
	h := newTestHarness(t, "")
	h.db.Close() // simulate a dead/unreachable database

	resp, err := http.Get(h.url("/healthz"))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["status"] != "error" {
		t.Errorf("status field = %q, want %q", body["status"], "error")
	}
}
