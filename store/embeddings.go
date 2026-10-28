// embeddings.go persists the semantic index over messages and stars — one
// polymorphic table, see schema.go's `embeddings` comment for why it isn't
// a child of either table.
//
// This file owns three things and nothing else: what text an indexable row
// contributes, which rows are eligible to be indexed at all, and the
// upsert/delete/purge primitives. Retrieval lives in embedding_search.go;
// the goroutine that drives it lives in gateway/embedsvc.go.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// Embedding kinds. Stored as text rather than an integer so a row read out
// of the table is legible in a sqlite3 session during a debugging session —
// this is a table an operator will look at by hand.
const (
	EmbeddingKindMessage = "message"
	EmbeddingKindStar    = "star"
)

// EmbeddableMessage is one message awaiting an embedding: its id and the
// exact text to embed.
//
// Text is raw Content, unprefixed and untruncated. Measured rather than
// assumed — role prefixes and a 1,200-char truncation were both
// indistinguishable from raw content on 14 real paraphrase queries
// (recall@10 14/14, MRR 0.751 for raw), so the indexer adds no transform it
// would then have to explain forever (docs/plans/embeddings.md, "What text
// to embed").
type EmbeddableMessage struct {
	ID   int64
	Text string
}

// EmbeddableStar is one star awaiting an embedding, plus the hash of the
// text it was last embedded with ("" when it has never been embedded).
type EmbeddableStar struct {
	ID         int64
	Text       string
	StoredHash string
}

// embeddingHash is sha256 hex of the exact embedded text. Used to decide
// whether a star actually needs re-embedding: unlike a message (only ever
// INSERTed, never updated), a star's title/summary/body is merged and
// edited over time, and re-embedding on every write would spend real money
// to reproduce an identical vector.
func embeddingHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// starEmbeddingText is what a star contributes to the index. Deliberately
// title+summary+body, where stars_fts indexes only title+summary: the body
// is where a star's substance actually lives, and "what do you know about
// how I take my coffee" is exactly the kind of query that has no lexical
// overlap with a title like "Morning Routine".
func starEmbeddingText(title, summary, body string) string {
	return title + "\n\n" + summary + "\n\n" + body
}

// MessagesNeedingEmbedding returns eligible messages with no embedding
// under model, oldest first, after afterID (a backfill cursor).
//
// Eligibility is searchableThreadWhere — the same predicate SearchMessages
// uses — so a ghost thread, an internal (pulsar/weaver/comet_approach)
// thread, a non-continued Atlas thread, or a Field opted out of chat search
// can never reach the index in the first place. That is the first of the
// two belts protecting ghost threads: their messages are hard-deleted on
// session end, and an embeddings row keyed by (kind, ref_id) would not
// cascade with them.
func (s *Store) MessagesNeedingEmbedding(model string, afterID int64, limit int) ([]EmbeddableMessage, error) {
	if model == "" {
		return nil, errors.New("embeddings: model is required")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT m.id, m.content
		 FROM messages m`+searchableThreadJoin+`
		 LEFT JOIN embeddings e ON e.kind = 'message' AND e.ref_id = m.id AND e.model = ?
		 WHERE e.id IS NULL
		   AND m.content != ''
		   AND m.id > ?
		   AND `+searchableThreadWhere+`
		 ORDER BY m.id
		 LIMIT ?`,
		model, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("finding messages to embed: %w", err)
	}
	defer rows.Close()

	var out []EmbeddableMessage
	for rows.Next() {
		var m EmbeddableMessage
		if err := rows.Scan(&m.ID, &m.Text); err != nil {
			return nil, fmt.Errorf("finding messages to embed: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StarsNeedingEmbedding returns non-disabled stars that are either not
// embedded under model yet or whose text has changed since they were
// (StoredHash differs from the current text's hash).
//
// Rejected stars are deliberately included, matching SearchStars but not
// SearchLibraryStars: the star index serves Weaver's own dedup lead-finder
// ("was this already said no to"), and dropping rejected stars from the
// index would resurrect exactly the duplicate-star bug that made
// search_stars semantic in the first place. Each read path filters by
// status at query time.
//
// Stars are few enough (a couple hundred) that loading every one and
// comparing hashes is the right shape here, unlike the message path's
// cursor.
func (s *Store) StarsNeedingEmbedding(model string) ([]EmbeddableStar, error) {
	if model == "" {
		return nil, errors.New("embeddings: model is required")
	}
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.summary, s.body, COALESCE(e.content_hash, '')
		 FROM stars s
		 LEFT JOIN embeddings e ON e.kind = 'star' AND e.ref_id = s.id AND e.model = ?
		 WHERE s.disabled = 0
		 ORDER BY s.id`,
		model,
	)
	if err != nil {
		return nil, fmt.Errorf("finding stars to embed: %w", err)
	}
	defer rows.Close()

	var out []EmbeddableStar
	for rows.Next() {
		var (
			id                   int64
			title, summary, body string
			storedHash           string
		)
		if err := rows.Scan(&id, &title, &summary, &body, &storedHash); err != nil {
			return nil, fmt.Errorf("finding stars to embed: %w", err)
		}
		text := starEmbeddingText(title, summary, body)
		if storedHash == embeddingHash(text) {
			continue
		}
		out = append(out, EmbeddableStar{ID: id, Text: text, StoredHash: storedHash})
	}
	return out, rows.Err()
}

