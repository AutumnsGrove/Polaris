package store

import "time"

type Thread struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd"`
	// ContextTokens is exposed to the frontend for the context-usage %
	// display. CompactedSummary/CompactedThroughID are internal —
	// history-building only, never sent to the frontend.
	ContextTokens int `json:"context_tokens"`
	// PromptTokens/CacheReadTokens are the thread's all-time summed input
	// and prompt-cache-read tokens (issue #107) — not columns on threads
	// itself, filled in by handleGetThread via ThreadCacheUsage.
	PromptTokens       int    `json:"prompt_tokens"`
	CacheReadTokens    int    `json:"cache_read_tokens"`
	CompactedSummary   string `json:"-"`
	CompactedThroughID int64  `json:"-"`
	// Source is informational only (see schema comment in Open) — "web"
	// for the normal chat UI, or a caller-supplied label for threads
	// created via POST /api/ask.
	Source string `json:"source"`
	// Favorite drives the sidebar's pinned Favorites section — see the
	// schema comment in Open.
	Favorite bool `json:"favorite"`
	// FocusMode/DeepResearch/NoResearch are this thread's sticky turn
	// config, alongside Model above — see the schema comment on
	// focus_mode/no_research.
	FocusMode    string `json:"focus_mode"`
	DeepResearch bool   `json:"deep_research"`
	NoResearch   bool   `json:"no_research"`
	// UsedTransponder is set once a turn on this thread carried voice_mode
	// (a Transponder call) — see the used_transponder migration's comment.
	UsedTransponder bool `json:"used_transponder"`
	// PulsarRoutineID is set only on a pulse (source = "pulsar") — nil for
	// every other thread. The frontend uses this to show a "back to
	// routine" affordance on a pulse's thread view instead of the normal
	// sidebar-toggle-only header, per the plan doc's "Pulse detail" UI.
	PulsarRoutineID *int64 `json:"pulsar_routine_id,omitempty"`
	// FieldID is the Field this thread belongs to (see threads.field_id)
	// — nil for an ungrouped thread. The frontend reads it for the header's
	// field pill; gateway/turn.go reads it off GetThreadRaw each turn, since
	// membership can change between turns (SetThreadField).
	FieldID   *string   `json:"field_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Ghost mirrors threads.ghost — see its schema comment. Internal-only:
	// gateway/turn.go reads this off GetThreadRaw to re-derive ghost status
	// per turn, but it's never sent to the frontend.
	Ghost bool `json:"-"`
}

// CreateThread inserts a new thread. title is typically derived from the
// first user message (truncated) and can be renamed later. source tags
// where the thread came from (see schema comment above) — pass "web" for
// the normal chat UI.
func (s *Store) CreateThread(id, title, model, source string) error {
	_, err := s.db.Exec(
		`INSERT INTO threads (id, title, model, source) VALUES (?, ?, ?, ?)`,
		id, title, model, source,
	)
	return err
}

// CreateGhostThread is CreateThread's ghost-mode twin (issue #67) — same
// insert, with ghost set to 1 atomically at creation time rather than a
// second UPDATE after the fact, so there is no window where a ghost
// thread is briefly a normal, listed one. Kept as its own function
// instead of adding a `ghost bool` parameter to CreateThread so the
// existing call sites (gateway/constellation_weaver.go's Weaver threads,
// which are never ghost) don't need to change.
func (s *Store) CreateGhostThread(id, title, model, source string) error {
	_, err := s.db.Exec(
		`INSERT INTO threads (id, title, model, source, ghost) VALUES (?, ?, ?, ?, 1)`,
		id, title, model, source,
	)
	return err
}

// PromoteGhostThread clears a thread's ghost flag — the only DB-side
// effect of "promoting" a ghost session, since the row/messages/events
// already exist in full (see the ghost column's schema comment).
// Unconditional (not `AND ghost = 1`), so calling this twice — a
// double-click, a retried request — is a harmless no-op rather than a
// confusing second 404.
func (s *Store) PromoteGhostThread(id string) error {
	_, err := s.db.Exec(`UPDATE threads SET ghost = 0 WHERE id = ?`, id)
	return err
}

// DeleteThreadPermanently hard-deletes a thread iff it is still tagged
// ghost — the `ghost = 1` condition is what makes this race-free against
// a concurrent promote with no read-then-write window: either the row is
// still ghost and this deletes it (messages/events cascade via their
// existing `ON DELETE CASCADE` FKs), or it was promoted moments earlier
// and this WHERE matches nothing, leaving the now-permanent thread
// untouched. Only call this after waiting out any goroutine that might
// still be writing to this thread — see gateway/ws.go's connWG.
func (s *Store) DeleteThreadPermanently(id string) error {
	_, err := s.db.Exec(`DELETE FROM threads WHERE id = ? AND ghost = 1`, id)
	return err
}

// DeleteAllGhostThreads hard-deletes every thread still tagged ghost —
// cmd/run.go's startup sweep for the crash/force-quit case, where the
// graceful WS-disconnect cleanup (gateway/ws.go) never ran. Returns the
// deleted ids too, not just a count, so the caller can also clean up
// their code_exec workspace directories the same way ws.go's own
// disconnect cleanup does — a leftover ghost thread's workspace dir has
// no other owner to reclaim it once its row is gone.
func (s *Store) DeleteAllGhostThreads() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM threads WHERE ghost = 1`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := s.db.Exec(`DELETE FROM threads WHERE ghost = 1`); err != nil {
		return nil, err
	}
	return ids, nil
}

