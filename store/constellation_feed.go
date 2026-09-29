package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

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
