package store

import (
	"fmt"
	"time"
)

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
		`SELECT `+starColumnsPrefixed("s")+`
		 FROM stars s
		 JOIN star_sources src ON src.star_id = s.id
		 WHERE src.thread_id = ?
		 ORDER BY s.updated_at DESC`, threadID,
	)
	if err != nil {
		return nil, fmt.Errorf("stars by thread: %w", err)
	}
	defer rows.Close()

	stars, err := scanStars(rows)
	if err != nil {
		return nil, fmt.Errorf("stars by thread: %w", err)
	}
	return stars, nil
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
