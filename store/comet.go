// comet.go persists Comet, the monthly (and eventually yearly)
// retrospective — see docs/plans/comet.md. Named for the periodic-return
// astronomical object: a saved cadence that produces something you read
// rather than type into, just with a retrospective lens. Three tables:
// comet_config (the singleton settings row), comet_editions (one assembled
// page per period), and trails (one classification row per thread per
// period, written by an Approach run).
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrCometEditionNotFound is returned by GetCometEdition/LatestCometEdition
// when no edition exists for the requested/any period.
var ErrCometEditionNotFound = errors.New("comet edition not found")

// ErrCometClassifyAlreadyRunning is returned by SetCometClassifyStarted
// when a classify pass is already in progress — see that function's doc
// comment.
var ErrCometClassifyAlreadyRunning = errors.New("comet classify already running")

// CometConfig is Comet's singleton settings row.
type CometConfig struct {
	Enabled   bool   `json:"enabled"`
	TimeOfDay string `json:"time_of_day"`
	// Model: empty means "use whatever config.DefaultModel currently
	// resolves to" — same empty-means-inherit pattern
	// ConstellationConfig.Model uses. Used for both the Approach classifier
	// and the spike/fade synthesis pass.
	Model           string     `json:"model"`
	CreatedAt       time.Time  `json:"created_at"`
	LastGeneratedAt *time.Time `json:"last_generated_at"`
	LastAttemptAt   *time.Time `json:"last_attempt_at"`
	// ClassifyStartedAt: nil means no manual classify pass is running — see
	// the schema comment on this column for why it's a DB column rather
	// than an in-process flag.
	ClassifyStartedAt *time.Time `json:"classify_started_at"`
}

// GetCometConfig returns the singleton config row, inserting the
// column-default row first if this is the very first read — same
// first-read-inserts-the-row pattern GetDailyConfig/GetConstellationConfig
// use.
func (s *Store) GetCometConfig() (*CometConfig, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO comet_config (id) VALUES (1)`); err != nil {
		return nil, fmt.Errorf("get comet config: %w", err)
	}
	var c CometConfig
	err := s.db.QueryRow(
		`SELECT enabled, time_of_day, model, created_at, last_generated_at, last_attempt_at, classify_started_at
		 FROM comet_config WHERE id = 1`,
	).Scan(&c.Enabled, &c.TimeOfDay, &c.Model, &c.CreatedAt, &c.LastGeneratedAt, &c.LastAttemptAt, &c.ClassifyStartedAt)
	if err != nil {
		return nil, fmt.Errorf("get comet config: %w", err)
	}
	return &c, nil
}

// UpdateCometConfig writes the settings-panel-editable fields — does not
// touch last_generated_at/last_attempt_at/classify_started_at, which only
// the scheduler and the classify runner write.
func (s *Store) UpdateCometConfig(enabled bool, timeOfDay, model string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO comet_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("update comet config: %w", err)
	}
	_, err := s.db.Exec(
		`UPDATE comet_config SET enabled = ?, time_of_day = ?, model = ? WHERE id = 1`,
		enabled, timeOfDay, model,
	)
	if err != nil {
		return fmt.Errorf("update comet config: %w", err)
	}
	return nil
}

// SetCometLastGenerated records a successful edition assembly — the
// due-check's own baseline, same shape as SetDailyLastGenerated.
func (s *Store) SetCometLastGenerated(when string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO comet_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set comet last generated: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE comet_config SET last_generated_at = ? WHERE id = 1`, when); err != nil {
		return fmt.Errorf("set comet last generated: %w", err)
	}
	return nil
}

// SetCometLastAttempt stamps every generation attempt, success or failure —
// retry throttling reads it (see the schema comment on this column), so a
// failed run doesn't re-fire on the very next once-a-minute tick.
func (s *Store) SetCometLastAttempt(when string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO comet_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set comet last attempt: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE comet_config SET last_attempt_at = ? WHERE id = 1`, when); err != nil {
		return fmt.Errorf("set comet last attempt: %w", err)
	}
	return nil
}

// SetCometClassifyStarted marks a classify pass as in progress — called
// once at the very start of a `polaris comet classify` run, before it reads
// the eligible-threads list. Same compare-and-set shape (and the same
// reasoning) as SetConstellationBackfillStarted: a real CAS, not a blind
// UPDATE, so two concurrent runs can't both "win" and process overlapping
// threads. staleAfter lets a crashed run's marker be ignored rather than
// wedging every future run.
func (s *Store) SetCometClassifyStarted(staleAfter time.Duration) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO comet_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set comet classify started: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE comet_config
		 SET classify_started_at = CURRENT_TIMESTAMP
		 WHERE id = 1 AND (classify_started_at IS NULL OR classify_started_at <= datetime('now', printf('-%d seconds', ?)))`,
		int(staleAfter.Seconds()),
	)
	if err != nil {
		return fmt.Errorf("set comet classify started: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set comet classify started: %w", err)
	}
	if n == 0 {
		return ErrCometClassifyAlreadyRunning
	}
	return nil
}

