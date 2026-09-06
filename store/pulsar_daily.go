// pulsar_daily.go persists Pulsar Daily — see docs/plans/pulsar-daily.md.
// Unlike pulsar.go's routines, this is a singleton: one config row (id
// fixed to 1) and one edition row per calendar date, not a table shaped
// for arbitrarily many independent schedules.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrDailyEditionNotFound is returned by GetDailyEdition when no edition
// exists for the requested date.
var ErrDailyEditionNotFound = errors.New("pulsar daily edition not found")

// PulsarDailyConfig is the Daily feature's singleton settings row.
type PulsarDailyConfig struct {
	EnabledBlocks []string `json:"enabled_blocks"`
	SportsTeams   string   `json:"sports_teams"`
	// CustomInstructions maps a block key to an optional free-text
	// steering instruction — see the schema comment on this column for
	// why it exists. Absent keys/empty values mean "use the plain
	// default framing" for that block.
	CustomInstructions map[string]string `json:"custom_instructions"`
	ArchitectModel     string            `json:"architect_model"`
	WriterModel        string            `json:"writer_model"`
	TimeOfDay          string            `json:"time_of_day"`
	CreatedAt          time.Time         `json:"created_at"`
	LastGeneratedAt    *time.Time        `json:"last_generated_at"`
}

// GetDailyConfig returns the singleton config row, inserting the
// column-default row first if this is the very first read — same idea as
// a routine's CreatedAt fallback, just applied to a table that's never
// supposed to be empty rather than to a single missing timestamp.
func (s *Store) GetDailyConfig() (*PulsarDailyConfig, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO pulsar_daily_config (id) VALUES (1)`); err != nil {
		return nil, fmt.Errorf("get daily config: %w", err)
	}
	var c PulsarDailyConfig
	var enabledBlocksJSON, customInstructionsJSON string
	err := s.db.QueryRow(
		`SELECT enabled_blocks, sports_teams, custom_instructions, architect_model, writer_model, time_of_day, created_at, last_generated_at
		 FROM pulsar_daily_config WHERE id = 1`,
	).Scan(&enabledBlocksJSON, &c.SportsTeams, &customInstructionsJSON, &c.ArchitectModel, &c.WriterModel, &c.TimeOfDay, &c.CreatedAt, &c.LastGeneratedAt)
	if err != nil {
		return nil, fmt.Errorf("get daily config: %w", err)
	}
	if err := json.Unmarshal([]byte(enabledBlocksJSON), &c.EnabledBlocks); err != nil {
		return nil, fmt.Errorf("get daily config: decode enabled_blocks: %w", err)
	}
	if err := json.Unmarshal([]byte(customInstructionsJSON), &c.CustomInstructions); err != nil {
		return nil, fmt.Errorf("get daily config: decode custom_instructions: %w", err)
	}
	return &c, nil
}

// UpdateDailyConfig overwrites the singleton config's editable fields —
// does not touch last_generated_at, which only the scheduler writes.
func (s *Store) UpdateDailyConfig(enabledBlocks []string, sportsTeams string, customInstructions map[string]string, architectModel, writerModel, timeOfDay string) error {
	enabledBlocksJSON, err := json.Marshal(enabledBlocks)
	if err != nil {
		return fmt.Errorf("update daily config: encode enabled_blocks: %w", err)
	}
	if customInstructions == nil {
		customInstructions = map[string]string{}
	}
	customInstructionsJSON, err := json.Marshal(customInstructions)
	if err != nil {
		return fmt.Errorf("update daily config: encode custom_instructions: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pulsar_daily_config (id, enabled_blocks, sports_teams, custom_instructions, architect_model, writer_model, time_of_day)
		 VALUES (1, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			enabled_blocks = excluded.enabled_blocks,
			sports_teams = excluded.sports_teams,
			custom_instructions = excluded.custom_instructions,
			architect_model = excluded.architect_model,
			writer_model = excluded.writer_model,
			time_of_day = excluded.time_of_day`,
		string(enabledBlocksJSON), sportsTeams, string(customInstructionsJSON), architectModel, writerModel, timeOfDay,
	)
	if err != nil {
		return fmt.Errorf("update daily config: %w", err)
	}
	return nil
}

