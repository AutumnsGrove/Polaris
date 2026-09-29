package store

import (
	"strings"
	"time"
)

// MessageSearchResult is one matching message from SearchMessages, not a
// deduped-by-thread summary — a user hunting for "that thing I asked"
// wants to see where each hit actually landed (a thread can match more
// than once), not just which threads contain a match somewhere.
type MessageSearchResult struct {
	ThreadID    string `json:"thread_id"`
	ThreadTitle string `json:"thread_title"`
	Role        string `json:"role"`
	// Snippet wraps each matched term in \x02...\x03 (ASCII STX/ETX,
	// never legitimate message content) instead of literal HTML — the
	// frontend splits on these markers and renders highlights as real
	// text nodes, so there's no {@html} injection surface from search
	// results built out of past user/assistant text.
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"created_at"`
}

// buildFTSQuery turns free-text search-box input into an FTS5 MATCH
// expression: each whitespace-separated token is individually quoted
// (doubling any embedded quote) with a trailing * for prefix matching,
// ANDed together. Quoting is what makes this safe against a token that
// happens to contain an FTS5 operator character (AND, OR, NOT, -, (, ")
// being parsed as query syntax instead of literal text typed by the user;
// the prefix match is what makes it feel "natural" while still typing,
// rather than requiring a whole word before anything matches.
func buildFTSQuery(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " AND ")
}

// SearchMessages does a full-text search over every message's content via
// messages_fts (kept in sync by triggers — see the schema comment),
// ranked by FTS5's own bm25-based relevance.
//
// Deliberately not just ListThreads' filter copy-pasted: a message row
// can live in a hidden variant thread (fork_root_id set — see
// ForkThread's doc comment) that's currently the *effective* content of
// its root, per EffectiveThreadID/active_variant_id. Filtering out every
// fork_root_id-set thread (what ListThreads does, since it's only ever
// listing roots) would make an edited/regenerated message's own current
// content unsearchable while its now-superseded predecessor in the root
// thread stayed findable — the opposite of what "search my chats" should
// mean. The join against `root` below instead includes a message iff its
// owning thread is exactly the one EffectiveThreadID(root) would resolve
// to: the root itself when active_variant_id is unset, or that specific
// variant when it's been swapped to. Every other check (disabled, Atlas
// visibility) reads from `root`, not `t` — a forked thread's own
// disabled/source/continued_in_assistant columns are just copied
// defaults from ForkThread and never independently updated (DeleteThread
// only ever flips the root's own row), so root's values are the ones
// that are actually authoritative. ThreadID/ThreadTitle are root's too:
// a forked thread's title is always ” (ForkThread never sets one) and
// its own id isn't independently addressable by GetThread — only a root
// id is, which is what clicking a result needs to open.
func (s *Store) SearchMessages(query string, limit int) ([]MessageSearchResult, error) {
	if limit <= 0 {
		limit = 30
	}
	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return []MessageSearchResult{}, nil
	}

	rows, err := s.db.Query(
		`SELECT root.id, root.title, m.role, m.created_at,
			snippet(messages_fts, 0, char(2), char(3), '…', 12)
		 FROM messages_fts
		 JOIN messages m ON m.id = messages_fts.rowid
		 JOIN threads t ON t.id = m.thread_id
		 JOIN threads root ON root.id = COALESCE(NULLIF(t.fork_root_id, ''), t.id)
		 WHERE messages_fts MATCH ?
		   AND root.disabled = 0
		   AND root.ghost = 0
		   AND (root.active_variant_id = t.id OR (root.active_variant_id = '' AND t.id = root.id))
		   AND root.source != 'pulsar'
		   AND root.source != 'weaver'
		   AND (root.source != 'atlas' OR root.continued_in_assistant = 1)
		   AND `+notInFieldWhere(`root.field_id`, `exclude_from_chat_search = 1`)+`
		 ORDER BY rank LIMIT ?`,
		ftsQuery, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []MessageSearchResult{}
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.ThreadID, &r.ThreadTitle, &r.Role, &r.CreatedAt, &r.Snippet); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
