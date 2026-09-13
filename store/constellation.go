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
	"strings"
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
	// Reasoning: only ever populated by handleListConstellationStars for
	// the "inbox" section (see LatestCandidateReasoningBulk) -- every
	// other reader of Star leaves this "", hence omitempty, so ListStars'
	// own SQL/scan stays untouched for the other three sections.
	Reasoning string `json:"reasoning,omitempty"`
}

// CreateStar writes a new stars row, using whatever Status the caller
// requests. Personal stars no longer get a forced 'proposed' override —
// see the plan doc's "Personal stars" section: that gate was a deliberate
// starting-point restriction, expected to soften once real usage showed
// whether the extra review friction was worth it. It wasn't (Weaver-written
// personal stars proved reliably well-written in practice), and the library
// pivoted to personal-only extraction, so treating every star as needing
// human review defeated the point of trusting it.
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
// title == "" means "leave the title as-is" — the Weaver background tool
// (tools/update_star.go) has no title field in its own schema at all and
// always passes "", while the Edit/Refine correction sheet
// (reconcileAndSaveStar) supplies whatever reconcileStarContent resolved,
// which is only ever non-empty when the correction actually changed what
// the star is about (see weaver.reconcile_system's TITLE: instructions) —
// before this, a correction that invalidated the original title (e.g. "it's
// fantasy, not sci-fi") could rewrite summary/body to match while the title
// silently kept describing the old, now-wrong premise.
//
// body and confidenceClass share title's own "" == "leave as-is" contract —
// update_star's tool schema (tools/update_star.go) only requires star_id
// and summary, so a model call that omits body/confidence_class (a
// plausible "just fixing the summary, no need to resend the body" call)
// used to unconditionally blank those columns out via the unconditional
// `body = ?, confidence = ?` this function ran before. tags similarly
// treats a nil slice as "leave as-is" (distinct from a non-nil empty slice,
// which really does mean "clear every tag" — json.Unmarshal only produces
// nil when the JSON key was absent, not when it was `[]`) and isPersonal is
// a *bool for the same reason (a plain bool has no way to represent "the
// model didn't say" separately from "the model said false").
//
// Content updates never touch status (personal or not) — see CreateStar's
// doc comment: the old isPersonal-forces-'proposed' gate was retired once
// the library pivoted to personal-only extraction, since forcing every
// single update back through human review defeated the point of trusting
// Weaver's personal-star writing, which real usage showed was reliable.
//
// Returns ErrStarNotFound if id doesn't match any row — previously a
// no-op UPDATE against a stale/hallucinated star_id silently "succeeded",
// so a caller (Weaver's update_star tool included) had no way to tell a
// real merge from one that touched nothing at all.
func (s *Store) UpdateStar(id int64, title, summary, body string, tags []string, confidenceClass string, isPersonal *bool) error {
	var tagsArg any
	if tags != nil {
		tagsJSON, err := json.Marshal(tags)
		if err != nil {
			return fmt.Errorf("update star: encode tags: %w", err)
		}
		tagsArg = string(tagsJSON)
	}
	var personalArg any
	if isPersonal != nil {
		personalArg = *isPersonal
	}

	query := `UPDATE stars SET
		title = CASE WHEN ? <> '' THEN ? ELSE title END,
		summary = ?,
		body = CASE WHEN ? <> '' THEN ? ELSE body END,
		tags = CASE WHEN ? IS NOT NULL THEN ? ELSE tags END,
		confidence = CASE WHEN ? <> '' THEN ? ELSE confidence END,
		is_personal = CASE WHEN ? IS NOT NULL THEN ? ELSE is_personal END,
		content_updated_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	args := []any{
		title, title,
		summary,
		body, body,
		tagsArg, tagsArg,
		confidenceClass, confidenceClass,
		personalArg, personalArg,
		id,
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update star: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update star: %w", err)
	}
	if n == 0 {
		return ErrStarNotFound
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

// DistinctCategories returns every category value currently in use across
// non-disabled stars, alphabetically — Weaver's escape hatch for a category
// outside its fixed list needs this to actually reuse an already-established
// overflow category instead of guessing blind (search_stars' own results
// don't carry category, see its doc comment).
func (s *Store) DistinctCategories() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT category FROM stars WHERE disabled = 0 ORDER BY category`)
	if err != nil {
		return nil, fmt.Errorf("distinct categories: %w", err)
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, fmt.Errorf("distinct categories: %w", err)
		}
		categories = append(categories, category)
	}
	return categories, rows.Err()
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
//
// query is rewritten into an OR of its individual words before hitting FTS5
// — passing a free-text phrase straight to MATCH hits FTS5's default
// implicit-AND-of-every-token behavior (no stemming either, since
// stars_fts uses the default unicode61 tokenizer), which requires every
// single word — including stop words like "of" — to literally co-occur in
// one row. Live-confirmed as the actual root cause of Weaver creating
// duplicate stars for the same topic: a real query like "purpose of life
// meaning existence" against an existing "The purpose/meaning of life —
// philosophical perspectives" star returned zero results (no literal
// "existence" token in that star), even though every individual word in
// the query overlaps. OR'ing the same terms against the same star finds it
// immediately. This is Weaver's own dedup mechanism doing exactly what the
// system prompt asks ("always call search_stars before create_star") and
// getting a false "nothing found" back.
func (s *Store) SearchStars(query string, limit int) ([]StarSearchResult, error) {
	rows, err := s.db.Query(
		// disabled = 0: a star the person explicitly hid via the overflow
		// menu's Disable action shouldn't stay a live lead for Weaver to
		// find, read, update, or link to — that would silently revive
		// content they asked to be removed from their library. Rejected
		// stars stay included on purpose (see below); disabled is a
		// different, unconditional "not for Weaver" signal.
		`SELECT s.id, s.title, s.summary, s.status
		 FROM stars_fts
		 JOIN stars s ON s.id = stars_fts.rowid
		 WHERE stars_fts MATCH ? AND s.disabled = 0
		 ORDER BY rank
		 LIMIT ?`, orFTSQuery(query), limit,
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

// SearchLibraryStars backs the Library's search box — deliberately not
// SearchStars reused directly: that one includes rejected stars on purpose
// (it doubles as Weaver's own "was this already said no to" check), which
// would be confusing surfaced to a person searching their own library. Only
// auto/confirmed, non-disabled stars are eligible, same set the
// library/about_you sections themselves show. Returns full Star rows (the
// same shape ListStars does), not a slimmer result type, so the frontend
// can render a hit with the exact same StarCard component the rest of the
// Library uses instead of a second, inconsistent-looking result row.
func (s *Store) SearchLibraryStars(query string, limit int) ([]Star, error) {
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.category, s.summary, s.body, s.tags, s.status, s.confidence, s.is_personal, s.disabled, s.created_at, s.updated_at
		 FROM stars_fts
		 JOIN stars s ON s.id = stars_fts.rowid
		 WHERE stars_fts MATCH ? AND s.disabled = 0 AND s.status IN ('auto', 'confirmed')
		 ORDER BY rank
		 LIMIT ?`, orFTSQuery(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search library stars: %w", err)
	}
	defer rows.Close()

	var results []Star
	for rows.Next() {
		var star Star
		var tagsJSON string
		var isPersonal, disabled int
		if err := rows.Scan(&star.ID, &star.Title, &star.Category, &star.Summary, &star.Body, &tagsJSON, &star.Status, &star.Confidence, &isPersonal, &disabled, &star.CreatedAt, &star.UpdatedAt); err != nil {
			return nil, fmt.Errorf("search library stars: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &star.Tags); err != nil {
			return nil, fmt.Errorf("search library stars: decode tags: %w", err)
		}
		star.IsPersonal = isPersonal != 0
		star.Disabled = disabled != 0
		results = append(results, star)
	}
	if results == nil {
		results = []Star{}
	}
	return results, rows.Err()
}

// orFTSQuery turns a free-text query into an FTS5 query string that
// matches a row containing ANY of the individual words, not (FTS5's
// default) every single one of them. Each word is double-quoted so
// FTS5-special characters in the query (hyphens, colons, quotes — real
// possibilities in a Weaver-generated search query) are treated as
// literal text instead of query syntax, which would otherwise return a
// query-syntax error for a query FTS5 rejects outright rather than a
// clean empty result.
func orFTSQuery(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return `""`
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
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

// StarSource is one thread that contributed to a star. LinkedAt is
// bookkeeping (when this star_sources row was written/refreshed, i.e. when
// Weaver actually processed it — often long after the fact for a backlog
// run); ThreadCreatedAt is the real-world date the conversation happened,
// which is what the UI should show a person as "when was this discussed" —
// conflating the two made a month-old thread processed overnight display as
// if it had just happened.
type StarSource struct {
	ThreadID        string    `json:"thread_id"`
	LinkedAt        time.Time `json:"linked_at"`
	ThreadCreatedAt time.Time `json:"thread_created_at"`
}

// StarSources lists the threads backing a star's "Linked articles" block.
func (s *Store) StarSources(starID int64) ([]StarSource, error) {
	rows, err := s.db.Query(
		`SELECT star_sources.thread_id, star_sources.linked_at, threads.created_at
		 FROM star_sources JOIN threads ON threads.id = star_sources.thread_id
		 WHERE star_sources.star_id = ? ORDER BY star_sources.linked_at DESC`,
		starID,
	)
	if err != nil {
		return nil, fmt.Errorf("star sources: %w", err)
	}
	defer rows.Close()

	var out []StarSource
	for rows.Next() {
		var src StarSource
		if err := rows.Scan(&src.ThreadID, &src.LinkedAt, &src.ThreadCreatedAt); err != nil {
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

// HasInFlightShootingStarRun reports whether any shooting_star_run is
// currently mid-flight (started_at set, finished_at still NULL) — the
// signal the Docker update watcher needs to avoid recreating the container
// out from under a running Weaver pass (see issue #57: a live update once
// landed exactly mid-batch, caught only because the batch happened to
// finish just before the container actually got recreated).
func (s *Store) HasInFlightShootingStarRun() (bool, error) {
	var busy bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM shooting_star_runs WHERE finished_at IS NULL)`).Scan(&busy)
	if err != nil {
		return false, fmt.Errorf("has in-flight shooting star run: %w", err)
	}
	return busy, nil
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
		// id DESC, not created_at DESC — created_at only has second
		// resolution, so two candidates recorded within the same second
		// would otherwise tie arbitrarily. See LatestCandidateReasoningBulk's
		// identical, already-fixed concern.
		`SELECT reasoning FROM shooting_star_candidates
		 WHERE resulting_star_id = ? ORDER BY id DESC LIMIT 1`,
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

// LatestCandidateReasoningBulk is LatestCandidateReasoning for a whole set
// of stars in one query — the Inbox list (handleListConstellationStars'
// "inbox" section) needs every proposed star's own "why" for its card,
// same reasoning text the Review screen's "Why this needs a look" block
// already surfaces, and doing that as one N-star query instead of N
// separate round trips. Stars with no candidate row (shouldn't normally
// happen) are simply absent from the returned map.
func (s *Store) LatestCandidateReasoningBulk(starIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(starIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(starIDs))
	for i, id := range starIDs {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT resulting_star_id, reasoning FROM shooting_star_candidates
		 WHERE resulting_star_id IN (`+placeholders(len(starIDs))+`)
		 ORDER BY id ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("latest candidate reasoning bulk: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var reasoning string
		if err := rows.Scan(&id, &reasoning); err != nil {
			return nil, fmt.Errorf("latest candidate reasoning bulk: %w", err)
		}
		// ORDER BY id ASC (not created_at, which only has second
		// resolution — two candidates for the same star recorded within
		// the same second would otherwise tie) + overwrite-on-scan means
		// the last write wins per id, i.e. the most recently inserted row.
		out[id] = reasoning
	}
	return out, rows.Err()
}

// RecordStarReconcileCost logs the real, billed cost of one Refine/Edit
// LLM call (see gateway/constellation_routes.go's reconcileAndSaveStar) —
// these calls aren't part of any shooting_star_runs row, so they can't use
// RecordShootingStarEvent (whose run_id column is NOT NULL). Recorded
// regardless of whether the correction turned out to be a flat denial
// (invalidated=true) — the model call itself was made and billed either
// way, so the cost is real even when its content is discarded.
func (s *Store) RecordStarReconcileCost(starID int64, costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO star_reconcile_events (star_id, cost_usd) VALUES (?, ?)`, starID, costUSD)
	if err != nil {
		return fmt.Errorf("record star reconcile cost: %w", err)
	}
	return nil
}

// SetStarStatusAndRecordReview atomically updates a star's status and logs
// the review action that caused it — approve/discard/refine each write
// both rows together, so a crash between the two writes (server restart,
// process kill) can never leave a star's status changed with no matching
// star_reviews row, or vice versa. Previously these were two independent
// Execs in the HTTP handler.
func (s *Store) SetStarStatusAndRecordReview(starID int64, status, action, correction string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE stars SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, starID); err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO star_reviews (star_id, action, correction) VALUES (?, ?, ?)`, starID, action, correction); err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	return tx.Commit()
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

	// Total/period cost is the sum of shooting_star_events (Weaver's own
	// runs) AND star_reconcile_events (Refine/Edit's one-off calls) — two
	// tables because the latter has no shooting_star_runs row to hang off
	// of (see RecordStarReconcileCost's doc comment), but both are real
	// billed spend and belong in the same total.
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events`).Scan(&stats.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: total cost: %w", err)
	}
	var reconcileTotal float64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events`).Scan(&reconcileTotal); err != nil {
		return nil, fmt.Errorf("constellation stats: total reconcile cost: %w", err)
	}
	stats.TotalCostUSD += reconcileTotal

	// periodDays is a Go int, never attacker-shaped, so the old
	// fmt.Sprintf-built WHERE clause wasn't actually exploitable — but
	// binding it as a real parameter (via printf's own '-%d days' inside
	// SQLite, since datetime()'s modifier can't itself be a bound string
	// built from a numeric arg without one more layer) matches every other
	// query in this file's fully-parameterized convention instead of being
	// the one exception a future edit could copy-paste into an actually
	// unsafe shape.
	periodFilter := "1=1"
	periodArgs := []any{}
	if periodDays > 0 {
		periodFilter = "created_at >= datetime('now', printf('-%d days', ?))"
		periodArgs = []any{periodDays}
	}

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE `+periodFilter, periodArgs...).Scan(&stats.PeriodCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: period cost: %w", err)
	}
	var reconcilePeriod float64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events WHERE `+periodFilter, periodArgs...).Scan(&reconcilePeriod); err != nil {
		return nil, fmt.Errorf("constellation stats: period reconcile cost: %w", err)
	}
	stats.PeriodCostUSD += reconcilePeriod

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

	// Distinct threads whose MOST RECENT run still needs a retry — not a
	// raw COUNT(*) over every needs_retry=1 row ever written. needs_retry
	// only ever gets updated on the run row it was set on; a thread that
	// failed once and later succeeded leaves that old row's flag sitting
	// at 1 forever, so a plain COUNT(*) silently double-counts history
	// instead of reporting what's actually stuck right now (confirmed
	// live: stayed at 10 even after every one of those 10 threads had
	// already been retried successfully). Mirrors the exact "most recent
	// run per thread" logic EligibleConstellationThreads' own retry gate
	// already uses, so this reports the same set the scheduler will
	// actually reattempt.
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT thread_id FROM shooting_star_runs r
			WHERE r.id = (SELECT r2.id FROM shooting_star_runs r2 WHERE r2.thread_id = r.thread_id ORDER BY r2.id DESC LIMIT 1)
			  AND r.needs_retry = 1
		)
	`).Scan(&stats.NeedsRetryCount); err != nil {
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
	// Joined to stars on both sides so a link touching a since-disabled star
	// doesn't still surface that star's title in the count/highlight — the
	// "new" count above already excludes disabled=1 stars the same way.
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM star_edges e
		 JOIN stars sa ON sa.id = e.star_a_id
		 JOIN stars sb ON sb.id = e.star_b_id
		 WHERE e.created_at >= datetime('now', '-7 days') AND sa.disabled = 0 AND sb.disabled = 0`,
	).Scan(&d.LinksCount); err != nil {
		return nil, fmt.Errorf("constellation digest: links count: %w", err)
	}

	var titleA, titleB string
	err := s.db.QueryRow(
		`SELECT sa.title, sb.title FROM star_edges e
		 JOIN stars sa ON sa.id = e.star_a_id
		 JOIN stars sb ON sb.id = e.star_b_id
		 WHERE e.created_at >= datetime('now', '-7 days') AND sa.disabled = 0 AND sb.disabled = 0
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
	Kind  string `json:"kind"` // "new" | "updated" | "linked"
	Title string `json:"title"`
	// StarID is what a tap on this row opens — the star Title belongs to
	// (the first side of the link, for "linked"). Previously missing
	// entirely, which is why "This week" rows couldn't be tapped through
	// to the actual star at all.
	StarID    int64     `json:"star_id"`
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

	newRows, err := s.db.Query(`SELECT id, title, created_at FROM stars WHERE disabled = 0 AND created_at >= datetime('now', '-7 days')`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
	}
	for newRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "new"
		if err := newRows.Scan(&it.StarID, &it.Title, &it.Timestamp); err != nil {
			newRows.Close()
			return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
		}
		items = append(items, it)
	}
	newRows.Close()
	if err := newRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation week feed: new stars: %w", err)
	}

	// content_updated_at, not updated_at — updated_at is bumped by every
	// mutator (rename, disable, a review action's status change), not just
	// a real content merge, which used to make e.g. approving a 10-day-old
	// proposed star show up here as "updated" — exactly the review-action
	// pollution the plan doc's "The weekly digest" says this feed must
	// exclude.
	updatedRows, err := s.db.Query(`
		SELECT id, title, content_updated_at FROM stars
		WHERE disabled = 0 AND content_updated_at >= datetime('now', '-7 days') AND created_at < datetime('now', '-7 days')`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: updated stars: %w", err)
	}
	for updatedRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "updated"
		if err := updatedRows.Scan(&it.StarID, &it.Title, &it.Timestamp); err != nil {
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
		SELECT sa.id, sa.title, sb.title, e.created_at FROM star_edges e
		JOIN stars sa ON sa.id = e.star_a_id
		JOIN stars sb ON sb.id = e.star_b_id
		WHERE e.created_at >= datetime('now', '-7 days') AND sa.disabled = 0 AND sb.disabled = 0`)
	if err != nil {
		return nil, fmt.Errorf("constellation week feed: links: %w", err)
	}
	for linkRows.Next() {
		var it ConstellationWeekItem
		it.Kind = "linked"
		if err := linkRows.Scan(&it.StarID, &it.Title, &it.Detail, &it.Timestamp); err != nil {
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
//   - source = 'pulsar' is excluded outright, same as ListThreads/
//     SearchMessages — a pulsar routine's own pulse history isn't a
//     conversation Weaver should mine for stars: it's Constellation's own
//     downstream content-adjacent surface talking to itself, and letting a
//     pulse thread back in as a shooting-star candidate would eventually
//     feed Weaver's output back into Weaver.
//
// Joins through root the same way SearchMessages does, and for the same
// reason (see that function's doc comment) — an edited/regenerated
// thread's real, current content lives in a hidden variant (fork_root_id
// set), not the root's own messages rows, but a variant's own id is never
// independently addressable (GetThread can't open it, its title is always
// "" — ForkThread never sets one) and isn't stable across further edits.
// Returning t.id here instead of root.id, as this used to, is exactly what
// made every star pulled from an edited thread link back to an id with no
// real title: it's a hidden implementation detail, not the conversation a
// person actually has open in their sidebar. root.id is what
// star_sources/shooting_star_runs should always track; t (whichever of
// root or its currently-active variant EffectiveThreadID(root) would
// resolve to) is only consulted here for its live message content/timing.
func (s *Store) EligibleConstellationThreads(pollIntervalMinutes int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT root.id
		FROM threads t
		JOIN threads root ON root.id = COALESCE(NULLIF(t.fork_root_id, ''), t.id)
		WHERE root.disabled = 0
		  AND root.source != 'pulsar'
		  AND (root.active_variant_id = t.id OR (root.active_variant_id = '' AND t.id = root.id))
		  AND (SELECT MAX(m.created_at) FROM messages m WHERE m.thread_id = t.id) <= datetime('now', '-' || ? || ' minutes')
		  AND (
		    (SELECT r.needs_retry FROM shooting_star_runs r
		      WHERE r.thread_id = root.id ORDER BY r.id DESC LIMIT 1) = 1
		    OR NOT EXISTS (SELECT 1 FROM shooting_star_runs r2 WHERE r2.thread_id = root.id)
		    OR (SELECT MAX(m2.id) FROM messages m2 WHERE m2.thread_id = t.id) > (
		      SELECT r3.last_message_id_seen FROM shooting_star_runs r3
		      WHERE r3.thread_id = root.id ORDER BY r3.id DESC LIMIT 1
		    )
		  )
		ORDER BY root.id`,
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
