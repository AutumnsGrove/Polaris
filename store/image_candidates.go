package store

// Persistence for image_search's per-thread candidate pool
// (tools.Context.ImageCandidates, issue #124). Cards travel as opaque JSON
// strings, the same way messages.cards does, so store doesn't import tools.

// LoadImageCandidates returns a thread's pooled candidates as JSON, indexed
// so that element i is the candidate numbered i+1. A gap (a number that was
// never saved) comes back as "" — the caller skips it — so numbers the
// model was already told never shift.
func (s *Store) LoadImageCandidates(threadID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT num, card FROM image_candidates WHERE thread_id = ? ORDER BY num`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var num int
		var card string
		if err := rows.Scan(&num, &card); err != nil {
			return nil, err
		}
		if num < 1 {
			continue
		}
		for len(out) < num {
			out = append(out, "")
		}
		out[num-1] = card
	}
	return out, rows.Err()
}

// SaveImageCandidates upserts the pool: element i is stored as number i+1,
// empty entries skipped. Upsert rather than replace so a turn that only
// appended never rewrites (or, if it crashed midway, loses) earlier numbers.
func (s *Store) SaveImageCandidates(threadID string, cards []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, card := range cards {
		if card == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO image_candidates (thread_id, num, card) VALUES (?, ?, ?)
			 ON CONFLICT(thread_id, num) DO UPDATE SET card = excluded.card`,
			threadID, i+1, card,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
