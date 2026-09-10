// constellation.go implements Constellation's store layer (see
// docs/plans/constellation.md): constellation_config's singleton settings,
// stars and their FTS5 search, star_sources/star_edges, and the
// shooting_star_* observability tables Weaver's runs write to.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrStarNotFound is returned by GetStar when no row matches the given id.
var ErrStarNotFound = errors.New("star not found")

// ConstellationConfig is Constellation's singleton settings row.
type ConstellationConfig struct {
	Enabled             bool       `json:"enabled"`
	PollIntervalMinutes int        `json:"poll_interval_minutes"`
	LastCheckedAt       *time.Time `json:"last_checked_at"`
	// Model: empty means "use whatever config.DefaultModel currently
	// resolves to" — same empty-means-inherit pattern
	// PulsarDailyConfig.WeatherLocation uses.
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
}

// GetConstellationConfig returns the singleton config row, inserting the
// column-default row first if this is the very first read — same
// first-read-inserts-the-row pattern GetDailyConfig uses.
func (s *Store) GetConstellationConfig() (*ConstellationConfig, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO constellation_config (id) VALUES (1)`); err != nil {
		return nil, fmt.Errorf("get constellation config: %w", err)
	}
	var c ConstellationConfig
	err := s.db.QueryRow(
		`SELECT enabled, poll_interval_minutes, last_checked_at, model, created_at
		 FROM constellation_config WHERE id = 1`,
	).Scan(&c.Enabled, &c.PollIntervalMinutes, &c.LastCheckedAt, &c.Model, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get constellation config: %w", err)
	}
	return &c, nil
}

// UpdateConstellationConfig writes the settings-panel-editable fields.
func (s *Store) UpdateConstellationConfig(enabled bool, pollIntervalMinutes int, model string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO constellation_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("update constellation config: %w", err)
	}
	_, err := s.db.Exec(
		`UPDATE constellation_config SET enabled = ?, poll_interval_minutes = ?, model = ? WHERE id = 1`,
		enabled, pollIntervalMinutes, model,
	)
	if err != nil {
		return fmt.Errorf("update constellation config: %w", err)
	}
	return nil
}

// SetConstellationLastChecked records the poller's most recent tick.
func (s *Store) SetConstellationLastChecked(at string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO constellation_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set constellation last checked: %w", err)
	}
	_, err := s.db.Exec(`UPDATE constellation_config SET last_checked_at = ? WHERE id = 1`, at)
	if err != nil {
		return fmt.Errorf("set constellation last checked: %w", err)
	}
	return nil
}

// Star is one topic — a card in the Library, a node on the Map. See the
// plan doc's "Database schema" for the full reasoning behind status/
// is_personal.
type Star struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Category   string    `json:"category"`
	Summary    string    `json:"summary"`
	Body       string    `json:"body"`
	Tags       []string  `json:"tags"`
	Status     string    `json:"status"`
	Confidence string    `json:"confidence"`
	IsPersonal bool      `json:"is_personal"`
	Disabled   bool      `json:"disabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CreateStar writes a new stars row. A personal star always starts
// 'proposed' regardless of the requested Status — see the plan doc's
// "Personal stars" status-routing rules, "no exceptions".
func (s *Store) CreateStar(star Star) (int64, error) {
	if star.Tags == nil {
		// json.Marshal(nil slice) encodes "null", not "[]" — every reader
		// of this column (GetStar/ListStars' json.Unmarshal into
		// []string, and the frontend's own JSON decode) expects a real,
		// always-iterable array, matching the column's own '[]' default.
		star.Tags = []string{}
	}
	tagsJSON, err := json.Marshal(star.Tags)
	if err != nil {
		return 0, fmt.Errorf("create star: encode tags: %w", err)
	}
	status := star.Status
	if status == "" {
		status = "proposed"
	}
	if star.IsPersonal {
		status = "proposed"
	}
	res, err := s.db.Exec(
		`INSERT INTO stars (title, category, summary, body, tags, status, confidence, is_personal)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		star.Title, star.Category, star.Summary, star.Body, string(tagsJSON), status, star.Confidence, star.IsPersonal,
	)
	if err != nil {
		return 0, fmt.Errorf("create star: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create star: %w", err)
	}
	return id, nil
}

// GetStar returns one star by id, or ErrStarNotFound.
func (s *Store) GetStar(id int64) (*Star, error) {
	var star Star
	var tagsJSON string
	var isPersonal, disabled int
	err := s.db.QueryRow(
		`SELECT id, title, category, summary, body, tags, status, confidence, is_personal, disabled, created_at, updated_at
		 FROM stars WHERE id = ?`, id,
	).Scan(&star.ID, &star.Title, &star.Category, &star.Summary, &star.Body, &tagsJSON, &star.Status, &star.Confidence, &isPersonal, &disabled, &star.CreatedAt, &star.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStarNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get star: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &star.Tags); err != nil {
		return nil, fmt.Errorf("get star: decode tags: %w", err)
	}
	star.IsPersonal = isPersonal != 0
	star.Disabled = disabled != 0
	return &star, nil
}

// UpdateStar merges into an existing star (rewrite to read as one coherent,
// current entry, never append — Weaver's own job, this just persists it).
// isPersonal resets status back to 'proposed' on any update, even a pure
// reinforcement — see the plan doc's "Personal stars" for why this is
// deliberately strict rather than trying to self-judge "is this the same
// claim or a different one".
func (s *Store) UpdateStar(id int64, summary, body string, tags []string, confidenceClass string, isPersonal bool) error {
	if tags == nil {
		// See CreateStar's identical guard — json.Marshal(nil) encodes
		// "null", not the "[]" every reader of this column expects.
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("update star: encode tags: %w", err)
	}
	query := `UPDATE stars SET summary = ?, body = ?, tags = ?, confidence = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	args := []any{summary, body, string(tagsJSON), confidenceClass, id}
	if isPersonal {
		query = `UPDATE stars SET summary = ?, body = ?, tags = ?, confidence = ?, status = 'proposed', updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	}
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("update star: %w", err)
	}
	return nil
}

// SetStarStatus is used by review actions (approve/discard) and by
// Restore, which moves a rejected star straight to 'confirmed' (not back
// to 'proposed' — see the plan doc's "Rejected stars" section).
func (s *Store) SetStarStatus(id int64, status string) error {
	if _, err := s.db.Exec(`UPDATE stars SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id); err != nil {
		return fmt.Errorf("set star status: %w", err)
	}
	return nil
}

// SetStarDisabled implements the overflow menu's Disable action —
// independent of status, same soft-delete shape as threads.disabled.
func (s *Store) SetStarDisabled(id int64, disabled bool) error {
	if _, err := s.db.Exec(`UPDATE stars SET disabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, disabled, id); err != nil {
		return fmt.Errorf("set star disabled: %w", err)
	}
	return nil
}

// RenameStar implements the overflow menu's plain rename — no LLM
// involved, unlike Edit star.
func (s *Store) RenameStar(id int64, title string) error {
	if _, err := s.db.Exec(`UPDATE stars SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, title, id); err != nil {
		return fmt.Errorf("rename star: %w", err)
	}
	return nil
}

// StarFilter selects which stars ListStars returns. Statuses is required —
// there's no "everything" default, since the Library/Inbox/Rejected/About-
// you sections each want a distinct, deliberate slice, not an
// undifferentiated list (see the plan doc's "The UI" section).
type StarFilter struct {
	Statuses   []string
	IsPersonal *bool
}

// ListStars returns non-disabled stars matching the filter, most recently
// updated first.
func (s *Store) ListStars(filter StarFilter) ([]Star, error) {
	if len(filter.Statuses) == 0 {
		return nil, fmt.Errorf("list stars: at least one status is required")
	}
	query := `SELECT id, title, category, summary, body, tags, status, confidence, is_personal, disabled, created_at, updated_at
	           FROM stars WHERE disabled = 0 AND status IN (` + placeholders(len(filter.Statuses)) + `)`
	args := make([]any, 0, len(filter.Statuses)+1)
	for _, st := range filter.Statuses {
		args = append(args, st)
	}
	if filter.IsPersonal != nil {
		query += ` AND is_personal = ?`
		args = append(args, *filter.IsPersonal)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list stars: %w", err)
	}
	defer rows.Close()

	var stars []Star
	for rows.Next() {
		var star Star
		var tagsJSON string
		var isPersonal, disabled int
		if err := rows.Scan(&star.ID, &star.Title, &star.Category, &star.Summary, &star.Body, &tagsJSON, &star.Status, &star.Confidence, &isPersonal, &disabled, &star.CreatedAt, &star.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list stars: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &star.Tags); err != nil {
			return nil, fmt.Errorf("list stars: decode tags: %w", err)
		}
		star.IsPersonal = isPersonal != 0
		star.Disabled = disabled != 0
		stars = append(stars, star)
	}
	return stars, rows.Err()
}

func placeholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ", "
		}
		out += "?"
	}
	return out
}