// SetDailyLastGenerated records that Stage D just completed for today —
// called by the scheduler once assembly finishes, same "set right before
// the due-check would otherwise re-fire" shape as
// SetPulsarRoutineLastRun.
func (s *Store) SetDailyLastGenerated(when string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO pulsar_daily_config (id) VALUES (1)`); err != nil {
		return fmt.Errorf("set daily last generated: %w", err)
	}
	_, err := s.db.Exec(`UPDATE pulsar_daily_config SET last_generated_at = ? WHERE id = 1`, when)
	if err != nil {
		return fmt.Errorf("set daily last generated: %w", err)
	}
	return nil
}

// PulsarDailyBlock is one rendered block within an edition — the shape
// both the frontend's masonry page and the next day's Stage A diff-judge
// read.
type PulsarDailyBlock struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Gist       string `json:"gist"`
	IsTopStory bool   `json:"is_top_story"`
	ImageURL   string `json:"image_url,omitempty"`
	// Chart is raw tools.ChartSpec JSON, not a typed field — store can't
	// import the tools package (tools already imports store, for
	// tools/memory.go's use of it), so the gateway marshals whatever
	// tools.Context.ChartSnapshot() returned before this struct is built.
	// The frontend decodes it as the same ChartSpec shape ChartCard.svelte
	// already renders for chat turns.
	Chart json.RawMessage `json:"chart,omitempty"`
}

// PulsarDailyEdition is one calendar date's assembled Daily page.
type PulsarDailyEdition struct {
	Date   string             `json:"date"`
	Blocks []PulsarDailyBlock `json:"blocks"`
	// CostUSD is the total LLM spend across every stage that produced
	// this edition — see the schema comment on this column.
	CostUSD   float64   `json:"cost_usd"`
	CreatedAt time.Time `json:"created_at"`
}

// UpsertDailyEdition writes today's Stage D result — overwrites rather
// than duplicates on a second run for the same date (e.g. a manual
// re-trigger), since edition_date is the natural key, not an
// auto-incrementing history of attempts.
func (s *Store) UpsertDailyEdition(date string, blocks []PulsarDailyBlock, costUSD float64) error {
	blocksJSON, err := json.Marshal(blocks)
	if err != nil {
		return fmt.Errorf("upsert daily edition: encode blocks: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pulsar_daily_editions (edition_date, blocks, cost_usd)
		 VALUES (?, ?, ?)
		 ON CONFLICT(edition_date) DO UPDATE SET blocks = excluded.blocks, cost_usd = excluded.cost_usd`,
		date, string(blocksJSON), costUSD,
	)
	if err != nil {
		return fmt.Errorf("upsert daily edition: %w", err)
	}
	return nil
}

// GetDailyEdition returns one date's edition, or ErrDailyEditionNotFound
// if none exists yet — the normal case for "first ever day" (see the plan
// doc) and for any date nothing has generated for.
func (s *Store) GetDailyEdition(date string) (*PulsarDailyEdition, error) {
	var e PulsarDailyEdition
	var blocksJSON string
	err := s.db.QueryRow(
		`SELECT edition_date, blocks, cost_usd, created_at FROM pulsar_daily_editions WHERE edition_date = ?`, date,
	).Scan(&e.Date, &blocksJSON, &e.CostUSD, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDailyEditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get daily edition: %w", err)
	}
	if err := json.Unmarshal([]byte(blocksJSON), &e.Blocks); err != nil {
		return nil, fmt.Errorf("get daily edition: decode blocks: %w", err)
	}
	return &e, nil
}

// LatestDailyEdition returns the most recent edition strictly before the
// given date — what Stage A's diff-judge compares fresh content against,
// without assuming "yesterday" is literally date-1 (a missed day, e.g.
// server downtime, should still diff against the last real edition rather
// than finding nothing and treating every block as first-ever-day).
func (s *Store) LatestDailyEdition(beforeDate string) (*PulsarDailyEdition, error) {
	var e PulsarDailyEdition
	var blocksJSON string
	err := s.db.QueryRow(
		`SELECT edition_date, blocks, cost_usd, created_at FROM pulsar_daily_editions
		 WHERE edition_date < ? ORDER BY edition_date DESC LIMIT 1`, beforeDate,
	).Scan(&e.Date, &blocksJSON, &e.CostUSD, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDailyEditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("latest daily edition: %w", err)
	}
	if err := json.Unmarshal([]byte(blocksJSON), &e.Blocks); err != nil {
		return nil, fmt.Errorf("latest daily edition: decode blocks: %w", err)
	}
	return &e, nil
}

// NextDailyEdition returns the oldest edition strictly after the given
// date — the mirror image of LatestDailyEdition's "strictly before",
// backing the frontend's "forward" nav once a user has stepped back more
// than one day (previously ← Previous had no inverse, so browsing back
// two days and wanting to return to the first of them had no query to
// use short of re-fetching "latest").
func (s *Store) NextDailyEdition(afterDate string) (*PulsarDailyEdition, error) {
	var e PulsarDailyEdition
	var blocksJSON string
	err := s.db.QueryRow(
		`SELECT edition_date, blocks, cost_usd, created_at FROM pulsar_daily_editions
		 WHERE edition_date > ? ORDER BY edition_date ASC LIMIT 1`, afterDate,
	).Scan(&e.Date, &blocksJSON, &e.CostUSD, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDailyEditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("next daily edition: %w", err)
	}
	if err := json.Unmarshal([]byte(blocksJSON), &e.Blocks); err != nil {
		return nil, fmt.Errorf("next daily edition: decode blocks: %w", err)
	}
	return &e, nil
}

