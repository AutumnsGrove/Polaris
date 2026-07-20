package procmgr

import (
	"runtime"
	"testing"
)

func TestNew_DispatchesByPlatform(t *testing.T) {
	mgr, err := New("polaris-test")
	switch runtime.GOOS {
	case "darwin":
		if err != nil || mgr.Name() != "launchd" {
			t.Errorf("New() on darwin = (%v, %v), want a launchd manager", mgr, err)
		}
	case "linux":
		if err != nil || mgr.Name() != "systemd" {
			t.Errorf("New() on linux = (%v, %v), want a systemd manager", mgr, err)
		}
	default:
		if err == nil {
			t.Errorf("New() on unsupported platform %s should error, got manager %v", runtime.GOOS, mgr)
		}
	}
}

func TestCurrentUsername_NeverEmpty(t *testing.T) {
	// Whatever the environment, this must return something usable as a
	// unit-file User= field, never an empty string.
	if got := currentUsername(); got == "" {
		t.Error("currentUsername() returned empty string")
	}
}
