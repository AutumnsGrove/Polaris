package gateway

import "testing"

func TestUpdateStatus_TryStartRejectsWhileRunning(t *testing.T) {
	var s updateStatus

	if !s.tryStart() {
		t.Fatal("first tryStart() = false, want true (nothing running yet)")
	}
	if s.tryStart() {
		t.Fatal("second tryStart() = true, want false — an update is already running")
	}

	snap := s.snapshot()
	if snap["running"] != true {
		t.Errorf("snapshot()[running] = %v, want true", snap["running"])
	}
	if snap["done"] != false {
		t.Errorf("snapshot()[done] = %v, want false while still running", snap["done"])
	}
}

func TestUpdateStatus_FinishClearsRunningAndRecordsOutcome(t *testing.T) {
	var s updateStatus
	s.tryStart()
	s.finish(true, "pull ok\nbuild ok", "", true)

	snap := s.snapshot()
	if snap["running"] != false {
		t.Errorf("running = %v, want false after finish", snap["running"])
	}
	if snap["done"] != true || snap["success"] != true {
		t.Errorf("done/success = %v/%v, want true/true", snap["done"], snap["success"])
	}
	if snap["restarting"] != true {
		t.Errorf("restarting = %v, want true", snap["restarting"])
	}

	// A finished (non-running) update must be startable again — e.g. the
	// next self-update, once this one's fully wrapped up.
	if !s.tryStart() {
		t.Fatal("tryStart() after finish() = false, want true")
	}
}

func TestUpdateStatus_FinishRecordsFailure(t *testing.T) {
	var s updateStatus
	s.tryStart()
	s.finish(false, "pull ok\nbuild failed", "go build failed: exit status 1", false)

	snap := s.snapshot()
	if snap["success"] != false {
		t.Errorf("success = %v, want false", snap["success"])
	}
	if snap["error"] != "go build failed: exit status 1" {
		t.Errorf("error = %v, want the build failure message", snap["error"])
	}
}
