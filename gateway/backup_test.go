package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"polaris/backup"
)

// TestHandleServerBusy_ReflectsInFlightBackup covers the gap this closed: a
// --force-recreate during a backup run strands either a partially-written
// snapshot or a half-uploaded R2 mirror, so /api/busy has to report busy for
// one too. Both halves of a run sit under the same flag (see
// backup.runInProgress), so claiming it directly is the whole check.
func TestHandleServerBusy_ReflectsInFlightBackup(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	getBusy := func() bool {
		resp, err := http.Get(h.url("/api/busy"))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		var body struct {
			Busy bool `json:"busy"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return body.Busy
	}

	if getBusy() {
		t.Error("busy = true with no backup running, want false")
	}

	release, ok := backup.TryStart()
	if !ok {
		t.Fatal("backup.TryStart returned ok=false, want it to claim the run")
	}
	defer release()
	if !getBusy() {
		t.Error("busy = false during a backup run, want true")
	}

	release()
	if getBusy() {
		t.Error("busy = true after the backup run finished, want false")
	}
}

// TestHandleCreateBackup_RejectsConcurrentRun covers the other half of the
// single-flight flag: POST /api/backup while a run is already in progress must
// be refused rather than racing it over the same destination filename.
func TestHandleCreateBackup_RejectsConcurrentRun(t *testing.T) {
	h := newTestHarness(t, "")

	release, ok := backup.TryStart()
	if !ok {
		t.Fatal("backup.TryStart returned ok=false, want it to claim the run")
	}
	defer release()

	resp, err := http.Post(h.url("/api/backup"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/backup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 while another backup run is in progress", resp.StatusCode)
	}

	// Nothing should have been written, since the handler bailed before Create.
	infos, err := backup.List(h.srvObj.liveConfig().Backup.Dir)
	if err != nil {
		t.Fatalf("listing backups: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("got %d backups, want 0 when the request was refused", len(infos))
	}
}

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

func TestHandleListBackups_RemoteWithoutR2ConfiguredReturns400(t *testing.T) {
	h := newTestHarness(t, "")

	resp, err := http.Get(h.url("/api/backup?remote=1"))
	if err != nil {
		t.Fatalf("GET /api/backup?remote=1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (r2 isn't configured in the test config)", resp.StatusCode)
	}
}
