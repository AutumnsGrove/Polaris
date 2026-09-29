// constellation.go implements Constellation's store layer (see
// docs/plans/constellation.md): constellation_config's singleton settings,
// stars and their FTS5 search, star_sources/star_edges, and the
// shooting_star_* observability tables Weaver's runs write to.
package store

import (
	"errors"
	"fmt"
	"time"
)

// ErrStarNotFound is returned by GetStar when no row matches the given id.
var ErrStarNotFound = errors.New("star not found")

// ErrBackfillAlreadyRunning is returned by SetConstellationBackfillStarted
// when a backfill is already in progress — see that function's doc comment.
var ErrBackfillAlreadyRunning = errors.New("constellation backfill already running")

// ConstellationConfig is Constellation's singleton settings row.
type ConstellationConfig struct {
	Enabled             bool       `json:"enabled"`
	PollIntervalMinutes int        `json:"poll_interval_minutes"`
	LastCheckedAt       *time.Time `json:"last_checked_at"`
	// Model: empty means "use whatever config.DefaultModel currently
	// resolves to" — same empty-means-inherit pattern
	// PulsarDailyConfig.WeatherLocation uses.
	Model string `json:"model"`
	// BackfillStartedAt: nil means no backfill is currently running — see
	// the schema comment on this column in store.go's `schema` const for
	// why this is a DB column and not an in-process flag.
	BackfillStartedAt *time.Time `json:"backfill_started_at"`
	CreatedAt         time.Time  `json:"created_at"`
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
		`SELECT enabled, poll_interval_minutes, last_checked_at, model, backfill_started_at, created_at
		 FROM constellation_config WHERE id = 1`,
	).Scan(&c.Enabled, &c.PollIntervalMinutes, &c.LastCheckedAt, &c.Model, &c.BackfillStartedAt, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get constellation config: %w", err)
	}
	return &c, nil
}

// SetConstellationBackfillStarted marks a backfill as in progress — called
// once at the very start of BackfillConstellation, before it reads the
// eligible-threads list, so the window where the live scheduler could still
// race it is as small as possible.
//
// A real compare-and-set (WHERE backfill_started_at IS NULL or stale,
// checking RowsAffected) rather than a blind UPDATE — a blind UPDATE let
// two concurrent BackfillConstellation calls (a double-click, a retried
// request after a timeout, or a bare-metal CLI run racing an HTTP-triggered
// one) both "win" and proceed to independently compute the eligible-threads
// list and run Weaver concurrently over overlapping threads, reproducing
// the exact class of live-observed duplicate-processing bug
// (BackfillConstellation's own doc comment) that this column exists to
// prevent, just via a different trigger than the one it was first fixed
// for. staleAfter mirrors runConstellationTick's own backfillStaleAfter
// allowance — without it, a backfill that crashed before reaching its own
// defer (ClearConstellationBackfillStarted) would wedge every future
// backfill attempt behind ErrBackfillAlreadyRunning forever, not just the
// scheduler's tick.
func (s *Store) SetConstellationBackfillStarted(staleAfter time.Duration) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO constellation_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set constellation backfill started: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE constellation_config
		 SET backfill_started_at = CURRENT_TIMESTAMP
		 WHERE id = 1 AND (backfill_started_at IS NULL OR backfill_started_at <= datetime('now', printf('-%d seconds', ?)))`,
		int(staleAfter.Seconds()),
	)
	if err != nil {
		return fmt.Errorf("set constellation backfill started: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set constellation backfill started: %w", err)
	}
	if n == 0 {
		return ErrBackfillAlreadyRunning
	}
	return nil
}

// ClearConstellationBackfillStarted marks a backfill as finished — called
// via defer in BackfillConstellation so it clears on every exit path,
// success or error, not just the happy path.
func (s *Store) ClearConstellationBackfillStarted() error {
	if _, err := s.db.Exec(`UPDATE constellation_config SET backfill_started_at = NULL WHERE id = 1`); err != nil {
		return fmt.Errorf("clear constellation backfill started: %w", err)
	}
	return nil
}

// UpdateConstellationConfig writes the settings-panel-editable fields.
// person_name/person_pronouns used to be set here too, before they moved to
// the general settings table (store.SetSetting) — see store.go's migration
// comment.
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
