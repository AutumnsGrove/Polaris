package benchmark

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// trackingSchema is deliberately minimal and separate from store.Store's
// full chat-app schema (threads/messages/events/...) — this only ever
// needs one flat table of per-question outcomes, queried directly with
// plain SQL (sqlite3 benchmark-tracking.db "SELECT ..."), not through any
// Go query helpers. dataset_row is BrowseComp's row index (see
// Row.Index's doc comment) — never the question/answer text itself, so
// this DB is safe to keep around and accumulate across many runs without
// running into the canary/leakage concern the raw dataset carries.
const trackingSchema = `
CREATE TABLE IF NOT EXISTS runs (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	run_at           TEXT NOT NULL,
	model            TEXT NOT NULL,
	dataset_row      INTEGER NOT NULL,
	correct          INTEGER,             -- NULL if errored/empty; else 0 or 1
	verdict          TEXT NOT NULL,       -- 'yes' | 'no' | 'error' | 'empty'
	brave_calls      INTEGER NOT NULL DEFAULT 0,
	assistant_turns  INTEGER NOT NULL DEFAULT 0,
	subject_cost_usd REAL NOT NULL DEFAULT 0,
	grader_cost_usd  REAL NOT NULL DEFAULT 0,
	error            TEXT NOT NULL DEFAULT ''
);
`

// TrackingDB is a persistent (not per-run-isolated, unlike the DB
// store.Open manages for Brave usage caps) record of every question a
// benchmark run has ever graded — meant to accumulate across many
// invocations of `polaris benchmark`, not get wiped each time, so trends
// across repeated small samples are queryable later.
type TrackingDB struct {
	db *sql.DB
}

// OpenTracking opens (creating if needed) the tracking DB at path and
// ensures its schema exists — safe to call against a brand-new file or
// one from a prior run.
func OpenTracking(path string) (*TrackingDB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening tracking db: %w", err)
	}
	if _, err := db.Exec(trackingSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating tracking schema: %w", err)
	}
	return &TrackingDB{db: db}, nil
}

func (t *TrackingDB) Close() error { return t.db.Close() }

// RunRecord is one question's outcome from one benchmark invocation.
// Correct is nil for the 'error'/'empty' verdicts (agent.Run itself
// failed, or came back with no answer — see cmd/benchmark.go) since
// "wrong" and "couldn't even try" are different failure modes worth
// telling apart in a query, not collapsed into the same 0.
type RunRecord struct {
	RunAt          string
	Model          string
	DatasetRow     int
	Correct        *bool
	Verdict        string
	BraveCalls     int
	AssistantTurns int
	SubjectCostUSD float64
	GraderCostUSD  float64
	Error          string
}

// Record inserts one RunRecord — never updates/dedupes an existing row,
// since the whole point is a full history of every run, including
// re-running the exact same dataset row on a later date to see if a
// prompting change flipped its outcome.
func (t *TrackingDB) Record(r RunRecord) error {
	var correct interface{}
	if r.Correct != nil {
		if *r.Correct {
			correct = 1
		} else {
			correct = 0
		}
	}
	_, err := t.db.Exec(
		`INSERT INTO runs (run_at, model, dataset_row, correct, verdict, brave_calls, assistant_turns, subject_cost_usd, grader_cost_usd, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunAt, r.Model, r.DatasetRow, correct, r.Verdict, r.BraveCalls, r.AssistantTurns, r.SubjectCostUSD, r.GraderCostUSD, r.Error,
	)
	return err
}
