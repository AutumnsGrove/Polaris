package store

import (
	"math"
	"path/filepath"
	"testing"
)

// tableExists reports whether name is a table in s's database.
func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&n); err != nil {
		t.Fatalf("checking for table %s: %v", name, err)
	}
	return n > 0
}

// wantMonthCost fails unless the monthly cap total is want. The cap is the
// reason the rename needs care: a ledger that came through empty would read 0
// here and silently disable the spend limit.
func wantMonthCost(t *testing.T, s *Store, want float64) {
	t.Helper()
	got, err := s.DecisionsCostThisMonth()
	if err != nil {
		t.Fatalf("DecisionsCostThisMonth: %v", err)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("DecisionsCostThisMonth = %v, want %v", got, want)
	}
}

// A database from before issue #184 keeps its spend ledger under the old name,
// jev_usage. Open must carry every row over — cost AND source, so the stats
// breakouts survive too — and not leave a second, empty ledger behind. The
// legacy shape is built by renaming a current database's table back, which is
// exactly what the old schema was.
func TestOpen_RenamesLegacyJevUsageToDecisionsUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, log := range []func(float64) error{s.LogDecisionsCost, s.LogOracleDecisionsCost, s.LogCompareDecisionsCost} {
		if err := log(0.002); err != nil {
			t.Fatalf("logging spend: %v", err)
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE decisions_usage RENAME TO jev_usage`); err != nil {
		t.Fatalf("building legacy shape: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a legacy database: %v", err)
	}
	defer s.Close()

	if tableExists(t, s, "jev_usage") {
		t.Error("jev_usage still exists after Open, want it renamed away")
	}
	wantMonthCost(t, s, 0.006)
	stats, err := s.GetStats(30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if got := stats.DecisionCalls; got.Oracle.Total != 1 || got.Verification.Total != 1 || got.Compare.Total != 1 {
		t.Errorf("DecisionCalls = %+v, want one call of each source carried over", got)
	}
	// New spend lands in the same ledger, on top of the carried-over rows.
	if err := s.LogDecisionsCost(0.004); err != nil {
		t.Fatalf("LogDecisionsCost after rename: %v", err)
	}
	wantMonthCost(t, s, 0.010)
}

// Rolling back to an image older than the rename and upgrading again leaves
// BOTH tables: the old image's schema re-created an empty jev_usage and logged
// whatever it spent into it. Those rows must be folded in, not dropped — the
// monthly cap has to count money spent while rolled back.
func TestOpen_FoldsJevUsageLeftByARolledBackImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rolledback.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.LogDecisionsCost(0.001); err != nil {
		t.Fatalf("LogDecisionsCost: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE jev_usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		cost_usd REAL NOT NULL,
		source TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("building the old image's ghost table: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO jev_usage (cost_usd, source) VALUES (0.004, 'oracle')`); err != nil {
		t.Fatalf("seeding spend logged while rolled back: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("upgrading again: %v", err)
	}
	defer s.Close()

	if tableExists(t, s, "jev_usage") {
		t.Error("jev_usage still exists after folding, want it dropped")
	}
	wantMonthCost(t, s, 0.005)
	stats, err := s.GetStats(30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.DecisionCalls.Oracle.Total != 1 {
		t.Errorf("Oracle calls = %d, want the folded row's source preserved", stats.DecisionCalls.Oracle.Total)
	}
}

// Open runs on every start, so the rename must be a no-op once done — a second
// pass that re-folded or re-created anything would double-count spend.
func TestOpen_DecisionsUsageRenameIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if tableExists(t, s, "jev_usage") {
		t.Error("a brand-new database created jev_usage, want only decisions_usage")
	}
	if err := s.LogOracleDecisionsCost(0.003); err != nil {
		t.Fatalf("LogOracleDecisionsCost: %v", err)
	}
	s.Close()

	for i := 0; i < 3; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if tableExists(t, s, "jev_usage") {
			t.Errorf("reopen %d re-created jev_usage", i)
		}
		wantMonthCost(t, s, 0.003)
		s.Close()
	}
}
