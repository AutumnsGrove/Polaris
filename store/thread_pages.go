package store

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// RecencyPageSize is search_chats' recency-mode ("search" with no query)
// fixed page size — deliberately not the keyword path's tunable limit, see
// docs/plans/search-chats.md's "Recency mode" section: a flat, predictable
// page size is more useful for "page through my recent threads" than a
// model-guessed number.
const RecencyPageSize = 10

// ThreadSummary is one recency-mode listing entry — title/date/short
// preview, deliberately not a full thread body (see ListThreadsPage).
type ThreadSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Preview   string    `json:"preview"`
}

// encodeThreadCursor/decodeThreadCursor turn a (updated_at, id) resume
// point into an opaque page token and back. Deliberately round-trips
// through Go's time.Time (RFC3339Nano) rather than trying to preserve
// SQLite's own on-disk TEXT format verbatim — database/sql's generic
// convertAssign reformats a DATETIME column's value into RFC3339Nano the
// moment it's scanned into anything (a *time.Time destination, or even a
// *string one — confirmed live: scanning the same column into a string
// variable still came back "2026-01-01T00:03:00Z", not the
// "2026-01-01 00:03:00.000" the row actually stored), so there was never a
// "raw stored text" available to round-trip in the first place. The WHERE
// clause below wraps both sides in SQLite's own datetime() function, which
// normalizes either format to the same canonical value for comparison —
// but the cursor's timestamp must be *bound as a plain string*, not a
// time.Time: confirmed live that modernc.org/sqlite's driver hands a
// time.Time query parameter to SQLite in a form datetime() can't parse at
// all (silently returns NULL, which made every comparison against it NULL
// — i.e. false — collapsing every page-2-or-later fetch to zero rows).
// ThreadSummary.UpdatedAt.Format(time.RFC3339Nano) below is exactly the
// plain-string form that works.
func encodeThreadCursor(updatedAt time.Time, id string) string {
	return base64.URLEncoding.EncodeToString([]byte(updatedAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeThreadCursor(cursor string) (updatedAtStr, id string, err error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}
	return parts[0], parts[1], nil
}

// ListThreadsPage lists non-disabled, non-variant, non-pulsar/unopened-
// Atlas threads newest-first — the same visibility filter ListThreads
// applies — keyset-paginated by (updated_at, id) rather than OFFSET/LIMIT.
// Keyset pagination is what makes this stable across a page 2 fetch even
// if a new thread was created after page 1 was returned: it always resumes
// strictly after the last row actually handed back, not "the Nth row of
// whatever the table looks like right now" (which an OFFSET page would,
// silently reshowing or skipping a row when the underlying order shifts
// between fetches). id is included as a tiebreaker purely for determinism
// when two threads share the same updated_at to the stored precision, not
// because id itself carries any chronological meaning.
//
// Preview is each thread's most recent message content, truncated — cheap
// per-thread context for a model reasoning over "what have I been asking
// about lately" without a separate query per thread.
func (s *Store) ListThreadsPage(cursor string) (threads []ThreadSummary, nextCursor string, err error) {
	where := "disabled = 0 AND fork_root_id = '' AND ghost = 0 AND source != 'pulsar' AND source != 'weaver' AND (source != 'atlas' OR continued_in_assistant = 1)"
	args := []interface{}{}
	if cursor != "" {
		cursorUpdatedAt, cursorID, derr := decodeThreadCursor(cursor)
		if derr != nil {
			return nil, "", derr
		}
		where += " AND (datetime(updated_at) < datetime(?) OR (datetime(updated_at) = datetime(?) AND id < ?))"
		args = append(args, cursorUpdatedAt, cursorUpdatedAt, cursorID)
	}
	rows, err := s.db.Query(
		`SELECT id, title, updated_at,
			COALESCE((SELECT content FROM messages WHERE thread_id = threads.id ORDER BY id DESC LIMIT 1), '')
		 FROM threads
		 WHERE `+where+`
		 ORDER BY datetime(updated_at) DESC, id DESC LIMIT ?`,
		append(args, RecencyPageSize+1)..., // +1 to detect a next page without returning an empty one
	)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	for rows.Next() {
		var t ThreadSummary
		if err := rows.Scan(&t.ID, &t.Title, &t.UpdatedAt, &t.Preview); err != nil {
			return nil, "", err
		}
		t.Preview = truncateThreadPreview(t.Preview)
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	if len(threads) > RecencyPageSize {
		last := threads[RecencyPageSize-1]
		nextCursor = encodeThreadCursor(last.UpdatedAt, last.ID)
		threads = threads[:RecencyPageSize]
	}
	return threads, nextCursor, nil
}

const threadPreviewMaxChars = 150

func truncateThreadPreview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= threadPreviewMaxChars {
		return s
	}
	return strings.TrimSpace(s[:threadPreviewMaxChars]) + "…"
}
