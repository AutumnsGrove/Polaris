package gateway

import (
	"testing"

	"polaris/store"
)

// TestOracleGhostEnabledFromStore pins the explicit-opt-in polarity of the
// oracle_ghost_enabled setting (see its doc comment in settings.go): off
// for a nil store, unset, a read error, or any value other than "true".
func TestOracleGhostEnabledFromStore(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if OracleGhostEnabledFromStore(nil) {
		t.Error("want Oracle-in-ghost off for a nil store")
	}
	if OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost off by default (unset)")
	}
	if err := db.SetSetting(settingOracleGhostEnabled, "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost on after storing \"true\"")
	}
	if err := db.SetSetting(settingOracleGhostEnabled, "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if OracleGhostEnabledFromStore(db) {
		t.Error("want Oracle-in-ghost off after storing \"false\"")
	}
}