// StarSearchResult is one search_stars hit — title/summary only, a lead
// never enough on its own to decide a match or a link (read_star is
// mandatory before acting on one — see the plan doc's "Weaver's tools").
type StarSearchResult struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

// SearchStars is FTS5 over stars_fts — keyword, not semantic. Includes
// rejected stars deliberately: a result here doubles as Weaver's "was this
// already said no to" check (see the plan doc's "A rejected star is a
// closed matter, not a candidate").
func (s *Store) SearchStars(query string, limit int) ([]StarSearchResult, error) {
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.summary, s.status
		 FROM stars_fts
		 JOIN stars s ON s.id = stars_fts.rowid
		 WHERE stars_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search stars: %w", err)
	}
	defer rows.Close()

	var results []StarSearchResult
	for rows.Next() {
		var r StarSearchResult
		if err := rows.Scan(&r.ID, &r.Title, &r.Summary, &r.Status); err != nil {
			return nil, fmt.Errorf("search stars: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// LinkStarSource upserts a star_sources row — a thread can contribute to
// the same star more than once over time (see the plan doc's "Revisiting a
// thread"), so a repeat call just refreshes linked_at instead of erroring
// or duplicating.
func (s *Store) LinkStarSource(starID int64, threadID string) error {
	_, err := s.db.Exec(
		`INSERT INTO star_sources (star_id, thread_id) VALUES (?, ?)
		 ON CONFLICT (star_id, thread_id) DO UPDATE SET linked_at = CURRENT_TIMESTAMP`,
		starID, threadID,
	)
	if err != nil {
		return fmt.Errorf("link star source: %w", err)
	}
	return nil
}

// StarSource is one thread that contributed to a star.
type StarSource struct {
	ThreadID string    `json:"thread_id"`
	LinkedAt time.Time `json:"linked_at"`
}

// StarSources lists the threads backing a star's "Linked articles" block.
func (s *Store) StarSources(starID int64) ([]StarSource, error) {
	rows, err := s.db.Query(`SELECT thread_id, linked_at FROM star_sources WHERE star_id = ? ORDER BY linked_at DESC`, starID)
	if err != nil {
		return nil, fmt.Errorf("star sources: %w", err)
	}
	defer rows.Close()

	var out []StarSource
	for rows.Next() {
		var src StarSource
		if err := rows.Scan(&src.ThreadID, &src.LinkedAt); err != nil {
			return nil, fmt.Errorf("star sources: %w", err)
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// StarsByThread returns the stars a thread has already contributed to —
// the "prior notes" a revisit's filter pass checks the fresh delta against
// (see the plan doc's "Revisiting a thread").
func (s *Store) StarsByThread(threadID string) ([]Star, error) {
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.category, s.summary, s.body, s.tags, s.status, s.confidence, s.is_personal, s.disabled, s.created_at, s.updated_at
		 FROM stars s
		 JOIN star_sources src ON src.star_id = s.id
		 WHERE src.thread_id = ?
		 ORDER BY s.updated_at DESC`, threadID,
	)
	if err != nil {
		return nil, fmt.Errorf("stars by thread: %w", err)
	}
	defer rows.Close()

	var stars []Star
	for rows.Next() {
		var star Star
		var tagsJSON string
		var isPersonal, disabled int
		if err := rows.Scan(&star.ID, &star.Title, &star.Category, &star.Summary, &star.Body, &tagsJSON, &star.Status, &star.Confidence, &isPersonal, &disabled, &star.CreatedAt, &star.UpdatedAt); err != nil {
			return nil, fmt.Errorf("stars by thread: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &star.Tags); err != nil {
			return nil, fmt.Errorf("stars by thread: decode tags: %w", err)
		}
		star.IsPersonal = isPersonal != 0
		star.Disabled = disabled != 0
		stars = append(stars, star)
	}
	return stars, rows.Err()
}

// StarEdge is one reflection-layer connection, as seen from either side.
type StarEdge struct {
	OtherStarID int64  `json:"other_star_id"`
	Reasoning   string `json:"reasoning"`
}

// LinkStars writes a star_edges row, idempotent (no-ops if the pair's
// already linked) and undirected — star_a_id/star_b_id are normalized to
// (min, max) regardless of call order, matching the schema's
// CHECK (star_a_id < star_b_id).
func (s *Store) LinkStars(starIDA, starIDB int64, reasoning string) error {
	a, b := starIDA, starIDB
	if a > b {
		a, b = b, a
	}
	_, err := s.db.Exec(
		`INSERT INTO star_edges (star_a_id, star_b_id, reasoning) VALUES (?, ?, ?)
		 ON CONFLICT (star_a_id, star_b_id) DO NOTHING`,
		a, b, reasoning,
	)
	if err != nil {
		return fmt.Errorf("link stars: %w", err)
	}
	return nil
}

// StarEdges returns every star this one connects to, from either side of
// the underdirected pair.
func (s *Store) StarEdges(starID int64) ([]StarEdge, error) {
	rows, err := s.db.Query(
		`SELECT star_b_id, reasoning FROM star_edges WHERE star_a_id = ?
		 UNION ALL
		 SELECT star_a_id, reasoning FROM star_edges WHERE star_b_id = ?`,
		starID, starID,
	)
	if err != nil {
		return nil, fmt.Errorf("star edges: %w", err)
	}
	defer rows.Close()

	var out []StarEdge
	for rows.Next() {
		var e StarEdge
		if err := rows.Scan(&e.OtherStarID, &e.Reasoning); err != nil {
			return nil, fmt.Errorf("star edges: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ShootingStarRun is one row per shooting star — one agent.Run over one
// thread.
type ShootingStarRun struct {
	ID                int64      `json:"id"`
	ThreadID          string     `json:"thread_id"`
	LastMessageIDSeen int64      `json:"last_message_id_seen"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	Summary           string     `json:"summary"`
	Error             string     `json:"error"`
	NeedsRetry        bool       `json:"needs_retry"`
	CostUSD           float64    `json:"cost_usd"`
}

// StartShootingStarRun opens a new run row.
func (s *Store) StartShootingStarRun(threadID string, lastMessageIDSeen int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO shooting_star_runs (thread_id, last_message_id_seen) VALUES (?, ?)`,
		threadID, lastMessageIDSeen,
	)
	if err != nil {
		return 0, fmt.Errorf("start shooting star run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start shooting star run: %w", err)
	}
	return id, nil
}

// FinishShootingStarRun closes out a run: sets finished_at, the closing
// summary/error, needs_retry, and rolls up cost_usd from whatever
// shooting_star_events rows this run logged along the way.
func (s *Store) FinishShootingStarRun(runID int64, summary, errText string, needsRetry bool) error {
	_, err := s.db.Exec(
		`UPDATE shooting_star_runs
		 SET finished_at = CURRENT_TIMESTAMP, summary = ?, error = ?, needs_retry = ?,
		     cost_usd = (SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE run_id = ?)
		 WHERE id = ?`,
		summary, errText, needsRetry, runID, runID,
	)
	if err != nil {
		return fmt.Errorf("finish shooting star run: %w", err)
	}
	return nil
}

// LastShootingStarRun returns the most recent run for a thread, or nil, nil
// if none exists yet — "no prior run" is the normal, expected state for a
// thread's first pass (see the plan doc's "Thread eligibility").
func (s *Store) LastShootingStarRun(threadID string) (*ShootingStarRun, error) {
	var run ShootingStarRun
	var needsRetry int
	err := s.db.QueryRow(
		`SELECT id, thread_id, last_message_id_seen, started_at, finished_at, summary, error, needs_retry, cost_usd
		 FROM shooting_star_runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1`, threadID,
	).Scan(&run.ID, &run.ThreadID, &run.LastMessageIDSeen, &run.StartedAt, &run.FinishedAt, &run.Summary, &run.Error, &needsRetry, &run.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last shooting star run: %w", err)
	}
	run.NeedsRetry = needsRetry != 0
	return &run, nil
}

// RecordShootingStarCandidate logs one topic candidate Weaver proposed
// within a run — called as a side effect of create_star/update_star, not a
// separate logging step.
func (s *Store) RecordShootingStarCandidate(runID int64, title, confidenceClass, decision, reasoning string, resultingStarID *int64) error {
	_, err := s.db.Exec(
		`INSERT INTO shooting_star_candidates (run_id, title, confidence_class, decision, reasoning, resulting_star_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		runID, title, confidenceClass, decision, reasoning, resultingStarID,
	)
	if err != nil {
		return fmt.Errorf("record shooting star candidate: %w", err)
	}
	return nil
}

// LatestCandidateReasoning returns the most recent
// shooting_star_candidates.reasoning recorded for a star — the "why" a
// human reviewing it in the Inbox needs (see the plan doc's "Reviewing a
// proposed star": the Review screen surfaces this as a "Why this needs a
// look" block, the same sentence Weaver already logged for the trace
// tables, not a separately-authored summary). Returns "" if the star has
// no candidate row at all (shouldn't normally happen — create_star/
// update_star always log one — but a star reached some other way
// shouldn't 500 over it).
func (s *Store) LatestCandidateReasoning(starID int64) (string, error) {
	var reasoning string
	err := s.db.QueryRow(
		`SELECT reasoning FROM shooting_star_candidates
		 WHERE resulting_star_id = ? ORDER BY created_at DESC LIMIT 1`,
		starID,
	).Scan(&reasoning)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest candidate reasoning: %w", err)
	}
	return reasoning, nil
}

// RecordShootingStarEvent logs one completion call in Weaver's loop —
// every turn produces a row, whether it called a tool or ended the run in
// plain text ('final_answer'), plus the double-RAG filter pass on a
// revisit ('filter_pass').
func (s *Store) RecordShootingStarEvent(runID int64, tool, args, result string, costUSD float64) error {
	_, err := s.db.Exec(
		`INSERT INTO shooting_star_events (run_id, tool, args, result, cost_usd) VALUES (?, ?, ?, ?, ?)`,
		runID, tool, args, result, costUSD,
	)
	if err != nil {
		return fmt.Errorf("record shooting star event: %w", err)
	}
	return nil
}

// RecordStarReview logs one Inbox review action (approved/refined/
// discarded) — scoped to Inbox review only, see the plan doc's
// star_reviews section for why an ordinary Edit-star correction doesn't
// write here.
func (s *Store) RecordStarReview(starID int64, action, correction string) error {
	_, err := s.db.Exec(
		`INSERT INTO star_reviews (star_id, action, correction) VALUES (?, ?, ?)`,
		starID, action, correction,
	)
	if err != nil {
		return fmt.Errorf("record star review: %w", err)
	}
	return nil
}

// ConstellationStats is Constellation's own cost/observability summary —
// a wholly separate surface from Stats/CostBySource (see the plan doc's
// "Cost tracking and observability" for why Weaver's spend isn't folded
// into the main Polaris/Pulsar/Daily breakdown).
type ConstellationStats struct {
	PeriodDays int `json:"period_days"`

	TotalCostUSD  float64 `json:"total_cost_usd"`
	PeriodCostUSD float64 `json:"period_cost_usd"`

	ShootingStarCount int `json:"shooting_star_count"`

	StarCountsByStatus map[string]int `json:"star_counts_by_status"`

	// ToolCallCounts is scoped to the five real tools only — the cost sum
	// above includes filter_pass/final_answer too, but this breakdown
	// doesn't, same distinction Stats.SearchProviderCounts' doc comment
	// draws between "what actually answered" and "what was billed".
	ToolCallCounts map[string]int `json:"tool_call_counts"`

	ReviewActionCounts map[string]int `json:"review_action_counts"`

	LinksCreatedCount int `json:"links_created_count"`

	MaxTurnsCount   int `json:"max_turns_count"`
	NeedsRetryCount int `json:"needs_retry_count"`
}

var constellationRealTools = []string{"search_stars", "read_star", "create_star", "update_star", "link_stars"}

// GetConstellationStats aggregates on demand from Constellation's own
// tables — same "no running counters, no second source of truth" approach
// GetStats already uses. periodDays of 0 means "all time" for
// PeriodCostUSD too (mirrors Stats' own PeriodDays=0 convention).
func (s *Store) GetConstellationStats(periodDays int) (*ConstellationStats, error) {
	stats := &ConstellationStats{
		PeriodDays:         periodDays,
		StarCountsByStatus: map[string]int{},
		ToolCallCounts:     map[string]int{},
		ReviewActionCounts: map[string]int{},
	}

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events`).Scan(&stats.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: total cost: %w", err)
	}

	periodFilter := "1=1"
	if periodDays > 0 {
		periodFilter = fmt.Sprintf("created_at >= datetime('now', '-%d days')", periodDays)
	}

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE ` + periodFilter).Scan(&stats.PeriodCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: period cost: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shooting_star_runs`).Scan(&stats.ShootingStarCount); err != nil {
		return nil, fmt.Errorf("constellation stats: shooting star count: %w", err)
	}

	statusRows, err := s.db.Query(`SELECT status, COUNT(*) FROM stars WHERE disabled = 0 GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
	}
	for statusRows.Next() {
		var status string
		var count int
		if err := statusRows.Scan(&status, &count); err != nil {
			statusRows.Close()
			return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
		}
		stats.StarCountsByStatus[status] = count
	}
	statusRows.Close()
	if err := statusRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
	}

	toolRows, err := s.db.Query(
		`SELECT tool, COUNT(*) FROM shooting_star_events WHERE tool IN (`+placeholders(len(constellationRealTools))+`) GROUP BY tool`,
		toAnySlice(constellationRealTools)...,
	)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
	}
	for toolRows.Next() {
		var tool string
		var count int
		if err := toolRows.Scan(&tool, &count); err != nil {
			toolRows.Close()
			return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
		}
		stats.ToolCallCounts[tool] = count
	}
	toolRows.Close()
	if err := toolRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
	}

	reviewRows, err := s.db.Query(`SELECT action, COUNT(*) FROM star_reviews GROUP BY action`)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
	}
	for reviewRows.Next() {
		var action string
		var count int
		if err := reviewRows.Scan(&action, &count); err != nil {
			reviewRows.Close()
			return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
		}
		stats.ReviewActionCounts[action] = count
	}
	reviewRows.Close()
	if err := reviewRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM star_edges`).Scan(&stats.LinksCreatedCount); err != nil {
		return nil, fmt.Errorf("constellation stats: links created count: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shooting_star_runs WHERE error = 'max_turns_exceeded'`).Scan(&stats.MaxTurnsCount); err != nil {
		return nil, fmt.Errorf("constellation stats: max turns count: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shooting_star_runs WHERE needs_retry = 1`).Scan(&stats.NeedsRetryCount); err != nil {
		return nil, fmt.Errorf("constellation stats: needs retry count: %w", err)
	}

	return stats, nil
}

