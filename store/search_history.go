package store

import "time"

type SearchHistoryEntry struct {
	ID        int64     `json:"id"`
	Query     string    `json:"query"`
	Favorite  bool      `json:"favorite"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecordSearch logs a completed Atlas search — called once per query from
// handleSearch, not per keystroke. An exact repeat of an existing query
// (case-sensitive, trimmed by the caller) bumps its updated_at instead of
// inserting a duplicate row, same recency-without-clutter idea as
// TouchUpdatedAt for threads.
func (s *Store) RecordSearch(query string) error {
	_, err := s.db.Exec(
		`INSERT INTO search_history (query) VALUES (?)
		 ON CONFLICT(query) DO UPDATE SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')`,
		query,
	)
	return err
}

// ListSearchHistory returns searches newest-first, for Atlas's sidebar —
// same shape as ListThreads: favorite/non-favorite interleaved in one
// recency order, split into sections by the frontend.
func (s *Store) ListSearchHistory(limit int) ([]SearchHistoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, query, favorite, created_at, updated_at
		 FROM search_history ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []SearchHistoryEntry
	for rows.Next() {
		var e SearchHistoryEntry
		if err := rows.Scan(&e.ID, &e.Query, &e.Favorite, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SetSearchHistoryFavorite pins/unpins a search to the sidebar's Favorites
// section — deliberately doesn't touch updated_at, same reasoning as
// SetThreadFavorite. Returns sql.ErrNoRows for an id that doesn't exist,
// same convention as SetThreadFavorite/SetThreadTitle.
func (s *Store) SetSearchHistoryFavorite(id int64, favorite bool) error {
	return execOne(s.db.Exec(`UPDATE search_history SET favorite = ? WHERE id = ?`, favorite, id))
}