// ClearCometClassifyStarted marks a classify pass as finished — called via
// defer so it clears on every exit path, success or error.
func (s *Store) ClearCometClassifyStarted() error {
	if _, err := s.db.Exec(`UPDATE comet_config SET classify_started_at = NULL WHERE id = 1`); err != nil {
		return fmt.Errorf("clear comet classify started: %w", err)
	}
	return nil
}

// CometEdition is one assembled period's retrospective page.
type CometEdition struct {
	// Period is "YYYY-MM" for a monthly edition, "YYYY" for a yearly one.
	Period string `json:"period"`
	// Kind is "monthly" | "yearly".
	Kind string `json:"kind"`
	// Hook is the one free-text editorial sentence the page is allowed.
	Hook      string           `json:"hook"`
	Data      CometEditionData `json:"data"`
	CostUSD   float64          `json:"cost_usd"`
	CreatedAt time.Time        `json:"created_at"`
}

// CometEditionData is the rendered content of one edition — the four fixed
// blocks plus the small bits the page needs to frame them. Stored as one
// JSON blob (see comet_editions' schema comment). The block shapes are why
// store can't simply hand the gateway an opaque blob: store marshals the
// typed Go value, and the frontend decodes the same field names.
type CometEditionData struct {
	// PeriodLabel is the human label for the masthead, e.g. "September 2026".
	PeriodLabel string          `json:"period_label"`
	Stats       CometStats      `json:"stats"`
	Categories  []CometCategory `json:"categories"`
	Spiked      []CometTrend    `json:"spiked"`
	Faded       []CometTrend    `json:"faded"`
	Stars       []CometStar     `json:"stars"`
	// StarTotal is how many stars formed this period in total — Stars is a
	// capped showcase ("+N more"), so the frontend needs the full count to
	// compute the remainder.
	StarTotal int `json:"star_total"`
	// GhostCount is exactly one number from ghost mode: how many times it
	// was used this period, nothing about content — see the plan doc's
	// "Eligibility filter".
	GhostCount int `json:"ghost_count"`

	// Highlights are the (up to three) conversations the synthesis pass
	// picked as defining the month — the "sentence + short list" the
	// masthead carries alongside Hook. Titles/ids are resolved server-side
	// from the candidate list the model chose from, never trusted verbatim.
	Highlights []CometHighlight `json:"highlights"`
	// Rhythm is the activity heatmap/time-of-day read, pure aggregation over
	// user-message timestamps.
	Rhythm CometRhythm `json:"rhythm"`
	// Tools is how often each user-facing tool actually fired this period,
	// most-used first. Weaver/Approach-internal tools are excluded — see
	// CometPeriodToolCounts.
	Tools []CometToolCount `json:"tools"`
	// Growth is Constellation's month: new stars, stars that grew via a
	// content merge, new star-to-star links, and a featured star. All zero
	// when Constellation is off or unused — the frontend hides the block
	// rather than showing an empty shell.
	Growth CometGrowth `json:"growth"`
	// UnresolvedCount is how many of the period's trails ended 'no' or
	// 'partial' — feeds the hook's framing, not (yet) its own block.
	UnresolvedCount int `json:"unresolved_count"`
}

// CometHighlight is one conversation picked as defining the month.
type CometHighlight struct {
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
	Topic    string `json:"topic,omitempty"`
	// Why is the model's one-line justification, shown after the title.
	Why string `json:"why"`
}

// CometRhythm is the activity-rhythm block: one count per calendar day in
// the period (for the heatmap), an hour-of-day histogram, and the peak.
type CometRhythm struct {
	Days         []CometDayCount `json:"days"`
	ActiveDays   int             `json:"active_days"`
	DaysInPeriod int             `json:"days_in_period"`
	// Hours is a 24-slot histogram in the operator's own zone.
	Hours    [24]int `json:"hours"`
	PeakHour int     `json:"peak_hour"`
	// BusiestDate is "YYYY-MM-DD", the day with the most messages.
	BusiestDate  string `json:"busiest_date"`
	BusiestCount int    `json:"busiest_count"`
}

// CometDayCount is one calendar day's message count.
type CometDayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// CometToolCount is one tool's period usage.
type CometToolCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CometGrowth is Constellation's month-over-month growth.
type CometGrowth struct {
	NewStars   int        `json:"new_stars"`
	GrownStars int        `json:"grown_stars"`
	NewLinks   int        `json:"new_links"`
	Featured   *CometStar `json:"featured,omitempty"`
}

