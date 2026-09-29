package store

import (
	"fmt"
	"sort"
	"time"
)

// EligibleConstellationThreadsForBackfill is EligibleConstellationThreads
// with the idle-timing gate bypassed (a historical thread is definitionally
// not "mid-conversation" — see the plan doc's "Backfill") and ordered
// oldest-active-first, optionally capped at limit (0 means every eligible
// thread) — the -n flag `polaris constellation backfill` exposes for
// testing against a small sample instead of a full backlog run.
//
// Oldest-first (not newest-first, as this originally shipped) because of a
// real, live-tested finding (2026-09-18): Weaver is never told a
// conversation's actual date (see weaverTaskText) — the only signal it has
// for "is this newer than what I already wrote in this star" is which
// thread it's processing right now, this run, relative to prior runs.
// weaver.system's "supersede stale specifics, keep the newest as current"
// guidance (prompts.yaml) depends entirely on that processing order
// matching real chronology, or a star accreted across many backfilled
// threads on the same subject ends up treating whichever thread happens to
// be processed *last* as current — which, under the old newest-first
// order, was actually the *oldest* real conversation. limit now samples the
// oldest backlog first rather than the most recent activity — a real
// trade for the -n testing flag's convenience, accepted deliberately for
// correctness on a full, real backfill (the actual point of this function).
func (s *Store) EligibleConstellationThreadsForBackfill(limit int) ([]string, error) {
	ids, err := s.EligibleConstellationThreads(0)
	if err != nil {
		return nil, err
	}

	type idWithTime struct {
		id string
		t  time.Time
	}
	withTimes := make([]idWithTime, 0, len(ids))
	for _, id := range ids {
		// Scanned as a string, not time.Time directly — MAX() over a
		// DATETIME column loses the driver's usual column-type affinity
		// hint, so modernc.org/sqlite hands back a plain string here even
		// though a direct column SELECT would auto-parse.
		var raw string
		if err := s.db.QueryRow(`SELECT MAX(created_at) FROM messages WHERE thread_id = ?`, id).Scan(&raw); err != nil {
			return nil, fmt.Errorf("eligible constellation threads for backfill: %w", err)
		}
		t, err := time.Parse("2006-01-02 15:04:05", raw)
		if err != nil {
			// Millisecond-precision timestamps (see RecordSearch's own
			// doc comment on this exact format split) use a different
			// layout — try that before giving up.
			t, err = time.Parse("2006-01-02 15:04:05.999999999", raw)
			if err != nil {
				return nil, fmt.Errorf("eligible constellation threads for backfill: parsing %q: %w", raw, err)
			}
		}
		withTimes = append(withTimes, idWithTime{id, t})
	}
	sort.Slice(withTimes, func(i, j int) bool { return withTimes[i].t.Before(withTimes[j].t) })
	if limit > 0 && limit < len(withTimes) {
		withTimes = withTimes[:limit]
	}

	out := make([]string, len(withTimes))
	for i, wt := range withTimes {
		out[i] = wt.id
	}
	return out, nil
}

// EligibleConstellationThreads returns the ids of every thread the
// scheduler should hand to Weaver this tick — see the plan doc's "Thread
// eligibility":
//   - disabled threads are excluded outright, full stop
//   - idle-timing gate: the thread's most recent message is at least
//     pollIntervalMinutes old — "don't grab a conversation mid-thought"
//   - retry gate: the thread's most recent run has needs_retry = 1 —
//     eligible unconditionally, regardless of the delta gate below
//   - delta gate (only checked when the retry gate doesn't already apply):
//     no prior run at all, or new messages since the last run's
//     last_message_id_seen
//   - source = 'pulsar' is excluded outright, same as ListThreads/
//     SearchMessages — a pulsar routine's own pulse history isn't a
//     conversation Weaver should mine for stars: it's Constellation's own
//     downstream content-adjacent surface talking to itself, and letting a
//     pulse thread back in as a shooting-star candidate would eventually
//     feed Weaver's output back into Weaver.
//
// Joins through root the same way SearchMessages does, and for the same
// reason (see that function's doc comment) — an edited/regenerated
// thread's real, current content lives in a hidden variant (fork_root_id
// set), not the root's own messages rows, but a variant's own id is never
// independently addressable (GetThread can't open it, its title is always
// "" — ForkThread never sets one) and isn't stable across further edits.
// Returning t.id here instead of root.id, as this used to, is exactly what
// made every star pulled from an edited thread link back to an id with no
// real title: it's a hidden implementation detail, not the conversation a
// person actually has open in their sidebar. root.id is what
// star_sources/shooting_star_runs should always track; t (whichever of
// root or its currently-active variant EffectiveThreadID(root) would
// resolve to) is only consulted here for its live message content/timing.
func (s *Store) EligibleConstellationThreads(pollIntervalMinutes int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT root.id
		FROM threads t
		JOIN threads root ON root.id = COALESCE(NULLIF(t.fork_root_id, ''), t.id)
		WHERE root.disabled = 0
		  AND root.source != 'pulsar'
		  AND root.source != 'weaver'
		  AND `+notInFieldWhere(`root.field_id`, `constellation_visible = 0`)+`
		  AND (root.active_variant_id = t.id OR (root.active_variant_id = '' AND t.id = root.id))
		  AND (SELECT MAX(m.created_at) FROM messages m WHERE m.thread_id = t.id) <= datetime('now', '-' || ? || ' minutes')
		  AND (
		    (SELECT r.needs_retry FROM shooting_star_runs r
		      WHERE r.thread_id = root.id ORDER BY r.id DESC LIMIT 1) = 1
		    OR NOT EXISTS (SELECT 1 FROM shooting_star_runs r2 WHERE r2.thread_id = root.id)
		    OR (SELECT MAX(m2.id) FROM messages m2 WHERE m2.thread_id = t.id) > (
		      SELECT r3.last_message_id_seen FROM shooting_star_runs r3
		      WHERE r3.thread_id = root.id ORDER BY r3.id DESC LIMIT 1
		    )
		  )
		ORDER BY root.created_at`, // real bug found live 2026-09-18: this was
		// "ORDER BY root.id" — root.id is a UUID string, so that sorted
		// threads effectively at random with no relationship to when they
		// happened. Barely visible on the live per-minute scheduler (usually
		// 0-1 newly-eligible threads per tick), but it directly corrupted a
		// backfill's ability to track an evolving topic's *current* state
		// across many merged update_star calls (see weaver.system's
		// "supersede stale specifics" guidance in prompts.yaml) — Weaver
		// would end a run believing whichever thread happened to sort last
		// by UUID was the most recent conversation, not the one that
		// actually was.
		pollIntervalMinutes,
	)
	if err != nil {
		return nil, fmt.Errorf("eligible constellation threads: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eligible constellation threads: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func toAnySlice(strs []string) []any {
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out
}
