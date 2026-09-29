package store

import (
	"github.com/google/uuid"
)

// EffectiveThreadID resolves which thread's messages are actually shown
// for rootID right now — rootID's own, unless SetActiveVariant last
// pointed it at a different variant (a thread ForkThread previously
// created). Every read (GetMessages, GetThreadEvents, loadHistory) and
// every new message (a plain send, or the shared prefix an edit/retry
// branches from) goes through this first, so continuing a conversation
// after browsing to an older variant just keeps building on that variant
// — no special-casing needed anywhere else.
func (s *Store) EffectiveThreadID(rootID string) (string, error) {
	var active string
	if err := s.db.QueryRow(`SELECT active_variant_id FROM threads WHERE id = ?`, rootID).Scan(&active); err != nil {
		return "", err
	}
	if active == "" {
		return rootID, nil
	}
	return active, nil
}

// ForkThread is what makes editing/regenerating non-destructive: instead
// of deleting whatever's being replaced (the old DeleteMessagesFromAndAdd
// Message behavior), the turn about to overwrite srcID's content first
// gets a permanent home of its own. This creates that new hidden thread —
// fork_root_id=rootID, fork_at_index=atIndex — and copies srcID's first
// atIndex messages into it (the shared prefix both branches have in
// common). srcID's own messages are never touched here; the caller is
// expected to write the new content into the returned thread, and
// srcID's row stays exactly as reachable afterward (via VariantsAt) as
// it was before this ran.
//
// atIndex is a position (0-based index into the message list ordered by
// id), not a message id — it's what lets VariantsAt group multiple
// threads as "alternatives at the same spot" even though each fork's own
// copied messages get entirely new autoincrement ids.
func (s *Store) ForkThread(rootID, srcID string, atIndex int) (string, error) {
	forkID := uuid.NewString()

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var model, source string
	if err := tx.QueryRow(`SELECT model, source FROM threads WHERE id = ?`, srcID).Scan(&model, &source); err != nil {
		return "", err
	}

	// compacted_summary/compacted_through_id/context_tokens and the
	// compacted_pending_* pair are all deliberately left at their defaults
	// here, not copied — a fork is a fresh variant that rebuilds its own
	// history from the raw messages it copied, which is defensible on its
	// own (a variant may well diverge from a summary of a prefix it now
	// shares only partially). The pending-notice columns make that decision
	// load-bearing rather than incidental: the notice describes the ROOT's
	// compaction, with a through_id that means nothing in this fork's id
	// space (forked messages get entirely new autoincrement ids), so
	// copying it would surface a summary under an unrelated variant — and,
	// worse, charge its cost to a session that never triggered it. Edit/
	// retry therefore surfaces no notice, and the compaction cost is
	// announced on whichever thread actually compacts.
	if _, err := tx.Exec(
		`INSERT INTO threads (id, title, model, source, fork_root_id, fork_at_index) VALUES (?, '', ?, ?, ?, ?)`,
		forkID, model, source, rootID, atIndex,
	); err != nil {
		return "", err
	}

	if _, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, workspace_file_id, attachments, cards, chart, pending_question, tts_audio_file_id, verification, prompt_tokens, cache_read_tokens, transcript, created_at)
		 SELECT ?, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, workspace_file_id, attachments, cards, chart, pending_question, tts_audio_file_id, verification, prompt_tokens, cache_read_tokens, transcript, created_at
		 FROM messages WHERE thread_id = ? ORDER BY id ASC LIMIT ?`,
		forkID, srcID, atIndex,
	); err != nil {
		return "", err
	}

	// Events (reasoning bursts, tool calls/results) aren't attached to a
	// message row — they're their own rows in a separate table, joined
	// back to a turn only by turn_id (see events.turn_id's doc comment).
	// Copying messages alone would leave the shared prefix's own
	// reasoning/tool-call history stranded under srcID: ListEvents filters
	// by thread_id, so querying by forkID would come back empty even
	// though the messages themselves came through fine. Copying only the
	// turn_ids that actually made it into forkID (not srcID's whole
	// event history) keeps this exact to the prefix, not everything srcID
	// ever did.
	if _, err := tx.Exec(
		`INSERT INTO events (thread_id, level, source, message, data, turn_id, created_at)
		 SELECT ?, level, source, message, data, turn_id, created_at
		 FROM events
		 WHERE thread_id = ? AND turn_id != '' AND turn_id IN (
			 SELECT DISTINCT turn_id FROM messages WHERE thread_id = ? AND turn_id != ''
		 )`,
		forkID, srcID, forkID,
	); err != nil {
		return "", err
	}

	// The whole candidate pool, not just the prefix's share: the fork's
	// history still carries the numbered lists, and a superset only means a
	// few numbers the fork's history never mentions.
	if _, err := tx.Exec(
		`INSERT INTO image_candidates (thread_id, num, card)
		 SELECT ?, num, card FROM image_candidates WHERE thread_id = ?`,
		forkID, srcID,
	); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return forkID, nil
}

// SetActiveVariant points rootID at a different variant of its own
// conversation. targetID is either rootID itself (show its own original
// content) or a thread ForkThread previously returned. Swapping is O(1)
// regardless of conversation length — it never moves or copies data,
// just repoints which existing thread EffectiveThreadID resolves to.
func (s *Store) SetActiveVariant(rootID, targetID string) error {
	active := targetID
	if targetID == rootID {
		active = ""
	}
	_, err := s.db.Exec(`UPDATE threads SET active_variant_id = ? WHERE id = ?`, active, rootID)
	return err
}

// VariantsAt lists every variant available at message index atIndex for
// rootID's conversation, oldest-created first: rootID's own original
// content (if its own history reaches that far — it's the implicit
// "slot 0" that predates any forking) followed by every fork branching at
// that exact index. A single-element result means nothing's actually
// been edited/regenerated at this position, so the caller shouldn't show
// a switcher for it at all.
func (s *Store) VariantsAt(rootID string, atIndex int) ([]string, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ?`, rootID).Scan(&count); err != nil {
		return nil, err
	}

	var ids []string
	if count > atIndex {
		ids = append(ids, rootID)
	}

	rows, err := s.db.Query(
		`SELECT id FROM threads WHERE fork_root_id = ? AND fork_at_index = ? ORDER BY created_at ASC`,
		rootID, atIndex,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// VariantIndices returns every position rootID has ever been
// edited/regenerated at, so the caller can build the full variants map
// for a GetThread response with one query per position instead of
// probing every possible index.
func (s *Store) VariantIndices(rootID string) ([]int, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT fork_at_index FROM threads WHERE fork_root_id = ? ORDER BY fork_at_index`,
		rootID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indices []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		indices = append(indices, i)
	}
	return indices, rows.Err()
}
