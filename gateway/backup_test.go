package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"polaris/backup"
)

func TestHandleCreateBackup_CreatesAListableBackup(t *testing.T) {
	h := newTestHarness(t, "")

	resp, err := http.Post(h.url("/api/backup"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/backup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var info backup.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if info.Name == "" {
		t.Error("Name is empty, want a real backup filename")
	}
	if info.SizeBytes == 0 {
		t.Error("SizeBytes = 0, want a real file size")
	}

	listResp, err := http.Get(h.url("/api/backup"))
	if err != nil {
		t.Fatalf("GET /api/backup: %v", err)
	}
	defer listResp.Body.Close()
	var infos []backup.Info
	if err := json.NewDecoder(listResp.Body).Decode(&infos); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != info.Name {
		t.Errorf("list = %+v, want exactly the backup just created (%s)", infos, info.Name)
	}
}

func TestHandleListBackups_EmptyBeforeAnyBackup(t *testing.T) {
	h := newTestHarness(t, "")

	resp, err := http.Get(h.url("/api/backup"))
	if err != nil {
		t.Fatalf("GET /api/backup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var infos []backup.Info
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("got %d backups, want 0 before any have been taken", len(infos))
	}
}