// UpsertEmbedding stores one vector, replacing any existing row for the
// same (kind, ref_id) — there is exactly one current embedding per row,
// tagged with the model that produced it.
//
// vec is expected to be int8-quantized already (embed.EncodeInt8); text is
// the exact text it was computed from, hashed here so callers can't supply
// a hash that disagrees with what they embedded.
func (s *Store) UpsertEmbedding(kind string, refID int64, model string, dims int, vec []byte, text string) error {
	if kind == "" || refID == 0 || model == "" || dims <= 0 || len(vec) == 0 {
		return errors.New("embeddings: kind, ref_id, model, dims and vec are all required")
	}
	if len(vec) != dims {
		return fmt.Errorf("embeddings: vec is %d bytes but dims says %d", len(vec), dims)
	}
	_, err := s.db.Exec(
		`INSERT INTO embeddings (kind, ref_id, model, dims, vec, content_hash)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (kind, ref_id) DO UPDATE SET
		   model = excluded.model,
		   dims = excluded.dims,
		   vec = excluded.vec,
		   content_hash = excluded.content_hash,
		   created_at = CURRENT_TIMESTAMP`,
		kind, refID, model, dims, vec, embeddingHash(text),
	)
	if err != nil {
		return fmt.Errorf("upserting embedding: %w", err)
	}
	s.bumpEmbeddingsEpoch()
	return nil
}

// DeleteEmbedding removes one row's vector, if any.
func (s *Store) DeleteEmbedding(kind string, refID int64) error {
	if _, err := s.db.Exec(`DELETE FROM embeddings WHERE kind = ? AND ref_id = ?`, kind, refID); err != nil {
		return err
	}
	s.bumpEmbeddingsEpoch()
	return nil
}

// EmbeddingVector is one stored vector, still quantized.
type EmbeddingVector struct {
	RefID int64
	Dims  int
	Vec   []byte
}