// SetThreadConfig writes through a thread's sticky turn config (model,
// focus mode, deep research, no-research/chat-mode) — called on every turn
// (see handleTurn) so reopening a thread later restores exactly what it was
// last configured with, and from handleUpdateThread when the composer's
// selectors are changed directly without sending a message. Deliberately
// does not touch updated_at, matching SetThreadFavorite's reasoning:
// applying a sticky config isn't "activity" on the thread and shouldn't
// reorder it in the sidebar.
func (s *Store) SetThreadConfig(id, model, focusMode string, deepResearch, noResearch bool) error {
	return execOne(s.db.Exec(
		`UPDATE threads SET model = ?, focus_mode = ?, deep_research = ?, no_research = ? WHERE id = ?`,
		model, focusMode, deepResearch, noResearch, id,
	))
}

// SetThreadTitle updates a thread's title — used both for the one-time
// LLM-generated title after a new thread's first turn finishes, and for
// a user-initiated rename from the sidebar. Either one replaces
// whatever title was there before; there's no separate "locked" flag,
// since a rename happening at all is itself the signal that the title
// is no longer just the auto-generated placeholder.
func (s *Store) SetThreadTitle(id, title string) error {
	return execOne(s.db.Exec(
		`UPDATE threads SET title = ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		title, id,
	))
}

// SetThreadFavorite pins/unpins a thread to the sidebar's Favorites
// section. Deliberately does not touch updated_at — favoriting isn't
// "activity" on a thread the way a rename or a new message is, and
// shouldn't reorder it within its section.
func (s *Store) SetThreadFavorite(id string, favorite bool) error {
	return execOne(s.db.Exec(`UPDATE threads SET favorite = ? WHERE id = ?`, favorite, id))
}

// MarkThreadContinued flips continued_in_assistant to 1 — see that
// column's schema comment. Called by handleGetThread the first time an
// "atlas"-sourced thread is actually opened in the Assistant, which is
// what makes it start showing up in ListThreads from then on. A no-op
// (not an error) once it's already 1, and safe to call on a non-"atlas"
// thread too — ListThreads never consults this column for those, so
// setting it there just does nothing observable.
func (s *Store) MarkThreadContinued(id string) error {
	_, err := s.db.Exec(`UPDATE threads SET continued_in_assistant = 1 WHERE id = ?`, id)
	return err
}

// MarkThreadUsedTransponder flips used_transponder to 1 the first time a
// turn on this thread carries voice_mode — see that column's schema
// comment. The `AND used_transponder = 0` guard just avoids a pointless
// write on every later call turn once it's already set; it's not needed
// for correctness (the value doesn't change), only for not touching the
// row on a call thread's second and later turns.
func (s *Store) MarkThreadUsedTransponder(id string) error {
	_, err := s.db.Exec(`UPDATE threads SET used_transponder = 1 WHERE id = ? AND used_transponder = 0`, id)
	return err
}

// TouchUpdatedAt bumps rootID's own updated_at to now, independent of
// whichever thread is actually being written to. Needed because AddMessage/
// CompactThread bump updated_at on storageThreadID — the effective variant a
// turn is writing into (see EffectiveThreadID), which is a hidden,
// forked thread (fork_root_id set) once anything's ever been edited or
// regenerated in rootID's conversation. ListThreads only ever returns
// root threads (fork_root_id = ”), so without this, a thread with even
// one edit/retry in its past silently stops advancing in the sidebar's
// recency order the moment that happens — every later message keeps
// bumping the hidden variant's own updated_at instead, which nothing
// user-visible ever reads. rootID is always safe to call this with even
// when it has no active variant (storageThreadID == rootID): the two
// bumps just land on the same row a moment apart, which is harmless.
func (s *Store) TouchUpdatedAt(rootID string) error {
	_, err := s.db.Exec(
		`UPDATE threads SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		rootID,
	)
	return err
}

// GetThread looks up a thread by id. A disabled (soft-deleted) thread, a
// hidden variant (fork_root_id set — see ForkThread), or a still-ghost
// thread (see the ghost schema comment) is excluded — same sql.ErrNoRows
// a caller gets for an id that never existed at all, so a stale tab/
// bookmark pointed at a deleted thread (or an unpromoted ghost session's
// id, which isn't meant to be individually addressable until promoted)
// fails the same way as a bad id. Internal callers that need to read a
// variant or ghost thread directly use GetThreadRaw instead.
func (s *Store) GetThread(id string) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, no_research, used_transponder, pulsar_routine_id, field_id, created_at, updated_at
		 FROM threads WHERE id = ? AND disabled = 0 AND fork_root_id = '' AND ghost = 0`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.NoResearch, &t.UsedTransponder, &t.PulsarRoutineID, &t.FieldID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetThreadRaw looks up any thread by id, including a hidden variant, a
// disabled one, or a still-ghost one — for internal use (loadHistory,
// ForkThread, gateway/turn.go's per-turn ghost/Weaver-status read-back)
// where the id in hand is known to be legitimate (resolved via
// EffectiveThreadID or the turn's own threadID, not taken from an
// untrusted request), not the public GetThread's job of rejecting ids
// that shouldn't be individually addressable.
func (s *Store) GetThreadRaw(id string) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, no_research, ghost, field_id, created_at, updated_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.NoResearch, &t.Ghost, &t.FieldID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListThreads returns non-disabled, non-variant threads newest-first, for
// the sidebar/history view. Favorite/non-favorite are interleaved here in
// one recency order — the frontend splits them into the pinned Favorites
// section and the rest, each keeping this same relative ordering.
//
// source = 'atlas' AND continued_in_assistant = 0 is excluded — a Quick
// Answer creates a real thread on every query (see gateway/ask.go), and
// without this, every one-off search (repeated ones most of all)
// permanently cluttered this list whether or not anyone ever actually
// followed up on it in the Assistant. See continued_in_assistant's schema
// comment for how it flips to 1.
//
// source = 'pulsar' is excluded unconditionally, with no equivalent
// "graduates into visibility" escape hatch — a pulse is only ever meant
// to be browsed via /pulsar's own routine detail view (see
// docs/plans/pulsar-routines.md's "UI structure"), never the ordinary
// chat sidebar. Confirmed live: without this, every pulse showed up in
// Recents indistinguishable from a normal chat thread.
func (s *Store) ListThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, focus_mode, deep_research, pulsar_routine_id, field_id, created_at, updated_at
		 FROM threads
		 WHERE disabled = 0 AND fork_root_id = '' AND ghost = 0 AND source != 'pulsar' AND source != 'weaver' AND (source != 'atlas' OR continued_in_assistant = 1)
		 ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.PulsarRoutineID, &t.FieldID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// DeleteThread soft-deletes a thread: it flips disabled rather than
// issuing a real DELETE, so the row and every message/event still
// referencing it survive as a durable record — ListThreads/GetThread
// simply stop returning it, which is indistinguishable from a real
// deletion to every existing API caller.
func (s *Store) DeleteThread(id string) error {
	_, err := s.db.Exec(
		`UPDATE threads SET disabled = 1, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		id,
	)
	return err
}

// AddCost bumps a thread's running cost without inserting a message row —
// for spend that isn't itself a stored turn, like a read-aloud TTS call
// against an existing assistant message.
func (s *Store) AddCost(threadID string, costUSD float64) error {
	_, err := s.db.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		costUSD, threadID,
	)
	return err
}

// SetContextTokens records the thread's current context size (prompt +
// completion tokens from the LLM's own usage numbers) — drives the
// context-usage % in the UI and the auto-compaction check.
func (s *Store) SetContextTokens(threadID string, tokens int) error {
	_, err := s.db.Exec(`UPDATE threads SET context_tokens = ? WHERE id = ?`, tokens, threadID)
	return err
}
