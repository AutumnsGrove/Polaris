package store

import (
	"fmt"
	"strings"
	"time"
)

// WeaverThreadSummary is one row in the "Weaver sessions" browsable list
// (issue #94, "Talk to Weaver") — every source = 'weaver' thread, whether
// created by the background scheduler's own shooting-star runs
// (RunShootingStar, gateway/constellation_weaver.go) or manually via "Talk
// to Weaver".
type WeaverThreadSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	// IsAutomatic distinguishes a scheduled shooting-star run from a
	// manually-started session for the list's badge. There's no direct
	// foreign key to check this against: shooting_star_runs.thread_id is
	// the *analyzed* conversation's own root id, not this hidden
	// weaver-source thread's id (RunShootingStar mints weaverThreadID as
	// its own separate uuid, entirely unrelated to the run's thread_id).
	// So this keys off the one thing that reliably differs instead:
	// RunShootingStar always titles its own thread "Shooting star" or
	// "Shooting star — <analyzed thread's title>" — a prefix nothing else
	// ever produces, since a manual session's title always comes from the
	// normal generateTitle flow (gateway/turn.go) instead.
	IsAutomatic bool `json:"is_automatic"`
}

// ListWeaverThreads lists every Weaver thread newest-first — both
// manually-started sessions and the scheduler's own shooting-star runs,
// deliberately including both rather than filtering to one kind: the whole
// point of this list is a single place to review everything Weaver has
// ever done, not just what a person started themselves. fork_root_id = ”
// excludes a variant the same way ListThreads/ListThreadsPage do — a
// Weaver thread is never edited/retried today, but this guards against
// ever double-listing one if that changes.
func (s *Store) ListWeaverThreads() ([]WeaverThreadSummary, error) {
	rows, err := s.db.Query(
		`SELECT id, title, updated_at
		 FROM threads
		 WHERE source = 'weaver' AND fork_root_id = ''
		 ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing weaver threads: %w", err)
	}
	defer rows.Close()

	var out []WeaverThreadSummary
	for rows.Next() {
		var t WeaverThreadSummary
		if err := rows.Scan(&t.ID, &t.Title, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("listing weaver threads: %w", err)
		}
		t.IsAutomatic = strings.HasPrefix(t.Title, "Shooting star")
		out = append(out, t)
	}
	return out, rows.Err()
}
