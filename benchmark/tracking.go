package benchmark

import (
	"database/sql"
	"fmt"
	"regexp"

	_ "modernc.org/sqlite"
)

// TrackingDB is a persistent (not per-run-isolated like --db's Brave
// usage caps) SQLite file that accumulates across every invocation of
// `polaris benchmark` — but each invocation gets its OWN table (see
// StartRun) rather than sharing one `runs` table, so re-running the same
// dataset row in a later run can never land in the same rows a prior
// run wrote; every run is queryable entirely on its own. run_index
// records one row per invocation (its table name + metadata) so a run
// can be found later without listing every table in the file by hand.
type TrackingDB struct {
	db *sql.DB
}

const runIndexSchema = `
CREATE TABLE IF NOT EXISTS run_index (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_at     TEXT NOT NULL,
	benchmark  TEXT NOT NULL,
	model      TEXT NOT NULL,
	table_name TEXT NOT NULL UNIQUE
);
`

// perRunSchema is one invocation's own table — the same columns the
// original shared `runs` table had, minus run_at/model/benchmark (those
// are constant for every row in a single run, so they live once in
// run_index instead of repeated on every question).
const perRunSchema = `
CREATE TABLE %q (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_row      INTEGER NOT NULL,
	correct          INTEGER,             -- NULL if errored/empty; else 0 or 1
	verdict          TEXT NOT NULL,       -- browsecomp: 'yes'|'no'; simpleqa: 'correct'|'incorrect'|'not_attempted'; both: 'error'|'empty'
	brave_calls      INTEGER NOT NULL DEFAULT 0,
	research_calls   INTEGER NOT NULL DEFAULT 0, -- web_search/web_read/etc calls (agent.Result.ResearchCalls) — diverges from assistant_turns when concurrent tool dispatch fires several in one turn
	assistant_turns  INTEGER NOT NULL DEFAULT 0,
	subject_cost_usd REAL NOT NULL DEFAULT 0,
	grader_cost_usd  REAL NOT NULL DEFAULT 0,
	error            TEXT NOT NULL DEFAULT ''
);
`

// OpenTracking opens (creating if needed) the tracking DB at path and
// ensures run_index exists — safe to call against a brand-new file or
// one from a prior run. It does NOT touch any run-specific table; those
// only come into existence via StartRun.
func OpenTracking(path string) (*TrackingDB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening tracking db: %w", err)
	}
	if _, err := db.Exec(runIndexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating run_index schema: %w", err)
	}
	return &TrackingDB{db: db}, nil
}

func (t *TrackingDB) Close() error { return t.db.Close() }

var tableNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// runTableName derives a valid, readable SQLite identifier from a run's
// timestamp and suite name, e.g. "run_20260831T004743Z_simpleqa" from
// runAt="2026-08-31T00:47:43Z", benchmarkName="simpleqa". Two runs would
// only collide if they started in the exact same second against the
// same suite — StartRun below uses CREATE TABLE (not IF NOT EXISTS), so
// a collision fails loudly instead of silently merging into a prior
// run's table.
func runTableName(runAt, benchmarkName string) string {
	safeTime := tableNameSanitizer.ReplaceAllString(runAt, "")
	safeName := tableNameSanitizer.ReplaceAllString(benchmarkName, "")
	return fmt.Sprintf("run_%s_%s", safeTime, safeName)
}

// Run is a handle to one benchmark invocation's own table. Every
// RunRecord passed to Record lands only in this run's table, never in
// another invocation's.
type Run struct {
	db    *sql.DB
	Table string
}

// StartRun creates a brand-new table for one invocation of
// `polaris benchmark` and records its existence in run_index. runAt/
// benchmarkName/model describe the whole run — constant across every
// question in it — so they're stored once here rather than repeated on
// every row.
func (t *TrackingDB) StartRun(runAt, benchmarkName, model string) (*Run, error) {
	table := runTableName(runAt, benchmarkName)
	ddl := fmt.Sprintf(perRunSchema, table)
	if _, err := t.db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("creating run table %s: %w", table, err)
	}
	if _, err := t.db.Exec(
		`INSERT INTO run_index (run_at, benchmark, model, table_name) VALUES (?, ?, ?, ?)`,
		runAt, benchmarkName, model, table,
	); err != nil {
		return nil, fmt.Errorf("recording run in run_index: %w", err)
	}
	return &Run{db: t.db, Table: table}, nil
}

// RunRecord is one question's outcome within a single run. Correct is
// nil for the 'error'/'empty' verdicts (agent.Run itself failed, or came
// back with no answer — see cmd/benchmark.go) since "wrong" and
// "couldn't even try" are different failure modes worth telling apart in
// a query, not collapsed into the same 0.
type RunRecord struct {
	DatasetRow     int
	Correct        *bool
	Verdict        string
	BraveCalls     int
	ResearchCalls  int
	AssistantTurns int
	SubjectCostUSD float64
	GraderCostUSD  float64
	Error          string
}

// Record inserts one RunRecord into this run's own table — never
// updates/dedupes, since even within one run each dataset row is graded
// exactly once.
func (r *Run) Record(rec RunRecord) error {
	var correct interface{}
	if rec.Correct != nil {
		if *rec.Correct {
			correct = 1
		} else {
			correct = 0
		}
	}
	_, err := r.db.Exec(
		fmt.Sprintf(`INSERT INTO %q (dataset_row, correct, verdict, brave_calls, research_calls, assistant_turns, subject_cost_usd, grader_cost_usd, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Table),
		rec.DatasetRow, correct, rec.Verdict, rec.BraveCalls, rec.ResearchCalls, rec.AssistantTurns, rec.SubjectCostUSD, rec.GraderCostUSD, rec.Error,
	)
	return err
}