// StarEdgePair is one star_edges row exactly as stored — both sides named
// explicitly, unlike StarEdge (which is deliberately one-sided, "the other
// star", for a single star's own edge list). Used by the Map view, which
// needs the full graph at once.
type StarEdgePair struct {
	StarAID   int64  `json:"star_a_id"`
	StarBID   int64  `json:"star_b_id"`
	Reasoning string `json:"reasoning"`
}

// AllStarEdges returns every star_edges row — the Map view's full graph.
func (s *Store) AllStarEdges() ([]StarEdgePair, error) {
	rows, err := s.db.Query(`SELECT star_a_id, star_b_id, reasoning FROM star_edges ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("all star edges: %w", err)
	}
	defer rows.Close()

	var out []StarEdgePair
	for rows.Next() {
		var e StarEdgePair
		if err := rows.Scan(&e.StarAID, &e.StarBID, &e.Reasoning); err != nil {
			return nil, fmt.Errorf("all star edges: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ConstellationDigest is the Library's digest banner data — pure counts
// plus one concrete example, computed live from tables Weaver already
// writes (see the plan doc's "The weekly digest"). Whether to actually
// show the banner (both counts zero means no signal) is the HTTP layer's
// call, not this query's — see gateway/constellation_routes.go's
// constellationDigest.Show.
type ConstellationDigest struct {
	NewCount   int
	LinksCount int
	Highlight  string
}

// GetConstellationDigest computes the trailing-7-day digest. Highlight
// prefers the most recent star_edges row this week ("{a} → {b}"), falling
// back to the most recent new star's title if no links happened this week
// — matching the plan doc's exact fallback order.
func (s *Store) GetConstellationDigest() (*ConstellationDigest, error) {
	var d ConstellationDigest
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stars WHERE disabled = 0 AND created_at >= datetime('now', '-7 days')`).Scan(&d.NewCount); err != nil {
		return nil, fmt.Errorf("constellation digest: new count: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM star_edges WHERE created_at >= datetime('now', '-7 days')`).Scan(&d.LinksCount); err != nil {
		return nil, fmt.Errorf("constellation digest: links count: %w", err)
	}

	var titleA, titleB string
	err := s.db.QueryRow(
		`SELECT sa.title, sb.title FROM star_edges e
		 JOIN stars sa ON sa.id = e.star_a_id
		 JOIN stars sb ON sb.id = e.star_b_id
		 WHERE e.created_at >= datetime('now', '-7 days')
		 ORDER BY e.id DESC LIMIT 1`,
	).Scan(&titleA, &titleB)
	switch {
	case err == nil:
		d.Highlight = titleA + " → " + titleB
	case errors.Is(err, sql.ErrNoRows):
		var newestTitle string
		err := s.db.QueryRow(
			`SELECT title FROM stars WHERE disabled = 0 AND created_at >= datetime('now', '-7 days')
			 ORDER BY id DESC LIMIT 1`,
		).Scan(&newestTitle)
		if err == nil {
			d.Highlight = newestTitle
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("constellation digest: fallback highlight: %w", err)
		}
	default:
		return nil, fmt.Errorf("constellation digest: highlight: %w", err)
	}

	return &d, nil
}

// ConstellationWeekItem is one row in the "This week" tap-through feed —
// everything actually made in the last 7 days: new stars, updated
// (merged) stars, and new links. Deliberately excludes review actions
// (approve/discard) — those are resolutions of something already made,
// not new material themselves (see the plan doc's "The weekly digest").
type ConstellationWeekItem struct {
	Kind      string    `json:"kind"` // "new" | "updated" | "linked"
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"` // second star's title, for "linked"
	Timestamp time.Time `json:"timestamp"`
}

// GetConstellationWeekFeed returns the trailing-7-day activity feed,
// reverse-chronological. A star with created_at also in the last 7 days
// is "new", never "updated" too, even if its updated_at also falls in the
// window — the plan doc's item is about what happened to a star this
// week, and a star that's brand new this week already covers that.
func (s *Store) GetConstellationWeekFeed() ([]ConstellationWeekItem, error) {
	var items []ConstellationWeekItem

	newRows, err := s.db.Query(`SELECT title, created_at FROM stars WHERE disabled = 0 AND created_at >= datetime('now', '-7 days')`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
	}
	for newRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "new"
		if err := newRows.Scan(&it.Title, &it.Timestamp); err != nil {
			newRows.Close()
			return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
		}
		items = append(items, it)
	}
	newRows.Close()
	if err := newRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
	}

	updatedRows, err := s.db.Query(`
		SELECT title, updated_at FROM stars
		WHERE disabled = 0 AND updated_at >= datetime('now', '-7 days') AND created_at < datetime('now', '-7 days')`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: updated stars: %w", err)
	}
	for updatedRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "updated"
		if err := updatedRows.Scan(&it.Title, &it.Timestamp); err != nil {
			updatedRows.Close()
			return nil, fmt.Errorf("constellation week feed: updated stars: %w", err)
		}
		items = append(items, it)
	}
	updatedRows.Close()
	if err := updatedRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation week feed: updated stars: %w", err)
	}

	linkRows, err := s.db.Query(`
		SELECT sa.title, sb.title, e.created_at FROM star_edges e
		JOIN stars sa ON sa.id = e.star_a_id
		JOIN stars sb ON sb.id = e.star_b_id
		WHERE e.created_at >= datetime('now', '-7 days')`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: links: %w", err)
	}
	for linkRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "linked"
		if err := linkRows.Scan(&it.Title, &it.Detail, &it.Timestamp); err != nil {
			linkRows.Close()
			return nil, fmt.Errorf("constellation week feed: links: %w", err)
		}
		items = append(items, it)
	}
	linkRows.Close()
	if err := linkRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation week feed: links: %w", err)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Timestamp.After(items[j].Timestamp) })
	return items, nil
}

