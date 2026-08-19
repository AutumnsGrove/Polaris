package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// CachedSearchResult mirrors search.SearchResult's fields without
// depending on the search package — store is the lower-level package
// here (search depends on store for caching, not the other way around),
// so callers convert to/from search.SearchResult at the boundary (see
// gateway/search.go's resolveVirtualPage).
type CachedSearchResult struct {
	Title     string
	URL       string
	Content   string
	Score     float64
	Thumbnail string
	Engine    string
	Engines   []string
	RankState string
	Pinned    bool
}

// CachedSearchPage is one real page (SearXNG's pageno, or Brave's
// offset+1) as last fetched live from its provider.
type CachedSearchPage struct {
	Results   []CachedSearchResult
	HasMore   bool
	Degraded  bool
	FetchedAt time.Time
}

// GetCachedSearchPage returns the most recently cached real page for
// this exact (provider, query, category, realPage, maxResults) key, or
// ok=false if nothing's cached at all — regardless of age. Freshness
// (the 24h TTL) is the caller's decision, not this method's: "no cache"
// and "cache exists but stale" call for different handling (a plain
// live fetch vs. a live fetch that falls back to serving this same
// stale row if the live attempt itself fails), so both cases return the
// same shape and let the caller decide.
func (s *Store) GetCachedSearchPage(provider, query, category string, realPage, maxResults int) (*CachedSearchPage, bool, error) {
	var cacheID int64
	page := &CachedSearchPage{}
	err := s.db.QueryRow(
		`SELECT id, has_more, degraded, fetched_at FROM search_cache
		 WHERE provider = ? AND query = ? AND category = ? AND real_page = ? AND max_results = ?`,
		provider, query, category, realPage, maxResults,
	).Scan(&cacheID, &page.HasMore, &page.Degraded, &page.FetchedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	rows, err := s.db.Query(
		`SELECT title, url, content, score, thumbnail, engine, engines, rank_state, pinned
		 FROM search_cache_results WHERE cache_id = ? ORDER BY position ASC`,
		cacheID,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var r CachedSearchResult
		var enginesJSON string
		if err := rows.Scan(&r.Title, &r.URL, &r.Content, &r.Score, &r.Thumbnail, &r.Engine, &enginesJSON, &r.RankState, &r.Pinned); err != nil {
			return nil, false, err
		}
		if enginesJSON != "" {
			_ = json.Unmarshal([]byte(enginesJSON), &r.Engines)
		}
		page.Results = append(page.Results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	return page, true, nil
}

// SaveCachedSearchPage upserts one real page's worth of results,
// replacing anything previously cached under the same key — a fresh
// live fetch is always the new source of truth once it succeeds, never
// merged with whatever was cached before.
func (s *Store) SaveCachedSearchPage(provider, query, category string, realPage, maxResults int, results []CachedSearchResult, hasMore, degraded bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO search_cache (provider, query, category, real_page, max_results, has_more, degraded, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(provider, query, category, real_page, max_results)
		 DO UPDATE SET has_more = excluded.has_more, degraded = excluded.degraded, fetched_at = CURRENT_TIMESTAMP`,
		provider, query, category, realPage, maxResults, hasMore, degraded,
	); err != nil {
		return err
	}

	var cacheID int64
	// ON CONFLICT ... DO UPDATE means the driver's LastInsertId() isn't
	// reliable here (SQLite only guarantees it on a genuine insert, not
	// the update branch) — look the row up explicitly instead of
	// trusting the Exec result.
	if err := tx.QueryRow(
		`SELECT id FROM search_cache WHERE provider = ? AND query = ? AND category = ? AND real_page = ? AND max_results = ?`,
		provider, query, category, realPage, maxResults,
	).Scan(&cacheID); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM search_cache_results WHERE cache_id = ?`, cacheID); err != nil {
		return err
	}

	for i, r := range results {
		enginesJSON, err := json.Marshal(r.Engines)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO search_cache_results (cache_id, position, title, url, content, score, thumbnail, engine, engines, rank_state, pinned)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cacheID, i, r.Title, r.URL, r.Content, r.Score, r.Thumbnail, r.Engine, string(enginesJSON), r.RankState, r.Pinned,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SetCachedSearchPageAgeForTest backdates a cached page's fetched_at —
// exported solely so other packages' tests (search.ResolveVirtualPage's
// stale-fallback behavior) can exercise TTL expiry without a real 24h
// wait, mirroring the NewClientForTest pattern parallel/brave/tavily use
// for the same reason. Not used outside tests.
func (s *Store) SetCachedSearchPageAgeForTest(provider, query, category string, realPage, maxResults int, age time.Duration) error {
	_, err := s.db.Exec(
		`UPDATE search_cache SET fetched_at = datetime('now', ?)
		 WHERE provider = ? AND query = ? AND category = ? AND real_page = ? AND max_results = ?`,
		fmt.Sprintf("-%d seconds", int(age.Seconds())), provider, query, category, realPage, maxResults,
	)
	return err
}