// LoadEmbeddings returns every stored vector for kind under model — the
// in-memory cache gateway/embedsvc.go scans. Rows under a different model
// are excluded on purpose: comparing across models is meaningless, and
// CosineSimilarity answers a length mismatch with 0 rather than an error,
// so mixing them would quietly return "nothing matches".
func (s *Store) LoadEmbeddings(kind, model string) ([]EmbeddingVector, error) {
	rows, err := s.db.Query(
		`SELECT ref_id, dims, vec FROM embeddings WHERE kind = ? AND model = ?`, kind, model,
	)
	if err != nil {
		return nil, fmt.Errorf("loading embeddings: %w", err)
	}
	defer rows.Close()

	var out []EmbeddingVector
	for rows.Next() {
		var v EmbeddingVector
		if err := rows.Scan(&v.RefID, &v.Dims, &v.Vec); err != nil {
			return nil, fmt.Errorf("loading embeddings: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// EmbeddingCoverage is how much of one kind is indexed, reported against
// the *eligible* row count rather than the raw one.
//
// That distinction is load-bearing for the settings panel: the real
// snapshot's 2,344 messages are 1,856 once eligibility is applied, so
// reporting against the raw count would show a permanent 79% that reads as
// a stuck indexer rather than a working one that is excluding rows by
// design.
type EmbeddingCoverage struct {
	Kind     string
	Indexed  int
	Eligible int
	// StaleModel is how many indexed rows belong to a *different* model —
	// the "config points at a new model, nothing has been reindexed yet"
	// signal. Non-zero means semantic retrieval is deliberately off (see
	// docs/plans/embeddings.md's model-swap decision) until a reindex runs.
	StaleModel int
}

// EmbeddingCoverage reports coverage for one kind under model.
func (s *Store) EmbeddingCoverage(kind, model string) (EmbeddingCoverage, error) {
	cov := EmbeddingCoverage{Kind: kind}

	eligibleSQL := `SELECT COUNT(*) FROM messages m` + searchableThreadJoin + ` WHERE m.content != '' AND ` + searchableThreadWhere
	if kind == EmbeddingKindStar {
		eligibleSQL = `SELECT COUNT(*) FROM stars s WHERE s.disabled = 0`
	}
	if err := s.db.QueryRow(eligibleSQL).Scan(&cov.Eligible); err != nil {
		return cov, fmt.Errorf("counting eligible %s rows: %w", kind, err)
	}

	// Indexed counts rows for eligible sources only, so a vector left
	// behind by a since-disabled thread can't make coverage read 100% while
	// a real message is missing. PurgeIneligibleEmbeddings removes those
	// anyway; counting them out here means the number is honest between
	// sweeps too.
	indexedSQL := `SELECT COUNT(*) FROM embeddings e
	               WHERE e.kind = ? AND e.model = ?
	                 AND EXISTS (SELECT 1 FROM messages m` + searchableThreadJoin + `
	                             WHERE m.id = e.ref_id AND m.content != '' AND ` + searchableThreadWhere + `)`
	if kind == EmbeddingKindStar {
		indexedSQL = `SELECT COUNT(*) FROM embeddings e
		              WHERE e.kind = ? AND e.model = ?
		                AND EXISTS (SELECT 1 FROM stars s WHERE s.id = e.ref_id AND s.disabled = 0)`
	}
	if err := s.db.QueryRow(indexedSQL, kind, model).Scan(&cov.Indexed); err != nil {
		return cov, fmt.Errorf("counting indexed %s rows: %w", kind, err)
	}

	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM embeddings WHERE kind = ? AND model != ?`, kind, model,
	).Scan(&cov.StaleModel); err != nil {
		return cov, fmt.Errorf("counting stale-model %s rows: %w", kind, err)
	}
	return cov, nil
}

// CountEmbeddings returns the total number of stored rows for kind,
// regardless of eligibility or model — the raw count. Coverage's Indexed
// is the eligible-only number the settings panel shows; this exists for
// tests and for a purge to report what it actually removed.
func (s *Store) CountEmbeddings(kind string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE kind = ?`, kind).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting embeddings: %w", err)
	}
	return n, nil
}

// SetEmbeddingDimsForTest overrides a row's recorded dims without touching
// its blob, which is the only way to produce the corrupt-row shape (a
// length/width disagreement) that Reload must refuse to load. Precedent:
// SetCachedSearchPageAgeForTest, same reasoning — the alternative is
// exporting the raw *sql.DB from this package, which is worse.
func (s *Store) SetEmbeddingDimsForTest(refID int64, dims int) error {
	_, err := s.db.Exec(`UPDATE embeddings SET dims = ? WHERE ref_id = ?`, dims, refID)
	return err
}

// PurgeIneligibleEmbeddings deletes vectors whose source row is gone or is
// no longer eligible — the general self-healing sweep, run on the indexer's
// tick.
//
// One anti-join rather than a case per exclusion: ghost, disabled, internal
// threads, Atlas visibility, variant resolution and Field opt-out are all
// already expressed by searchableThreadWhere, so a rule added there can't
// be forgotten here. This is also what makes a Field flipping *to*
// opted-out self-correct without UpdateField having to special-case it.
//
// Returns how many rows went, for logging.
func (s *Store) PurgeIneligibleEmbeddings() (int64, error) {
	var total int64

	res, err := s.db.Exec(
		`DELETE FROM embeddings
		 WHERE kind = 'message'
		   AND NOT EXISTS (
		     SELECT 1 FROM messages m` + searchableThreadJoin + `
		     WHERE m.id = embeddings.ref_id AND m.content != '' AND ` + searchableThreadWhere + `)`,
	)
	if err != nil {
		return total, fmt.Errorf("purging ineligible message embeddings: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		total += n
	}

	res, err = s.db.Exec(
		`DELETE FROM embeddings
		 WHERE kind = 'star'
		   AND NOT EXISTS (SELECT 1 FROM stars s WHERE s.id = embeddings.ref_id AND s.disabled = 0)`,
	)
	if err != nil {
		return total, fmt.Errorf("purging ineligible star embeddings: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		total += n
	}
	if total > 0 {
		s.bumpEmbeddingsEpoch()
	}
	return total, nil
}

// purgeEmbeddingsForThread removes every vector belonging to one thread's
// messages, returning how many rows went. Called inside deleteGhostThreads'
// transaction — see the comment there for why this cannot be left to the
// sweep.
//
// Returns the count rather than bumping the epoch itself: the caller must
// only signal a change once the transaction has actually committed, or a
// rollback would leave the in-memory cache needlessly reloading.
func purgeEmbeddingsForThread(tx *sql.Tx, threadIDsQuery string, args ...any) (int64, error) {
	res, err := tx.Exec(
		`DELETE FROM embeddings
		 WHERE kind = 'message'
		   AND ref_id IN (SELECT id FROM messages WHERE thread_id IN (`+threadIDsQuery+`))`,
		args...,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting ghost threads' embeddings: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