// EligibleConstellationThreadsForBackfill is EligibleConstellationThreads
// with the idle-timing gate bypassed (a historical thread is definitionally
// not "mid-conversation" — see the plan doc's "Backfill") and ordered
// newest-active-first, optionally capped at limit (0 means every eligible
// thread) — the -n flag `polaris constellation backfill` exposes for
// testing against a small sample instead of a full backlog run.
func (s *Store) EligibleConstellationThreadsForBackfill(limit int) ([]string, error) {
	ids, err := s.EligibleConstellationThreads(0)
	if err != nil {
		return nil, err
	}

	type idWithTime struct {
		id string
		t  time.Time
	}
	withTimes := make([]idWithTime, 0, len(ids))
	for _, id := range ids {
		// Scanned as a string, not time.Time directly — MAX() over a
		// DATETIME column loses the driver's usual column-type affinity
		// hint, so modernc.org/sqlite hands back a plain string here even
		// though a direct column SELECT would auto-parse.
		var raw string
		if err := s.db.QueryRow(`SELECT MAX(created_at) FROM messages WHERE thread_id = ?`, id).Scan(&raw); err != nil {
			return nil, fmt.Errorf("eligible constellation threads for backfill: %w", err)
		}
		t, err := time.Parse("2006-01-02 15:04:05", raw)
		if err != nil {
			// Millisecond-precision timestamps (see RecordSearch's own
			// doc comment on this exact format split) use a different
			// layout — try that before giving up.
			t, err = time.Parse("2006-01-02 15:04:05.999999999", raw)
			if err != nil {
				return nil, fmt.Errorf("eligible constellation threads for backfill: parsing %q: %w", raw, err)
			}
		}
		withTimes = append(withTimes, idWithTime{id, t})
	}
	sort.Slice(withTimes, func(i, j int) bool { return withTimes[i].t.After(withTimes[j].t) })
	if limit > 0 && limit < len(withTimes) {
		withTimes = withTimes[:limit]
	}

	out := make([]string, len(withTimes))
	for i, wt := range withTimes {
		out[i] = wt.id
	}
	return out, nil
}

