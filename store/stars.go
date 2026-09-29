package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

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

// starColumns is the column list every plain star scan below selects, in
// the exact order rowScanner.scanStar expects — kept as one constant so the
// four call sites below can never drift out of sync with each other on
// which columns (or what order) they ask for.
const starColumns = `id, title, category, summary, body, tags, status, confidence, is_personal, disabled, created_at, updated_at`

// starColumnsPrefixed is starColumns qualified with a table alias, for a
// query that joins stars against another table (SearchLibraryStars,
// StarsByThread) and needs unambiguous column references.
func starColumnsPrefixed(alias string) string {
	cols := strings.Split(starColumns, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows — scanStar uses it
// so GetStar (one row) and the list/search queries below (many rows) share
// one scan implementation instead of four hand-rolled copies of the same
// scan-then-decode-tags block, matching the scanEvents helper
// store/events.go already established for the same "N callers, one row
// shape" situation.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanStar scans one row selected via starColumns into a Star, decoding the
// tags JSON column and the is_personal/disabled ints along the way.
func scanStar(row rowScanner) (Star, error) {
	var star Star
	var tagsJSON string
	var isPersonal, disabled int
	if err := row.Scan(&star.ID, &star.Title, &star.Category, &star.Summary, &star.Body, &tagsJSON, &star.Status, &star.Confidence, &isPersonal, &disabled, &star.CreatedAt, &star.UpdatedAt); err != nil {
		return Star{}, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &star.Tags); err != nil {
		return Star{}, fmt.Errorf("decode tags: %w", err)
	}
	star.IsPersonal = isPersonal != 0
	star.Disabled = disabled != 0
	return star, nil
}

// scanStars runs scanStar over every remaining row of an already-executed
// query — the caller still owns closing rows (via its own defer) — shared
// by every list/search query below.
func scanStars(rows *sql.Rows) ([]Star, error) {
	var stars []Star
	for rows.Next() {
		star, err := scanStar(rows)
		if err != nil {
			return nil, err
		}
		stars = append(stars, star)
	}
	return stars, rows.Err()
}

// GetStar returns one star by id, or ErrStarNotFound.
func (s *Store) GetStar(id int64) (*Star, error) {
	row := s.db.QueryRow(`SELECT `+starColumns+` FROM stars WHERE id = ?`, id)
	star, err := scanStar(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStarNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get star: %w", err)
	}
	return &star, nil
}

// UpdateStar merges into an existing star (rewrite to read as one coherent,
// current entry, never append — Weaver's own job, this just persists it).
// title == "" means "leave the title as-is" — both the Weaver background
// tool (tools/update_star.go) and the Edit/Refine correction sheet
// (reconcileAndSaveStar, via reconcileStarContent) treat a non-empty title
// as the exception, only supplying one when the correction actually
// changed what the star is about, or when it contradicts something the
// current title itself states (see weaver.reconcile_system's TITLE:
// instructions and update_star.yaml's title field description) —
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
//
// source records who initiated this merge ("weaver", "manual_edit",
// "refine", or "revert" — see RevertStarToVersion) into the star_versions
// snapshot this function writes before overwriting the row. The snapshot
// captures the star's content-bearing columns as they stood *before* this
// merge, so version N always means "what the star looked like going into
// update N" (see star_versions' schema comment in store.go).
func (s *Store) UpdateStar(id int64, title, summary, body string, tags []string, confidenceClass string, isPersonal *bool, source string) error {
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

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("update star: %w", err)
	}
	defer tx.Rollback()

	// Snapshot the pre-update row before it's overwritten below — must run
	// inside the same transaction as the UPDATE so a crash between the two
	// can never leave a merge applied with no matching version row, or a
	// version row logged for a merge that never actually happened.
	_, err = tx.Exec(
		`INSERT INTO star_versions (star_id, version_number, title, summary, body, tags, confidence, source)
		 SELECT id, COALESCE((SELECT MAX(version_number) FROM star_versions WHERE star_id = stars.id), 0) + 1,
		        title, summary, body, tags, confidence, ?
		 FROM stars WHERE id = ?`,
		source, id,
	)
	if err != nil {
		return fmt.Errorf("update star: snapshot version: %w", err)
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
	res, err := tx.Exec(query, args...)
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
	return tx.Commit()
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
	query := `SELECT ` + starColumns + `
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

	stars, err := scanStars(rows)
	if err != nil {
		return nil, fmt.Errorf("list stars: %w", err)
	}
	return stars, nil
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
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
