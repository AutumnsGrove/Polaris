// events.go persists a structured audit trail — every tool call/result,
// turn start/finish/failure, compaction, config reload, and self-update —
// to SQLite. It exists alongside the file-based logger (see
// polaris/logger) rather than replacing it: the logger is for ops-style
// tailing/grepping, this is for "what exactly happened in thread X",
// queryable and durable even if the log directory was rotated away or
// never checked. See gateway's use of LogEvent for what gets recorded.
package store

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("store")

type Event struct {
	ID        int64     `json:"id"`
	ThreadID  string    `json:"thread_id,omitempty"` // "" for events with no single thread (startup, self-update)
	Level     string    `json:"level"`               // "info" | "warn" | "error"
	Source    string    `json:"source"`              // e.g. "turn", "tool.web_search", "compaction", "update"
	Message   string    `json:"message"`
	Data      string    `json:"data"` // JSON-encoded structured detail, "{}" if none
	CreatedAt time.Time `json:"created_at"`
}

// maxEventDataBytes caps how much a single event's JSON detail blob can
// hold — a web_read result can run past 10K characters, and this is meant
// to be a durable evidence trail, not an unbounded copy of every tool
// response. Long values (e.g. a "result" field) are truncated before
// marshaling; see truncateEventStrings.
const maxEventDataBytes = 4000

// LogEvent records one structured event. threadID empty means "not
// scoped to a single thread" (startup, self-update, a config reload
// failure). data may be nil. A failure to write is logged to the file
// logger rather than returned — callers instrument code paths that are
// often themselves error-handling, so a broken event log shouldn't ever
// mask or interrupt the thing it's trying to record.
func (s *Store) LogEvent(threadID, level, source, message string, data map[string]interface{}) {
	dataJSON := "{}"
	if len(data) > 0 {
		truncateEventStrings(data)
		if b, err := json.Marshal(data); err == nil {
			dataJSON = string(b)
		}
	}

	var tid interface{}
	if threadID != "" {
		tid = threadID
	}

	if _, err := s.db.Exec(
		`INSERT INTO events (thread_id, level, source, message, data) VALUES (?, ?, ?, ?, ?)`,
		tid, level, source, message, dataJSON,
	); err != nil {
		log.Warn("failed to persist event", "source", source, "err", err)
	}
}

// truncateEventStrings trims any string value over maxEventDataBytes in
// place — belt-and-suspenders against a single oversized tool result
// bloating the events table indefinitely.
func truncateEventStrings(data map[string]interface{}) {
	for k, v := range data {
		if s, ok := v.(string); ok && len(s) > maxEventDataBytes {
			data[k] = s[:maxEventDataBytes] + "... [truncated]"
		}
	}
}

// ListEvents returns a thread's events oldest-first, for reconstructing
// exactly what happened during it.
func (s *Store) ListEvents(threadID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(thread_id, ''), level, source, message, data, created_at
		 FROM events WHERE thread_id = ? ORDER BY id ASC LIMIT ?`,
		threadID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// ListRecentEvents returns the most recent events across every thread
// (and thread-less ones like startup/self-update), newest first — for a
// global "what's been happening" view instead of one specific thread's.
func (s *Store) ListRecentEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(thread_id, ''), level, source, message, data, created_at
		 FROM events ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ThreadID, &e.Level, &e.Source, &e.Message, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// PruneEvents deletes events older than olderThanDays — called once at
// startup, mirroring the log files' own 90-day retention (see
// logger.rotatingWriter) so this durable trail doesn't grow forever on a
// long-running install.
func (s *Store) PruneEvents(olderThanDays int) error {
	_, err := s.db.Exec(
		`DELETE FROM events WHERE created_at < datetime('now', '-' || ? || ' days')`,
		strconv.Itoa(olderThanDays),
	)
	return err
}