// CometThreadCandidate is one thread offered to the synthesis pass to pick
// highlights from — its title/topic/mood plus how many turns it ran.
type CometThreadCandidate struct {
	ThreadID string
	Title    string
	Topic    string
	Mood     string
	Resolved string
	Turns    int
}

// CometStats is the stat row — straight aggregation, no synthesis.
type CometStats struct {
	Turns    int     `json:"turns"`
	NewStars int     `json:"new_stars"`
	Searches int     `json:"searches"`
	CostUSD  float64 `json:"cost_usd"`
}

// CometCategory is one category-breakdown row: a topic (Trail.Topic, which
// reuses the stars.category vocabulary) and how many new trails this period
// landed in it.
type CometCategory struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CometTrend is one spiked/faded item. Count is how many searches it had
// this period (spiked) or in the trailing baseline (faded); Delta/Since
// are display strings the synthesis pass supplies.
type CometTrend struct {
	Topic string `json:"topic"`
	Count int    `json:"count"`
	// Delta is e.g. "↑ 11 searches" — pre-rendered server-side so the
	// frontend doesn't have to know spike-vs-fade phrasing rules.
	Delta string `json:"delta,omitempty"`
	// Since is e.g. "quiet since Aug 14" for a faded item.
	Since string `json:"since,omitempty"`
}

// CometStar is one capped-showcase star.
type CometStar struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
}

// UpsertCometEdition writes one period's assembled edition — overwrites
// rather than duplicates on a second run for the same period (e.g. a manual
// re-trigger), since period is the natural key, same shape as
// UpsertDailyEdition.
func (s *Store) UpsertCometEdition(period, kind, hook string, data CometEditionData, costUSD float64) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("upsert comet edition: encode data: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO comet_editions (period, kind, hook, data, cost_usd)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(period) DO UPDATE SET
			kind = excluded.kind, hook = excluded.hook, data = excluded.data, cost_usd = excluded.cost_usd`,
		period, kind, hook, string(dataJSON), costUSD,
	)
	if err != nil {
		return fmt.Errorf("upsert comet edition: %w", err)
	}
	return nil
}

func scanCometEdition(row interface{ Scan(...interface{}) error }) (*CometEdition, error) {
	var e CometEdition
	var dataJSON string
	if err := row.Scan(&e.Period, &e.Kind, &e.Hook, &dataJSON, &e.CostUSD, &e.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(dataJSON), &e.Data); err != nil {
		return nil, fmt.Errorf("decode comet edition data: %w", err)
	}
	return &e, nil
}

// GetCometEdition returns one period's edition.
func (s *Store) GetCometEdition(period string) (*CometEdition, error) {
	e, err := scanCometEdition(s.db.QueryRow(
		`SELECT period, kind, hook, data, cost_usd, created_at FROM comet_editions WHERE period = ?`, period,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCometEditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get comet edition: %w", err)
	}
	return e, nil
}

// LatestCometEdition returns the most recent edition by period string —
// period sorts lexicographically for both "YYYY-MM" and "YYYY", so a plain
// DESC works. The frontend's initial load uses this to mean "whatever the
// newest edition is" without first having to guess the current period.
func (s *Store) LatestCometEdition() (*CometEdition, error) {
	e, err := scanCometEdition(s.db.QueryRow(
		`SELECT period, kind, hook, data, cost_usd, created_at FROM comet_editions ORDER BY period DESC LIMIT 1`,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCometEditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("latest comet edition: %w", err)
	}
	return e, nil
}

// ListCometPeriods returns every period that has an edition, newest first —
// backs the frontend's ← / → edition navigation and its "has anything been
// generated yet" state.
func (s *Store) ListCometPeriods() ([]string, error) {
	rows, err := s.db.Query(`SELECT period FROM comet_editions ORDER BY period DESC`)
	if err != nil {
		return nil, fmt.Errorf("list comet periods: %w", err)
	}
	defer rows.Close()
	var periods []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		periods = append(periods, p)
	}
	return periods, rows.Err()
}

// Trail is one thread's classification for one period — see the trails
// schema comment. ThreadTitle is populated only by the join-backed lookups
// (SearchTrails) so the UI/tool result can name the thread without a second
// query.
type Trail struct {
	ID           int64      `json:"id"`
	ThreadID     string     `json:"thread_id"`
	ThreadTitle  string     `json:"thread_title,omitempty"`
	Period       string     `json:"period"`
	Topic        string     `json:"topic"`
	Mood         string     `json:"mood"`
	Intent       string     `json:"intent"`
	Resolved     string     `json:"resolved"`
	StartedAt    time.Time  `json:"started_at"`
	ClassifiedAt *time.Time `json:"classified_at"`
	NeedsRetry   bool       `json:"needs_retry"`
	Error        string     `json:"error"`
	CostUSD      float64    `json:"cost_usd"`
}

// StartTrail opens (or re-opens) the (thread_id, period) row for an
// Approach run and returns its id. An existing row — from a failed or
// abandoned earlier attempt — is reset in place rather than duplicated, so
// a retry always writes the same row. classified_at is cleared, which is
// exactly what re-marks this thread eligible if the run dies mid-flight.
func (s *Store) StartTrail(threadID, period string) (int64, error) {
	_, err := s.db.Exec(
		`INSERT INTO trails (thread_id, period) VALUES (?, ?)
		 ON CONFLICT(thread_id, period) DO UPDATE SET
			started_at = CURRENT_TIMESTAMP, classified_at = NULL, needs_retry = 0, error = '', cost_usd = 0`,
		threadID, period,
	)
	if err != nil {
		return 0, fmt.Errorf("start trail: %w", err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM trails WHERE thread_id = ? AND period = ?`, threadID, period).Scan(&id); err != nil {
		return 0, fmt.Errorf("start trail: %w", err)
	}
	return id, nil
}

