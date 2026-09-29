package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StarVersion is one star_versions row — a snapshot of a star's
// content-bearing columns as they stood immediately before the merge named
// by VersionNumber overwrote them.
type StarVersion struct {
	ID            int64     `json:"id"`
	StarID        int64     `json:"star_id"`
	VersionNumber int       `json:"version_number"`
	Title         string    `json:"title"`
	Summary       string    `json:"summary"`
	Body          string    `json:"body"`
	Tags          []string  `json:"tags"`
	Confidence    string    `json:"confidence"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}

// GetStarVersions returns every snapshot for a star, most recent first —
// backs the "See version history" modal.
func (s *Store) GetStarVersions(starID int64) ([]StarVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, star_id, version_number, title, summary, body, tags, confidence, source, created_at
		 FROM star_versions WHERE star_id = ? ORDER BY version_number DESC`,
		starID,
	)
	if err != nil {
		return nil, fmt.Errorf("get star versions: %w", err)
	}
	defer rows.Close()

	var out []StarVersion
	for rows.Next() {
		var v StarVersion
		var tagsJSON string
		if err := rows.Scan(&v.ID, &v.StarID, &v.VersionNumber, &v.Title, &v.Summary, &v.Body, &tagsJSON, &v.Confidence, &v.Source, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("get star versions: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &v.Tags); err != nil {
			return nil, fmt.Errorf("get star versions: decode tags: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ErrStarVersionNotFound is returned by RevertStarToVersion when
// versionNumber doesn't match any star_versions row for that star.
var ErrStarVersionNotFound = errors.New("star version not found")

// RevertStarToVersion restores a star's content to a prior snapshot by
// replaying it through UpdateStar (source "revert") — deliberately not a
// direct UPDATE against star_versions' saved values, so a revert creates a
// new version of its own rather than rewriting history. See star_versions'
// schema comment for why this is append-only by design.
func (s *Store) RevertStarToVersion(starID int64, versionNumber int) error {
	var v StarVersion
	var tagsJSON string
	err := s.db.QueryRow(
		`SELECT title, summary, body, tags, confidence FROM star_versions WHERE star_id = ? AND version_number = ?`,
		starID, versionNumber,
	).Scan(&v.Title, &v.Summary, &v.Body, &tagsJSON, &v.Confidence)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStarVersionNotFound
	}
	if err != nil {
		return fmt.Errorf("revert star to version: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &v.Tags); err != nil {
		return fmt.Errorf("revert star to version: decode tags: %w", err)
	}
	// title/body use UpdateStar's "" == "leave as-is" contract, so a
	// snapshot whose title/body were genuinely blank at the time (never
	// true in practice — CreateStar/UpdateStar don't allow blank content —
	// but not worth a special case here) would silently no-op that field
	// rather than restoring it. Summary has no such contract (UpdateStar
	// always overwrites it unconditionally), so it needs no guard.
	return s.UpdateStar(starID, v.Title, v.Summary, v.Body, v.Tags, v.Confidence, nil, "revert")
}