// EligibleConstellationThreads returns the ids of every thread the
// scheduler should hand to Weaver this tick — see the plan doc's "Thread
// eligibility":
//   - disabled threads are excluded outright, full stop
//   - idle-timing gate: the thread's most recent message is at least
//     pollIntervalMinutes old — "don't grab a conversation mid-thought"
//   - retry gate: the thread's most recent run has needs_retry = 1 —
//     eligible unconditionally, regardless of the delta gate below
//   - delta gate (only checked when the retry gate doesn't already apply):
//     no prior run at all, or new messages since the last run's
//     last_message_id_seen
func (s *Store) EligibleConstellationThreads(pollIntervalMinutes int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT t.id
		FROM threads t
		WHERE t.disabled = 0
		  AND (SELECT MAX(m.created_at) FROM messages m WHERE m.thread_id = t.id) <= datetime('now', '-' || ? || ' minutes')
		  AND (
		    (SELECT r.needs_retry FROM shooting_star_runs r
		      WHERE r.thread_id = t.id ORDER BY r.id DESC LIMIT 1) = 1
		    OR NOT EXISTS (SELECT 1 FROM shooting_star_runs r2 WHERE r2.thread_id = t.id)
		    OR (SELECT MAX(m2.id) FROM messages m2 WHERE m2.thread_id = t.id) > (
		      SELECT r3.last_message_id_seen FROM shooting_star_runs r3
		      WHERE r3.thread_id = t.id ORDER BY r3.id DESC LIMIT 1
		    )
		  )
		ORDER BY t.id`,
		pollIntervalMinutes,
	)
	if err != nil {
		return nil, fmt.Errorf("eligible constellation threads: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eligible constellation threads: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func toAnySlice(strs []string) []any {
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out
}