// FinishTrail records a completed Approach classification.
func (s *Store) FinishTrail(id int64, topic, mood, intent, resolved string, costUSD float64) error {
	err := execOne(s.db.Exec(
		`UPDATE trails SET topic = ?, mood = ?, intent = ?, resolved = ?, classified_at = CURRENT_TIMESTAMP, needs_retry = 0, error = '', cost_usd = ?
		 WHERE id = ?`,
		topic, mood, intent, resolved, costUSD, id,
	))
	if err != nil {
		return fmt.Errorf("finish trail: %w", err)
	}
	return nil
}

// FailTrail marks an Approach run as needing retry.
func (s *Store) FailTrail(id int64, errMsg string, costUSD float64) error {
	err := execOne(s.db.Exec(
		`UPDATE trails SET error = ?, needs_retry = 1, cost_usd = ? WHERE id = ?`,
		errMsg, costUSD, id,
	))
	if err != nil {
		return fmt.Errorf("fail trail: %w", err)
	}
	return nil
}

// MarkStaleTrailsFailed closes out every trail row left mid-flight
// (classified_at IS NULL) for longer than staleAfter — the self-healing
// counterpart to FinishTrail/FailTrail for a process that died mid-run,
// same shape as MarkStaleShootingStarRunsFailed.
func (s *Store) MarkStaleTrailsFailed(staleAfter time.Duration) (int, error) {
	res, err := s.db.Exec(
		`UPDATE trails
		 SET needs_retry = 1, error = 'stale: approach never finished (process likely crashed or was killed)'
		 WHERE classified_at IS NULL AND needs_retry = 0 AND started_at <= datetime('now', printf('-%d seconds', ?))`,
		int(staleAfter.Seconds()),
	)
	if err != nil {
		return 0, fmt.Errorf("mark stale trails failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark stale trails failed: %w", err)
	}
	return int(n), nil
}

