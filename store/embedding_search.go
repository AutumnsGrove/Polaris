// embedding_search.go holds the store-side half of hybrid retrieval: given
// a set of row ids a semantic scan produced, fetch them with exactly the
// same eligibility filters the keyword paths apply. The fusion itself lives
// in gateway/embedding_search.go, because it needs the query embedding and
// the in-memory vector cache.
package store

import (
	"fmt"
	"strings"
)

// idPlaceholders builds "?, ?, ?" for an IN clause of n ids.
func idPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// StarsByIDs returns the given stars as search leads, for the semantic side
// of Weaver's search_stars. Includes rejected stars on purpose — see
// SearchStars' doc comment on why that's the dedup guard — and excludes
// disabled ones, which is an unconditional "not for Weaver" signal.
//
// Returns fewer rows than ids when some are ineligible; the caller orders by
// its own fused ranking, so order here doesn't matter.
func (s *Store) StarsByIDs(ids []int64) ([]StarSearchResult, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.summary, s.status
		 FROM stars s
		 WHERE s.id IN (`+idPlaceholders(len(ids))+`) AND s.disabled = 0`, args...,
	)
	if err != nil {
		return nil, fmt.Errorf("search stars by id: %w", err)
	}
	defer rows.Close()

	results := []StarSearchResult{}
	for rows.Next() {
		var r StarSearchResult
		if err := rows.Scan(&r.ID, &r.Title, &r.Summary, &r.Status); err != nil {
			return nil, fmt.Errorf("search stars by id: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// LibraryStarsByIDs returns full star rows for the given ids, applying the
// same eligibility SearchLibraryStars does (auto/confirmed, not disabled) —
// the semantic side of the `stars` tool and the Library's search box, both
// of which render StarCards and must never surface a rejected or hidden
// star.
func (s *Store) LibraryStarsByIDs(ids []int64) ([]Star, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT `+starColumnsPrefixed("s")+`
		 FROM stars s
		 WHERE s.id IN (`+idPlaceholders(len(ids))+`) AND s.disabled = 0 AND s.status IN ('auto', 'confirmed')`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("search library stars by id: %w", err)
	}
	defer rows.Close()

	results, err := scanStars(rows)
	if err != nil {
		return nil, fmt.Errorf("search library stars by id: %w", err)
	}
	if results == nil {
		results = []Star{}
	}
	return results, nil
}

// MessagesByIDs fetches message-level search results for the semantic side
// of chat search, applying exactly the eligibility SearchMessages applies
// (searchableThreadWhere, which includes the ghost and Field opt-out rules).
//
// The snippet here is a plain prefix of the message, with no highlight
// markers: a semantic hit has no matched term to mark. That's safe for both
// consumers — search_chats strips the markers anyway, and the frontend's
// snippetSegments splits on them, so a marker-free snippet renders as one
// plain text node.
func (s *Store) MessagesByIDs(ids []int64) ([]MessageSearchResult, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT m.id, root.id, root.title, m.role, m.created_at, substr(m.content, 1, 200)
		 FROM messages m`+searchableThreadJoin+`
		 WHERE m.id IN (`+idPlaceholders(len(ids))+`)
		   AND `+searchableThreadWhere,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("fetching messages by id: %w", err)
	}
	defer rows.Close()

	results := []MessageSearchResult{}
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.MessageID, &r.ThreadID, &r.ThreadTitle, &r.Role, &r.CreatedAt, &r.Snippet); err != nil {
			return nil, fmt.Errorf("fetching messages by id: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