// PulsarDailyBlockTrace is one block's full recorded lifecycle for one
// edition date — see the schema comment on pulsar_daily_trace for why
// this exists (nothing about a stage's output or reasoning survived
// before this, only whatever made it into the final edition).
type PulsarDailyBlockTrace struct {
	EditionDate       string    `json:"edition_date"`
	BlockKey          string    `json:"block_key"`
	Title             string    `json:"title"`
	StageAContent     string    `json:"stage_a_content"`
	Verdict           string    `json:"verdict"`
	Gist              string    `json:"gist"`
	DiffReasoning     string    `json:"diff_reasoning"`
	Included          bool      `json:"included"`
	IsTopStory        bool      `json:"is_top_story"`
	TopStoryReasoning string    `json:"top_story_reasoning"`
	StageCContent     string    `json:"stage_c_content"`
	Error             string    `json:"error"`
	CostUSD           float64   `json:"cost_usd"`
	CreatedAt         time.Time `json:"created_at"`
}

// UpsertDailyBlockTrace records Stage A's (and, for a Watch block, the
// diff-judge's) output for one block — called once per enabled block per
// run, regardless of outcome, so a dropped ("unchanged") or hard-failed
// block still leaves a real record instead of vanishing with only a
// transient log line. Overwrites on a same-day rerun, same
// edition_date-is-the-natural-key reasoning as UpsertDailyEdition.
func (s *Store) UpsertDailyBlockTrace(t PulsarDailyBlockTrace) error {
	_, err := s.db.Exec(
		`INSERT INTO pulsar_daily_trace
		 (edition_date, block_key, title, stage_a_content, verdict, gist, diff_reasoning, error, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(edition_date, block_key) DO UPDATE SET
		   title = excluded.title,
		   stage_a_content = excluded.stage_a_content,
		   verdict = excluded.verdict,
		   gist = excluded.gist,
		   diff_reasoning = excluded.diff_reasoning,
		   error = excluded.error,
		   cost_usd = excluded.cost_usd,
		   -- A same-day rerun's fresh Stage A pass invalidates whatever
		   -- Stage D previously decided about this block — reset rather
		   -- than leave a stale included/top-story outcome from the prior
		   -- run sitting next to this run's new content until Stage D
		   -- gets around to deciding again.
		   included = 0,
		   is_top_story = 0,
		   top_story_reasoning = '',
		   stage_c_content = ''`,
		t.EditionDate, t.BlockKey, t.Title, t.StageAContent, t.Verdict, t.Gist, t.DiffReasoning, t.Error, t.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("upsert daily block trace: %w", err)
	}
	return nil
}

// UpdateDailyBlockTraceOutcome fills in what Stage D alone knows: whether
// a block actually made the final edition, whether it was elected Top
// Story, why (Stage B's reasoning), and the elaborated content if so.
// Called after UpsertDailyBlockTrace has already created the row for this
// block this run.
func (s *Store) UpdateDailyBlockTraceOutcome(editionDate, blockKey string, included, isTopStory bool, topStoryReasoning, stageCContent string, elabCost float64) error {
	_, err := s.db.Exec(
		`UPDATE pulsar_daily_trace SET
		   included = ?, is_top_story = ?, top_story_reasoning = ?, stage_c_content = ?, cost_usd = cost_usd + ?
		 WHERE edition_date = ? AND block_key = ?`,
		included, isTopStory, topStoryReasoning, stageCContent, elabCost, editionDate, blockKey,
	)
	if err != nil {
		return fmt.Errorf("update daily block trace outcome: %w", err)
	}
	return nil
}

// GetDailyTrace returns every block's recorded trace for one edition
// date, ordered by block_key for a stable, readable listing — the
// after-the-fact answer to "what did each stage actually think" that
// GetDailyEdition alone can't give (it only has whatever survived).
func (s *Store) GetDailyTrace(editionDate string) ([]PulsarDailyBlockTrace, error) {
	rows, err := s.db.Query(
		`SELECT edition_date, block_key, title, stage_a_content, verdict, gist, diff_reasoning,
		        included, is_top_story, top_story_reasoning, stage_c_content, error, cost_usd, created_at
		 FROM pulsar_daily_trace WHERE edition_date = ? ORDER BY block_key`, editionDate,
	)
	if err != nil {
		return nil, fmt.Errorf("get daily trace: %w", err)
	}
	defer rows.Close()

	out := []PulsarDailyBlockTrace{}
	for rows.Next() {
		var t PulsarDailyBlockTrace
		if err := rows.Scan(&t.EditionDate, &t.BlockKey, &t.Title, &t.StageAContent, &t.Verdict, &t.Gist,
			&t.DiffReasoning, &t.Included, &t.IsTopStory, &t.TopStoryReasoning, &t.StageCContent, &t.Error,
			&t.CostUSD, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("get daily trace: scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get daily trace: %w", err)
	}
	return out, nil
}