// HasInFlightTrail reports whether any trail row is currently mid-flight —
// the mirror of HasInFlightShootingStarRun, used by the Docker update
// watcher's busy check so a container recreation doesn't land out from
// under a running Approach batch.
func (s *Store) HasInFlightTrail() (bool, error) {
	var busy bool
	// needs_retry = 0 matters here: a failed Approach leaves classified_at
	// NULL too (it's only set on success), so without it this would report
	// busy=true forever after any single failure.
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM trails WHERE classified_at IS NULL AND needs_retry = 0)`).Scan(&busy)
	if err != nil {
		return false, fmt.Errorf("has in-flight trail: %w", err)
	}
	return busy, nil
}

func scanTrail(rows interface{ Scan(...interface{}) error }) (Trail, error) {
	var t Trail
	err := rows.Scan(&t.ID, &t.ThreadID, &t.Period, &t.Topic, &t.Mood, &t.Intent, &t.Resolved, &t.StartedAt, &t.ClassifiedAt, &t.NeedsRetry, &t.Error, &t.CostUSD)
	return t, err
}

const trailColumns = `id, thread_id, period, topic, mood, intent, resolved, started_at, classified_at, needs_retry, error, cost_usd`

// GetTrail returns one thread's trail for one period, or nil if none exists.
func (s *Store) GetTrail(threadID, period string) (*Trail, error) {
	t, err := scanTrail(s.db.QueryRow(`SELECT `+trailColumns+` FROM trails WHERE thread_id = ? AND period = ?`, threadID, period))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get trail: %w", err)
	}
	return &t, nil
}

// TrailsByThread returns every completed trail for one thread, oldest
// period first — the "picked back up from last period's result" context an
// Approach run for a later period gets (see docs/plans/comet.md's
// "Cross-month behavior").
func (s *Store) TrailsByThread(threadID string) ([]Trail, error) {
	rows, err := s.db.Query(
		`SELECT `+trailColumns+` FROM trails WHERE thread_id = ? AND classified_at IS NOT NULL ORDER BY period ASC`, threadID,
	)
	if err != nil {
		return nil, fmt.Errorf("trails by thread: %w", err)
	}
	defer rows.Close()
	var out []Trail
	for rows.Next() {
		t, err := scanTrail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TrailsForPeriod returns every completed trail for one period (newest
// thread first) — backs the category breakdown and the edition page's
// per-trail audit view.
func (s *Store) TrailsForPeriod(period string) ([]Trail, error) {
	rows, err := s.db.Query(
		`SELECT `+trailColumns+` FROM trails WHERE period = ? AND classified_at IS NOT NULL ORDER BY classified_at ASC`, period,
	)
	if err != nil {
		return nil, fmt.Errorf("trails for period: %w", err)
	}
	defer rows.Close()
	var out []Trail
	for rows.Next() {
		t, err := scanTrail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DistinctTrailTopics returns every topic value already recorded on a
// completed trail — the live vocabulary the Approach prompt shows the
// classifier so it extends existing labels instead of fragmenting them.
func (s *Store) DistinctTrailTopics() ([]string, error) {
	return s.distinctTrailColumn("topic")
}

// DistinctTrailMoods is the same lookup for mood.
func (s *Store) DistinctTrailMoods() ([]string, error) {
	return s.distinctTrailColumn("mood")
}

func (s *Store) distinctTrailColumn(column string) ([]string, error) {
	// column is a compile-time constant from the two wrappers above, never
	// user input, so string-concatenating it here is safe (SQLite can't
	// parameterize an identifier).
	rows, err := s.db.Query(
		`SELECT DISTINCT ` + column + ` FROM trails WHERE classified_at IS NOT NULL AND ` + column + ` != '' ORDER BY ` + column + ` ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("distinct trail %s: %w", column, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SearchTrails does a keyword LIKE lookup over completed trails' topic/
// mood/intent plus their thread titles — the search_trails tool's own
// lookup (see tools/search_trails.go). Deliberately a LIKE, not FTS: a
// Trail is a tiny row and the corpus is one row per thread per month, so
// there's nothing a second FTS index would buy.
func (s *Store) SearchTrails(query string, limit int) ([]Trail, error) {
	if limit <= 0 {
		limit = 8
	}
	like := "%" + strings.TrimSpace(query) + "%"
	rows, err := s.db.Query(
		`SELECT t.id, t.thread_id, t.period, t.topic, t.mood, t.intent, t.resolved, t.started_at, t.classified_at, t.needs_retry, t.error, t.cost_usd, COALESCE(th.title, '')
		 FROM trails t JOIN threads th ON th.id = t.thread_id
		 WHERE t.classified_at IS NOT NULL
		   AND (t.topic LIKE ? COLLATE NOCASE OR t.mood LIKE ? COLLATE NOCASE OR t.intent LIKE ? COLLATE NOCASE OR th.title LIKE ? COLLATE NOCASE)
		 ORDER BY t.period DESC LIMIT ?`,
		like, like, like, like, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search trails: %w", err)
	}
	defer rows.Close()
	var out []Trail
	for rows.Next() {
		var t Trail
		if err := rows.Scan(&t.ID, &t.ThreadID, &t.Period, &t.Topic, &t.Mood, &t.Intent, &t.Resolved, &t.StartedAt, &t.ClassifiedAt, &t.NeedsRetry, &t.Error, &t.CostUSD, &t.ThreadTitle); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EligibleCometThreads lists threads with message activity inside
// [startStr, endStr) that don't yet have a completed trail for period —
// the Approach worklist. Ordering is oldest-thread-first, matching
// EligibleConstellationThreadsForBackfill's own reasoning (a stable,
// deterministic order matters more than recency here; Approaches are
// independent, so order is cosmetic).
//
// Source exclusions are the plan doc's own eligibility filter: pulsiar
// routine pulses, Weaver runs, and comet_approach runs themselves are not
// conversations about the operator's interests. Atlas Quick Answers are
// deliberately included: they're real assistant research (full web_search
// tool use) and exactly the "what were you circling" signal Comet exists to
// surface, even though they can stay hidden from the sidebar. Ghost threads
// and edit/retry variants are excluded — a variant's root is the addressable
// thread, and a ghost turn was never meant to persist at all.
func (s *Store) EligibleCometThreads(startStr, endStr, period string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT t.id FROM threads t
		 WHERE t.disabled = 0 AND t.ghost = 0 AND t.fork_root_id = ''
		   AND t.source NOT IN ('pulsar', 'weaver', 'comet_approach')
		   AND EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id AND m.created_at >= ? AND m.created_at < ?)
		   AND NOT EXISTS (SELECT 1 FROM trails tr WHERE tr.thread_id = t.id AND tr.period = ? AND tr.classified_at IS NOT NULL)
		 ORDER BY t.created_at ASC`,
		startStr, endStr, period,
	)
	if err != nil {
		return nil, fmt.Errorf("eligible comet threads: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EarliestMessageAt returns the earliest messages.created_at (UTC DB
// format) with ok=false when there are no messages at all — the default
// lower bound for a month-by-month Comet backfill.
func (s *Store) EarliestMessageAt() (at string, ok bool, err error) {
	var v sql.NullString
	if err := s.db.QueryRow(`SELECT MIN(created_at) FROM messages`).Scan(&v); err != nil {
		return "", false, fmt.Errorf("earliest message at: %w", err)
	}
	if !v.Valid {
		return "", false, nil
	}
	return v.String, true, nil
}

// CometPeriodCounts returns the stat row's raw counts for [startStr,
// endStr): distinct turns asked and web_search calls run. Both are the same
// shape GetStats aggregates, just over a calendar period rather than a
// trailing window.
func (s *Store) CometPeriodCounts(startStr, endStr string) (turns, searches int, err error) {
	if err = s.db.QueryRow(
		`SELECT COUNT(DISTINCT CASE WHEN role = 'assistant' AND turn_id != '' THEN turn_id END)
		 FROM messages WHERE created_at >= ? AND created_at < ?`, startStr, endStr,
	).Scan(&turns); err != nil {
		return 0, 0, fmt.Errorf("comet period counts: %w", err)
	}
	if err = s.db.QueryRow(
		`SELECT COUNT(*) FROM events
		 WHERE source = 'tool.web_search' AND message = 'tool call started' AND created_at >= ? AND created_at < ?`, startStr, endStr,
	).Scan(&searches); err != nil {
		return 0, 0, fmt.Errorf("comet period counts: %w", err)
	}
	return turns, searches, nil
}

// CometPeriodCost sums real LLM spend for [startStr, endStr) across every
// ledger GetStats already sums — messages (deduped the same way, see
// costBySource), Daily editions, Constellation events, ghost usage, and
// aux usage. This is a period-bounded mirror of GetStats' own grand total,
// not a new source of truth.
func (s *Store) CometPeriodCost(startStr, endStr string) (float64, error) {
	var total float64
	// messages: same per-real-message dedup as costBySource, bounded to the
	// period. Fork copies share role+turn_id, so grouping collapses them.
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost), 0) FROM (
			SELECT MAX(m.cost_usd) AS cost FROM messages m
			WHERE m.created_at >= ? AND m.created_at < ?
			GROUP BY m.role, m.turn_id, CASE WHEN m.turn_id = '' THEN m.created_at || m.content END)`,
		startStr, endStr,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	add := func(query string, args ...interface{}) error {
		var v float64
		if err := s.db.QueryRow(query, args...).Scan(&v); err != nil {
			return err
		}
		total += v
		return nil
	}
	if err := add(`SELECT COALESCE(SUM(cost_usd), 0) FROM pulsar_daily_editions WHERE created_at >= ? AND created_at < ?`, startStr, endStr); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	if err := add(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE created_at >= ? AND created_at < ?`, startStr, endStr); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	if err := add(`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events WHERE created_at >= ? AND created_at < ?`, startStr, endStr); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	if err := add(`SELECT COALESCE(SUM(cost_usd), 0) FROM ghost_usage WHERE created_at >= ? AND created_at < ?`, startStr, endStr); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	if err := add(`SELECT COALESCE(SUM(cost_usd), 0) FROM aux_usage WHERE created_at >= ? AND created_at < ?`, startStr, endStr); err != nil {
		return 0, fmt.Errorf("comet period cost: %w", err)
	}
	return total, nil
}

// CometPeriodCategoryBreakdown groups completed trails for period by topic,
// most-common first, capped at limit (the plan doc's hard cap of 5).
func (s *Store) CometPeriodCategoryBreakdown(period string, limit int) ([]CometCategory, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.Query(
		`SELECT topic, COUNT(*) FROM trails
		 WHERE period = ? AND classified_at IS NOT NULL AND topic != ''
		 GROUP BY topic ORDER BY COUNT(*) DESC, topic ASC LIMIT ?`,
		period, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("comet category breakdown: %w", err)
	}
	defer rows.Close()
	var out []CometCategory
	for rows.Next() {
		var c CometCategory
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CometQueryStat is one distinct web_search query and its use within a
// time range — the raw input the spike/fade synthesis clusters.
// LastSeen is the most recent created_at for this query in the range,
// formatted "YYYY-MM-DD", so a faded cluster can be labelled "quiet since
// ..." without a second query.
type CometQueryStat struct {
	Query    string `json:"query"`
	Count    int    `json:"count"`
	LastSeen string `json:"last_seen"`
}

// CometSearchQueryStats returns distinct web_search query strings used
// within [startStr, endStr) with their counts and last-seen date. Read
// from events (not search_history, which dedupes on query text and only
// sees Atlas's own search box); one row per real assistant tool call.
func (s *Store) CometSearchQueryStats(startStr, endStr string) ([]CometQueryStat, error) {
	rows, err := s.db.Query(
		`SELECT json_extract(data, '$.args.query') AS q, COUNT(*), MAX(created_at) FROM events
		 WHERE source = 'tool.web_search' AND message = 'tool call started' AND created_at >= ? AND created_at < ?
		   AND json_valid(data) AND json_extract(data, '$.args.query') IS NOT NULL
		 GROUP BY q ORDER BY COUNT(*) DESC`,
		startStr, endStr,
	)
	if err != nil {
		return nil, fmt.Errorf("comet search query stats: %w", err)
	}
	defer rows.Close()
	var out []CometQueryStat
	for rows.Next() {
		var q, lastSeen string
		var n int
		if err := rows.Scan(&q, &n, &lastSeen); err != nil {
			return nil, err
		}
		if strings.TrimSpace(q) == "" {
			continue
		}
		if len(lastSeen) >= 10 {
			lastSeen = lastSeen[:10]
		}
		out = append(out, CometQueryStat{Query: q, Count: n, LastSeen: lastSeen})
	}
	return out, rows.Err()
}

// CometPeriodNewStars returns up to limit stars created within [startStr,
// endStr) plus the full count for the period — the capped showcase ("4 of
// 8, +4 more").
func (s *Store) CometPeriodNewStars(startStr, endStr string, limit int) ([]CometStar, int, error) {
	var total int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM stars WHERE created_at >= ? AND created_at < ?`, startStr, endStr,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("comet new stars: %w", err)
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Query(
		`SELECT id, title, category FROM stars WHERE created_at >= ? AND created_at < ?
		 ORDER BY created_at DESC LIMIT ?`,
		startStr, endStr, limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("comet new stars: %w", err)
	}
	defer rows.Close()
	var out []CometStar
	for rows.Next() {
		var st CometStar
		if err := rows.Scan(&st.ID, &st.Title, &st.Category); err != nil {
			return nil, 0, err
		}
		out = append(out, st)
	}
	return out, total, rows.Err()
}

// CometPeriodGhostCount counts ghost_usage rows in [startStr, endStr) — the
// one ghost-mode number Comet surfaces, and nothing about their content.
func (s *Store) CometPeriodGhostCount(startStr, endStr string) (int, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM ghost_usage WHERE created_at >= ? AND created_at < ?`, startStr, endStr,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("comet ghost count: %w", err)
	}
	return n, nil
}

// cometInternalTools are the tools Comet's own reporting deliberately
// hides from its "tool texture" block: Weaver's five (search_stars/
// read_star/create_star/update_star/link_stars), the Approach classifier's
// own tools, and the other internal pipeline/UI helpers. They're part of
// automated subsystems (Constellation, Comet, Pulsar Daily, the wizard),
// not things the operator asked for — and Constellation may be off
// entirely while Comet is on, so surfacing its tools would be misleading.
var cometInternalTools = map[string]bool{
	"think":                  true,
	"search_stars":           true,
	"read_star":              true,
	"create_star":            true,
	"update_star":            true,
	"link_stars":             true,
	"search_trails":          true,
	"record_classification":  true,
	"finalize_daily_items":   true,
	"finalize_wizard_prompt": true,
	"alert_me":               true,
}

// CometPeriodActivity returns the timestamps of every user message in
// [startStr, endStr), oldest first — the raw material for the rhythm
// block. Bucketing happens in the gateway (not SQL) so it uses the
// operator's own time zone, DST included, rather than UTC date() strings.
func (s *Store) CometPeriodActivity(startStr, endStr string) ([]time.Time, error) {
	rows, err := s.db.Query(
		`SELECT created_at FROM messages WHERE role = 'user' AND created_at >= ? AND created_at < ? ORDER BY created_at ASC`,
		startStr, endStr,
	)
	if err != nil {
		return nil, fmt.Errorf("comet period activity: %w", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		// Scan into time.Time, not string: modernc.org/sqlite hands a
		// DATETIME column back as a real time.Time (its string form is
		// RFC3339, not the "2006-01-02 15:04:05" the column stores), so
		// parsing a string here silently dropped every row.
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t.UTC())
	}
	return out, rows.Err()
}

// CometPeriodToolCounts returns how often each user-facing tool fired in
// [startStr, endStr), most-used first. Internal/automated tools are
// filtered out — see cometInternalTools.
func (s *Store) CometPeriodToolCounts(startStr, endStr string) ([]CometToolCount, error) {
	rows, err := s.db.Query(
		`SELECT source, COUNT(*) FROM events
		 WHERE source LIKE 'tool.%' AND message = 'tool call finished' AND created_at >= ? AND created_at < ?
		 GROUP BY source`,
		startStr, endStr,
	)
	if err != nil {
		return nil, fmt.Errorf("comet period tool counts: %w", err)
	}
	defer rows.Close()
	var out []CometToolCount
	for rows.Next() {
		var source string
		var n int
		if err := rows.Scan(&source, &n); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(source, "tool.")
		if cometInternalTools[name] || n == 0 {
			continue
		}
		out = append(out, CometToolCount{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, rows.Err()
}

// CometPeriodGrowth reports Constellation's growth in [startStr, endStr):
// stars that already existed and got a real content merge (grown), star
// links added, and the single most-updated star (the featured one). New
// stars are counted by CometPeriodNewStars; the caller adds them. Every
// number is zero when Constellation is off — callers must treat that as
// "hide the block", never as an error.
func (s *Store) CometPeriodGrowth(startStr, endStr string) (grown, links int, featured *CometStar, err error) {
	if err = s.db.QueryRow(
		`SELECT COUNT(*) FROM stars WHERE content_updated_at >= ? AND content_updated_at < ? AND created_at < ?`,
		startStr, endStr, startStr,
	).Scan(&grown); err != nil {
		return 0, 0, nil, fmt.Errorf("comet period growth (grown): %w", err)
	}
	if err = s.db.QueryRow(
		`SELECT COUNT(*) FROM star_edges WHERE created_at >= ? AND created_at < ?`,
		startStr, endStr,
	).Scan(&links); err != nil {
		return 0, 0, nil, fmt.Errorf("comet period growth (links): %w", err)
	}
	var st CometStar
	ferr := s.db.QueryRow(
		`SELECT s.id, s.title, s.category FROM star_versions v JOIN stars s ON s.id = v.star_id
		 WHERE v.created_at >= ? AND v.created_at < ?
		 GROUP BY v.star_id ORDER BY COUNT(*) DESC, MAX(v.created_at) DESC LIMIT 1`,
		startStr, endStr,
	).Scan(&st.ID, &st.Title, &st.Category)
	if ferr == nil {
		featured = &st
	} else if ferr != sql.ErrNoRows {
		return 0, 0, nil, fmt.Errorf("comet period growth (featured): %w", ferr)
	}
	return grown, links, featured, nil
}

// CometPeriodCandidateThreads lists up to limit threads active in
// [startStr, endStr) for the synthesis pass to pick highlights from —
// most turns first, with whatever trail the period already has. title is
// used for display; the model only ever chooses by index, so a thread id
// never has to survive a round trip through the model.
func (s *Store) CometPeriodCandidateThreads(startStr, endStr, period string, limit int) ([]CometThreadCandidate, error) {
	if limit <= 0 {
		limit = 15
	}
	rows, err := s.db.Query(
		`SELECT t.id, t.title, COALESCE(tr.topic, ''), COALESCE(tr.mood, ''), COALESCE(tr.resolved, ''),
			(SELECT COUNT(*) FROM messages m WHERE m.thread_id = t.id AND m.role = 'user' AND m.created_at >= ? AND m.created_at < ?) AS turns
		 FROM threads t
		 LEFT JOIN trails tr ON tr.thread_id = t.id AND tr.period = ?
		 WHERE t.disabled = 0 AND t.ghost = 0 AND t.fork_root_id = ''
		   AND t.source NOT IN ('pulsar', 'weaver', 'comet_approach')
		   AND EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id AND m.created_at >= ? AND m.created_at < ?)
		 ORDER BY turns DESC, t.updated_at DESC
		 LIMIT ?`,
		startStr, endStr, period, startStr, endStr, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("comet period candidate threads: %w", err)
	}
	defer rows.Close()
	var out []CometThreadCandidate
	for rows.Next() {
		var c CometThreadCandidate
		if err := rows.Scan(&c.ThreadID, &c.Title, &c.Topic, &c.Mood, &c.Resolved, &c.Turns); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CometPeriodUnresolvedTopics lists distinct topics from the period's
// completed trails that ended 'no' or 'partial' (most-common first), plus
// the total number of unresolved trails — the hook's "what didn't get
// settled" signal.
func (s *Store) CometPeriodUnresolvedTopics(period string, limit int) (topics []string, total int, err error) {
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM trails WHERE period = ? AND classified_at IS NOT NULL AND resolved IN ('no', 'partial')`, period,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("comet period unresolved count: %w", err)
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Query(
		`SELECT topic FROM trails
		 WHERE period = ? AND classified_at IS NOT NULL AND resolved IN ('no', 'partial') AND topic != ''
		 GROUP BY topic ORDER BY COUNT(*) DESC, topic ASC LIMIT ?`,
		period, limit,
	)
	if err != nil {
		return nil, total, fmt.Errorf("comet period unresolved topics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, total, err
		}
		topics = append(topics, t)
	}
	return topics, total, rows.Err()
}
