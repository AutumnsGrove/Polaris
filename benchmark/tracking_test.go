package benchmark

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestTrackingDB(t *testing.T) *TrackingDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tracking.db")
	db, err := OpenTracking(path)
	if err != nil {
		t.Fatalf("OpenTracking: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenTracking_CreatesSchemaOnFreshFile(t *testing.T) {
	db := openTestTrackingDB(t)
	if _, err := db.db.Exec("SELECT id, run_at, model, dataset_row, correct, verdict, brave_calls, assistant_turns, subject_cost_usd, grader_cost_usd, error FROM runs LIMIT 0"); err != nil {
		t.Errorf("runs table doesn't have the expected columns: %v", err)
	}
}

func TestRecord_StoresCorrectAsIntOrNull(t *testing.T) {
	db := openTestTrackingDB(t)

	yes := true
	no := false
	if err := db.Record(RunRecord{RunAt: "2026-01-01T00:00:00Z", Model: "m", DatasetRow: 1, Correct: &yes, Verdict: "yes"}); err != nil {
		t.Fatalf("Record (correct=true): %v", err)
	}
	if err := db.Record(RunRecord{RunAt: "2026-01-01T00:00:00Z", Model: "m", DatasetRow: 2, Correct: &no, Verdict: "no"}); err != nil {
		t.Fatalf("Record (correct=false): %v", err)
	}
	if err := db.Record(RunRecord{RunAt: "2026-01-01T00:00:00Z", Model: "m", DatasetRow: 3, Correct: nil, Verdict: "error", Error: "boom"}); err != nil {
		t.Fatalf("Record (correct=nil): %v", err)
	}

	rows, err := db.db.Query("SELECT dataset_row, correct, verdict, error FROM runs ORDER BY dataset_row")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	want := []struct {
		row     int
		correct sql.NullInt64
		verdict string
		errMsg  string
	}{
		{1, sql.NullInt64{Int64: 1, Valid: true}, "yes", ""},
		{2, sql.NullInt64{Int64: 0, Valid: true}, "no", ""},
		{3, sql.NullInt64{Valid: false}, "error", "boom"},
	}

	i := 0
	for rows.Next() {
		var datasetRow int
		var correct sql.NullInt64
		var verdict, errMsg string
		if err := rows.Scan(&datasetRow, &correct, &verdict, &errMsg); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if i >= len(want) {
			t.Fatalf("more rows than expected")
		}
		w := want[i]
		if datasetRow != w.row || correct != w.correct || verdict != w.verdict || errMsg != w.errMsg {
			t.Errorf("row %d = (dataset_row=%d, correct=%+v, verdict=%q, error=%q), want (%d, %+v, %q, %q)",
				i, datasetRow, correct, verdict, errMsg, w.row, w.correct, w.verdict, w.errMsg)
		}
		i++
	}
	if i != len(want) {
		t.Errorf("got %d rows, want %d", i, len(want))
	}
}

func TestRecord_AccumulatesAcrossReopens(t *testing.T) {
	// The whole point of a persistent tracking DB (unlike --db's
	// per-run-isolated store) is that it survives across separate
	// invocations of `polaris benchmark` — confirm a second Open against
	// the same path sees what a prior Open+Record wrote.
	path := filepath.Join(t.TempDir(), "tracking.db")

	db1, err := OpenTracking(path)
	if err != nil {
		t.Fatalf("OpenTracking (1st): %v", err)
	}
	correct := true
	if err := db1.Record(RunRecord{RunAt: "t", Model: "m", DatasetRow: 42, Correct: &correct, Verdict: "yes"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	db1.Close()

	db2, err := OpenTracking(path)
	if err != nil {
		t.Fatalf("OpenTracking (2nd): %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.db.QueryRow("SELECT COUNT(*) FROM runs WHERE dataset_row = 42").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (row from the first Open should still be there)", count)
	}
}
