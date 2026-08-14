package gateway

import "testing"

func TestUpdateStatus_TryStartRejectsWhileRunning(t *testing.T) {
	var s updateStatus

	if started, _ := s.tryStart("update"); !started {
		t.Fatal("first tryStart() = false, want true (nothing running yet)")
	}
	if started, _ := s.tryStart("update"); started {
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
	s.tryStart("update")
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
	if snap["restart_error"] != "" {
		t.Errorf("restart_error = %v, want empty until a restart actually fails", snap["restart_error"])
	}

	// A finished (non-running) update must be startable again — e.g. the
	// next self-update, once this one's fully wrapped up.
	if started, _ := s.tryStart("update"); !started {
		t.Fatal("tryStart() after finish() = false, want true")
	}
}

func TestUpdateStatus_SetRestartErrorSurfacesFailure(t *testing.T) {
	var s updateStatus
	_, startedAt := s.tryStart("update")
	s.finish(true, "pull ok\nbuild ok", "", true)

	// Mirrors handleUpdate's real sequence: finish() runs synchronously
	// (the build succeeded, a restart was attempted), then the restart
	// goroutine's mgr.Restart() call fails moments later, after the HTTP
	// response has already gone out reporting success.
	s.setRestartError(startedAt, "sudo systemctl restart polaris: exit status 1")

	snap := s.snapshot()
	if snap["restart_error"] != "sudo systemctl restart polaris: exit status 1" {
		t.Errorf("restart_error = %v, want the restart failure message", snap["restart_error"])
	}
	// The build itself still succeeded — only the restart failed — so
	// success/error (the build outcome) must be untouched.
	if snap["success"] != true {
		t.Errorf("success = %v, want true — the build succeeded even though the restart failed", snap["success"])
	}
}

func TestUpdateStatus_SetRestartErrorIgnoresStaleRun(t *testing.T) {
	var s updateStatus
	_, staleStartedAt := s.tryStart("update")
	s.finish(true, "pull ok\nbuild ok", "", true)

	// A second update started (and finished) before the first run's
	// restart goroutine got around to reporting its own failure — that
	// stale report must not clobber the second run's state.
	s.tryStart("update")
	s.finish(true, "pull ok\nbuild ok", "", true)

	s.setRestartError(staleStartedAt, "stale failure from the first run")

	snap := s.snapshot()
	if snap["restart_error"] != "" {
		t.Errorf("restart_error = %v, want empty — stale run's error must be ignored", snap["restart_error"])
	}
}

func TestUpdateStatus_FinishRecordsFailure(t *testing.T) {
	var s updateStatus
	s.tryStart("update")
	s.finish(false, "pull ok\nbuild failed", "go build failed: exit status 1", false)

	snap := s.snapshot()
	if snap["success"] != false {
		t.Errorf("success = %v, want false", snap["success"])
	}
	if snap["error"] != "go build failed: exit status 1" {
		t.Errorf("error = %v, want the build failure message", snap["error"])
	}
}

// TestUpdateStatus_KindDistinguishesUpdateFromRestart is a regression test
// for the settings panel's "Restart Polaris" button (handleRestart) —
// added alongside "Update Polaris" (handleUpdate). Both share this one
// updateStatus slot (see its doc comment on why: they'd otherwise be able
// to race each other's mgr.Restart() calls), so the frontend needs kind to
// tell which operation is actually in flight to show the right copy.
func TestUpdateStatus_KindDistinguishesUpdateFromRestart(t *testing.T) {
	var s updateStatus

	started, _ := s.tryStart("restart")
	if !started {
		t.Fatal("tryStart(\"restart\") = false, want true (nothing running yet)")
	}
	if snap := s.snapshot(); snap["kind"] != "restart" {
		t.Errorf("kind = %v, want %q while a restart is running", snap["kind"], "restart")
	}

	// A restart already running must block a concurrent update, and vice
	// versa — they share one slot precisely so this can't happen.
	if started, _ := s.tryStart("update"); started {
		t.Fatal("tryStart(\"update\") = true while a restart was running, want false — update and restart must be mutually exclusive")
	}

	s.finish(true, "restart requested", "", true)
	if snap := s.snapshot(); snap["kind"] != "restart" {
		t.Errorf("kind after finish = %v, want %q — finish() doesn't change which operation just ran", snap["kind"], "restart")
	}

	started, _ = s.tryStart("update")
	if !started {
		t.Fatal("tryStart(\"update\") after the restart finished = false, want true")
	}
	if snap := s.snapshot(); snap["kind"] != "update" {
		t.Errorf("kind = %v, want %q for the new run", snap["kind"], "update")
	}
}
