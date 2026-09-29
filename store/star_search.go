package store

import (
	"fmt"
	"strings"
)

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
		`SELECT `+starColumnsPrefixed("s")+`
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

	results, err := scanStars(rows)
	if err != nil {
		return nil, fmt.Errorf("search library stars: %w", err)
	}
	if results == nil {
		results = []Star{}
	}
	return results, nil
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
