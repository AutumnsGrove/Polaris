package store

import (
	"database/sql"
)

// CompactThread records a fresh summary of everything up to throughID —
// history built for the LLM from here on substitutes this summary for
// every message at or below throughID, instead of the full raw text.
// Deliberately does NOT touch the messages table: the visible transcript
// stays the complete, true record, only what's sent back to the model
// shrinks — apart from cost: the summarization call's own cost is added to
// the thread's running total like any other LLM call, and to the
// throughID message's own cost_usd too, so GetStats (which sums messages)
// sees it — same both-ledgers rule as AddTurnCost.
//
// It also arms the one-shot pending notice (see the threads schema comment
// on compacted_pending_notice) so the thread's next turn can announce this
// compaction and add its cost to the live session total. Both updates are
// in this same transaction on purpose: a reader that saw the notice armed
// but the summary not yet written would announce the previous summary
// while charging the new cost.
func (s *Store) CompactThread(threadID, summary string, throughID int64, cost float64, contextTokensEstimate int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE threads SET compacted_summary = ?, compacted_through_id = ?, cost_usd = cost_usd + ?,
		 context_tokens = ?, compacted_pending_notice = 1,
		 compacted_pending_cost = compacted_pending_cost + ?,
		 updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		summary, throughID, cost, contextTokensEstimate, cost, threadID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET cost_usd = cost_usd + ? WHERE id = ?`, cost, throughID); err != nil {
		return err
	}
	return tx.Commit()
}

// TakeCompactionNotice consumes the pending-compaction marker armed by
// CompactThread, returning the current summary and the cost of the
// compaction(s) that armed it. ok is false when nothing is pending, which
// is the overwhelmingly common case — a turn on a thread that hasn't just
// been compacted.
//
// Read-and-clear in one transaction, rather than a SELECT the caller
// follows with a Clear: announcing the same compaction twice would
// double-count its cost in the frontend's running session total, and two
// turns racing on the same thread (a pulse firing while the user types,
// say) is exactly the shape that would produce it. The UPDATE's own
// `WHERE compacted_pending_notice = 1` is what makes the take exclusive —
// only one of two concurrent callers can be the one that flips it back.
func (s *Store) TakeCompactionNotice(threadID string) (summary string, cost float64, ok bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, false, err
	}
	defer tx.Rollback()

	var pending int
	if err := tx.QueryRow(
		`SELECT compacted_summary, compacted_pending_cost, compacted_pending_notice FROM threads WHERE id = ?`,
		threadID,
	).Scan(&summary, &cost, &pending); err != nil {
		if err == sql.ErrNoRows {
			// A thread row that's gone (deleted mid-turn) has no notice to
			// give, and isn't an error the caller should surface to the user.
			return "", 0, false, nil
		}
		return "", 0, false, err
	}
	if pending == 0 {
		return "", 0, false, nil
	}

	res, err := tx.Exec(
		`UPDATE threads SET compacted_pending_notice = 0, compacted_pending_cost = 0
		 WHERE id = ? AND compacted_pending_notice = 1`, threadID,
	)
	if err != nil {
		return "", 0, false, err
	}
	// Defensive, not load-bearing: Open's SetMaxOpenConns(1) already means no
	// other goroutine can run a statement between the SELECT above and this
	// UPDATE, since this transaction holds the pool's only connection for
	// both. This only fires if that serialization ever stops holding (a
	// second connection, a second process) — and if it does, dropping the
	// notice here is what keeps the compaction cost from being announced and
	// charged twice, which is strictly better than the alternative.
	if n, err := res.RowsAffected(); err != nil {
		return "", 0, false, err
	} else if n == 0 {
		if err := tx.Commit(); err != nil {
			return "", 0, false, err
		}
		return "", 0, false, nil
	}
	if err := tx.Commit(); err != nil {
		return "", 0, false, err
	}
	return summary, cost, true, nil
}
