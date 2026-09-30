package store

import (
	"testing"
)

func TestSettings_GetSetAndListAll(t *testing.T) {
	s := openTestStore(t)

	if v, err := s.GetSetting("theme"); err != nil || v != "" {
		t.Fatalf("GetSetting on unset key = (%q, %v), want (\"\", nil)", v, err)
	}

	if err := s.SetSetting("theme", "light"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if v, err := s.GetSetting("theme"); err != nil || v != "light" {
		t.Fatalf("GetSetting after set = (%q, %v), want (\"light\", nil)", v, err)
	}

	// Upsert: setting the same key again replaces, not duplicates.
	if err := s.SetSetting("theme", "dark"); err != nil {
		t.Fatalf("SetSetting (update): %v", err)
	}
	all, err := s.AllSettings()
	if err != nil {
		t.Fatalf("AllSettings: %v", err)
	}
	if all["theme"] != "dark" {
		t.Errorf("AllSettings()[\"theme\"] = %q, want %q", all["theme"], "dark")
	}
}
