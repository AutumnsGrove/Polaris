package benchmark

import (
	"database/sql"
	"fmt"
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

func TestStartRun_CreatesItsOwnTableAndIndexesIt(t *testing.T) {
	db := openTestTrackingDB(t)

	run, err := db.StartRun("2026-01-01T00:00:00Z", "simpleqa", "m1")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := db.db.Exec(
		fmt.Sprintf("SELECT id, dataset_row, correct, verdict, brave_calls, research_calls, assistant_turns, subject_cost_usd, grader_cost_usd, error FROM %q LIMIT 0", run.Table),
	); err != nil {
		t.Errorf("run table doesn't have the expected columns: %v", err)
	}

	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM run_index WHERE table_name = ?`, run.Table).Scan(&count); err != nil {
		t.Fatalf("querying run_index: %v", err)
	}
	if count != 1 {
		t.Errorf("run_index has %d rows for table %s, want 1", count, run.Table)
	}
}

func TestTwoRuns_WriteToSeparateTables(t *testing.T) {
	// The whole point of this design: a question re-graded in a later
	// run must never land in an earlier run's rows.
	db := openTestTrackingDB(t)

	run1, err := db.StartRun("2026-01-01T00:00:00Z", "browsecomp", "m1")
	if err != nil {
		t.Fatalf("StartRun (1): %v", err)
	}
	run2, err := db.StartRun("2026-01-02T00:00:00Z", "browsecomp", "m1")
	if err != nil {
		t.Fatalf("StartRun (2): %v", err)
	}
	if run1.Table == run2.Table {
		t.Fatalf("two StartRun calls produced the same table name %q", run1.Table)
	}

	correct := true
	if err := run1.Record(RunRecord{DatasetRow: 42, Correct: &correct, Verdict: "yes"}); err != nil {
		t.Fatalf("run1.Record: %v", err)
	}

	var count int
	if err := db.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE dataset_row = 42", run2.Table)).Scan(&count); err != nil {
		t.Fatalf("querying run2's table: %v", err)
	}
	if count != 0 {
		t.Errorf("run2's table has %d rows for dataset_row 42, want 0 — run1's write leaked into run2's table", count)
	}
	if err := db.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE dataset_row = 42", run1.Table)).Scan(&count); err != nil {
		t.Fatalf("querying run1's table: %v", err)
	}
	if count != 1 {
		t.Errorf("run1's table has %d rows for dataset_row 42, want 1", count)
	}
}

func TestRun_RecordStoresCorrectAsIntOrNull(t *testing.T) {
	db := openTestTrackingDB(t)
	run, err := db.StartRun("2026-01-01T00:00:00Z", "browsecomp", "m")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	yes := true
	no := false
	if err := run.Record(RunRecord{DatasetRow: 1, Correct: &yes, Verdict: "yes"}); err != nil {
		t.Fatalf("Record (correct=true): %v", err)
	}
	if err := run.Record(RunRecord{DatasetRow: 2, Correct: &no, Verdict: "no"}); err != nil {
		t.Fatalf("Record (correct=false): %v", err)
	}
	if err := run.Record(RunRecord{DatasetRow: 3, Correct: nil, Verdict: "error", Error: "boom"}); err != nil {
		t.Fatalf("Record (correct=nil): %v", err)
	}

	rows, err := db.db.Query(fmt.Sprintf("SELECT dataset_row, correct, verdict, error FROM %q ORDER BY dataset_row", run.Table))
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

func TestRunIndex_AccumulatesAcrossReopens(t *testing.T) {
	// The whole point of a persistent tracking DB (unlike --db's
	// per-run-isolated store) is that it survives across separate
	// invocations of `polaris benchmark` — confirm a second Open against
	// the same path sees the run_index entry (and run table) a prior
	// Open+StartRun wrote.
	path := filepath.Join(t.TempDir(), "tracking.db")

	db1, err := OpenTracking(path)
	if err != nil {
		t.Fatalf("OpenTracking (1st): %v", err)
	}
	run1, err := db1.StartRun("2026-01-01T00:00:00Z", "browsecomp", "m")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	correct := true
	if err := run1.Record(RunRecord{DatasetRow: 42, Correct: &correct, Verdict: "yes"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	db1.Close()

	db2, err := OpenTracking(path)
	if err != nil {
		t.Fatalf("OpenTracking (2nd): %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.db.QueryRow(`SELECT COUNT(*) FROM run_index WHERE table_name = ?`, run1.Table).Scan(&count); err != nil {
		t.Fatalf("querying run_index: %v", err)
	}
	if count != 1 {
		t.Errorf("run_index count = %d, want 1 (entry from the first Open should still be there)", count)
	}
	if err := db2.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE dataset_row = 42", run1.Table)).Scan(&count); err != nil {
		t.Fatalf("querying run1's table via a reopened db: %v", err)
	}
	if count != 1 {
		t.Errorf("run1's table row count = %d, want 1", count)
	}
}
