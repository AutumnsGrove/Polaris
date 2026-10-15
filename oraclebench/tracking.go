package oraclebench

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Tracking is a persistent SQLite file that accumulates across invocations of
// `polaris oracle-bench`, following benchmark.TrackingDB's convention: every
// invocation gets its OWN table (so re-running the bank can never land in rows
// a prior run wrote) and run_index records one row per invocation. It is a
// separate file from `polaris benchmark`'s because the columns differ: here a
// row is a (model, case) score with its per-check detail, and a run is only
// comparable to another if it used the same case bank and weights, so both are
// recorded.
type Tracking struct {
	db *sql.DB
}

const runIndexSchema = `
CREATE TABLE IF NOT EXISTS run_index (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	run_at       TEXT NOT NULL,
	table_name   TEXT NOT NULL UNIQUE,
	label        TEXT NOT NULL DEFAULT '',
	weights_hash TEXT NOT NULL,
	cases        INTEGER NOT NULL,
	repeats      INTEGER NOT NULL,
	concurrency  INTEGER NOT NULL,
	budget_ms    INTEGER NOT NULL,
	seed         INTEGER NOT NULL DEFAULT 0,
	judge_model  TEXT NOT NULL DEFAULT '',
	models       TEXT NOT NULL,        -- JSON array of catalog ids
	leaderboard  TEXT NOT NULL DEFAULT '' -- the finished leaderboard JSON, filled at the end
);
`

const perRunSchema = `
CREATE TABLE %q (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	model            TEXT NOT NULL,
	resolved_model   TEXT NOT NULL DEFAULT '',
	case_id          TEXT NOT NULL,
	track            TEXT NOT NULL,
	disputed         INTEGER NOT NULL DEFAULT 0,
	errored          INTEGER NOT NULL DEFAULT 0,
	score            REAL NOT NULL,
	max_score        REAL NOT NULL,
	calls            INTEGER NOT NULL,
	latency_p50_ms   INTEGER NOT NULL DEFAULT 0,
	subject_cost_usd REAL NOT NULL DEFAULT 0,
	judge_cost_usd   REAL NOT NULL DEFAULT 0,
	error            TEXT NOT NULL DEFAULT '',
	checks_json      TEXT NOT NULL DEFAULT ''
);
`

// OpenTracking opens (creating if needed) the tracking DB.
func OpenTracking(path string) (*Tracking, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening tracking db: %w", err)
	}
	if _, err := db.Exec(runIndexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating run_index schema: %w", err)
	}
	return &Tracking{db: db}, nil
}

func (t *Tracking) Close() error { return t.db.Close() }

// RunMeta describes one invocation.
type RunMeta struct {
	RunAt       string
	Label       string
	WeightsHash string
	Cases       int
	Repeats     int
	Concurrency int
	Budget      time.Duration
	Seed        int64
	JudgeModel  string
	Models      []string
}

var tableNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// runTableName derives a readable SQLite identifier from the start time, e.g.
// run_20261016T120301Z_oraclebench. StartRun uses CREATE TABLE (not IF NOT
// EXISTS), so two runs starting in the same second fail loudly instead of
// merging.
func runTableName(runAt string) string {
	return "run_" + tableNameSanitizer.ReplaceAllString(runAt, "") + "_oraclebench"
}

// TrackedRun is one invocation's table.
type TrackedRun struct {
	db    *sql.DB
	Table string
}

// StartRun creates the invocation's table and its run_index row.
func (t *Tracking) StartRun(m RunMeta) (*TrackedRun, error) {
	table := runTableName(m.RunAt)
	if _, err := t.db.Exec(fmt.Sprintf(perRunSchema, table)); err != nil {
		return nil, fmt.Errorf("creating run table %s: %w", table, err)
	}
	models, _ := json.Marshal(m.Models)
	if _, err := t.db.Exec(
		`INSERT INTO run_index (run_at, table_name, label, weights_hash, cases, repeats, concurrency, budget_ms, seed, judge_model, models)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.RunAt, table, m.Label, m.WeightsHash, m.Cases, m.Repeats, m.Concurrency, m.Budget.Milliseconds(), m.Seed, m.JudgeModel, string(models),
	); err != nil {
		return nil, fmt.Errorf("recording run in run_index: %w", err)
	}
	return &TrackedRun{db: t.db, Table: table}, nil
}

// RecordCase stores one (model, case) score. The per-check detail goes in as
// JSON: it is what lets a later query ask which checks a model keeps missing
// without a column per check.
func (r *TrackedRun) RecordCase(model string, resolved []string, cr CaseRun, cs CaseScore) error {
	var costs, judgeCost float64
	var lat []time.Duration
	firstErr := ""
	for _, s := range cr.Samples {
		costs += s.Result.CostUSD
		if s.Err != ErrNone {
			if firstErr == "" {
				firstErr = string(s.Err) + ": " + s.ErrText
			}
			continue
		}
		lat = append(lat, s.Latency)
	}
	for _, j := range cr.Judgments {
		judgeCost += j.CostUSD
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	checks, err := json.Marshal(cs.Checks)
	if err != nil {
		return fmt.Errorf("encoding checks for %s: %w", cs.ID, err)
	}
	_, err = r.db.Exec(
		fmt.Sprintf(`INSERT INTO %q (model, resolved_model, case_id, track, disputed, errored, score, max_score, calls, latency_p50_ms, subject_cost_usd, judge_cost_usd, error, checks_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Table),
		model, strings.Join(resolved, ","), cs.ID, cs.Track, b2i(cs.Disputed), b2i(cs.Errored), cs.Score, cs.Max, len(cr.Samples),
		percentile(lat, 0.5).Milliseconds(), costs, judgeCost, firstErr, string(checks),
	)
	return err
}

// SaveLeaderboard stores the finished leaderboard JSON on the run_index row.
func (r *TrackedRun) SaveLeaderboard(js []byte) error {
	_, err := r.db.Exec(`UPDATE run_index SET leaderboard = ? WHERE table_name = ?`, string(js), r.Table)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
