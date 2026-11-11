package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"polaris/embed"
	"polaris/store"
)

// embedService owns the persisted semantic index: the in-memory vector
// cache that every semantic search scans, and the background sweep that
// keeps it in step with the database (see docs/plans/embeddings.md).
//
// Deliberately sweep-driven rather than queue-driven. The orphaned design
// sketch in search-chats.md called for an on-write enqueue, but that means
// threading a callback through every message/star write path, and for a
// single-operator app the freshness it buys is nil: a 30s tick that finds
// nothing pending costs one indexed SQL query, and the only thing a delay
// costs is that a thread written this instant isn't semantically searchable
// for another few seconds. Kick exists for the one case where that matters
// (a chat searching its own just-written turn), rather than a general
// queue.
//
// A nil *embedService is the "embeddings not configured" case and every
// method is safe to call on it, matching how *embed.Client's nil already
// disables the query-similarity signal.
type embedService struct {
	db     *store.Store
	client *embed.Client
	model  string

	// mu guards vectors. Writes take the write lock for a whole reload;
	// reads (semantic search, which runs on the tool path) take the read
	// lock and copy nothing — the slice itself is never mutated in place,
	// only swapped, so a search can't observe a half-built cache.
	mu        sync.RWMutex
	vectors   map[string][]embeddingEntry
	loaded    bool
	lastEpoch uint64

	// kick asks the indexer to run a sweep now instead of on its next tick.
	// Buffered with one slot and sent non-blocking, so N rapid kicks
	// collapse into one extra sweep rather than queueing N.
	kick chan struct{}
}

// embeddingEntry is one vector in the cache, decoded to float32 on load —
// the search path does the cosine math on it directly, so the int8 blob is
// only ever the storage form.
type embeddingEntry struct {
	refID int64
	vec   []float32
}

// newEmbedService returns nil when client is nil, so a deployment with no
// embedding provider configured behaves exactly as it did before this
// subsystem existed.
func newEmbedService(db *store.Store, client *embed.Client) *embedService {
	if client == nil || db == nil {
		return nil
	}
	return &embedService{
		db:     db,
		client: client,
		model:  client.Model(),
		kick:   make(chan struct{}, 1),
	}
}

func (v *embedService) Model() string {
	if v == nil {
		return ""
	}
	return v.model
}

// Kick requests a sweep as soon as possible. Cheap and non-blocking: safe
// to call on a nil service, and calling it in a tight loop just collapses
// into one pending sweep.
func (v *embedService) Kick() {
	if v == nil {
		return
	}
	select {
	case v.kick <- struct{}{}:
	default:
	}
}

// indexTick is how often the idle sweep runs. Small enough that a freshly
// written thread is searchable within a turn or two, large enough that an
// idle Polaris does one indexed query every 30s and nothing else.
const indexTick = 30 * time.Second

// indexBudget bounds how many rows one sweep will embed, so a large
// first-run backlog can't monopolize the scheduler tick or hold an API
// connection open indefinitely — Backfill (the CLI path) is what an
// operator uses for a real initial load.
const indexBudget = 200

// RunEmbedIndexer loads the cache and then sweeps until done is closed —
// the same blocking-goroutine shape as RunPulsarScheduler and
// RunConstellationScheduler, so cmd/run.go launches all three identically.
// Safe on a nil service (returns immediately), which is what makes this a
// no-op for a deployment with no embedding provider configured.
func (v *embedService) RunEmbedIndexer(done <-chan struct{}) {
	if v == nil {
		return
	}
	if err := v.Reload(); err != nil {
		// Not fatal: an empty cache just means semantic search returns
		// nothing until a sweep succeeds, and FTS is unaffected.
		log.Warn("embeddings: initial cache load failed", "err", err)
	}

	// The sweeps need a context that ends when done closes, so a shutdown
	// mid-backfill aborts the in-flight HTTP request instead of leaving it
	// to the provider's own timeout.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-done
		cancel()
	}()

	ticker := time.NewTicker(indexTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-v.kick:
		}
		if _, err := v.IndexOnce(ctx, indexBudget); err != nil && ctx.Err() == nil {
			log.Warn("embeddings: index sweep failed", "err", err)
		}
	}
}

// Reload rebuilds the whole cache from the database. Called at startup and
// after any sweep that wrote something — a full reload is a couple hundred
// KB of BLOBs at today's scale, and it keeps the cache trivially consistent
// with storage instead of reconciling individual upserts.
func (v *embedService) Reload() error {
	if v == nil {
		return nil
	}
	// Read the epoch *first*: a mutation landing between this read and the
	// loads below would otherwise be stamped as "already loaded", and the
	// cache would stay one change behind until the next mutation.
	epoch := v.db.EmbeddingsEpoch()

	fresh := map[string][]embeddingEntry{}
	for _, kind := range []string{store.EmbeddingKindMessage, store.EmbeddingKindStar} {
		rows, err := v.db.LoadEmbeddings(kind, v.model)
		if err != nil {
			return err
		}
		entries := make([]embeddingEntry, 0, len(rows))
		for _, r := range rows {
			vec := embed.DecodeInt8(r.Vec)
			// A row whose stored width disagrees with its own blob can only
			// be corruption; drop it rather than let CosineSimilarity's
			// length-mismatch-means-zero quietly answer "nothing matches".
			if len(vec) != r.Dims {
				log.Warn("embeddings: skipping a corrupt stored vector", "kind", kind, "ref_id", r.RefID, "dims", r.Dims, "bytes", len(r.Vec))
				continue
			}
			entries = append(entries, embeddingEntry{refID: r.RefID, vec: vec})
		}
		fresh[kind] = entries
	}

	v.mu.Lock()
	v.vectors = fresh
	v.loaded = true
	v.lastEpoch = epoch
	v.mu.Unlock()
	return nil
}

// IndexProgress reports what one sweep or backfill did.
type IndexProgress struct {
	Messages int
	Stars    int
	// CostUSD is what the provider reported for this run — real spend, not
	// an estimate (see store.RecordAuxCost's own doc on why that matters).
	CostUSD float64
}

// IndexOnce runs one bounded sweep of both kinds: embed whatever eligible
// rows are missing, then purge rows that are no longer eligible.
//
// Purge lives here rather than only at the point a rule changes because the
// rules are spread across many writes (soft delete, Field opt-out, ghost
// hard-delete, variant swap). One anti-join against the same predicate
// catches every one of them, and running it every sweep means a missed
// immediate purge self-heals within a tick.
func (v *embedService) IndexOnce(ctx context.Context, budget int) (IndexProgress, error) {
	var p IndexProgress
	if v == nil {
		return p, nil
	}
	if budget <= 0 {
		budget = indexBudget
	}

	// Recorded per sweep, not per row (the settings panel wants the total, and
	// one row per tick keeps aux_usage from growing a line per message
	// forever) — but deferred, so the messages half still lands in the ledger
	// when the stars half fails. The provider has already billed it either way.
	defer func() { v.recordSpend(p.CostUSD) }()

	msgs, msgCost, err := v.indexKind(ctx, store.EmbeddingKindMessage, budget)
	p.Messages, p.CostUSD = msgs, msgCost
	if err != nil {
		return p, err
	}
	stars, starCost, err := v.indexKind(ctx, store.EmbeddingKindStar, budget)
	p.Stars, p.CostUSD = stars, msgCost+starCost
	if err != nil {
		return p, err
	}

	// Purge runs even when nothing was embedded — it's the only thing that
	// notices an opted-out or deleted row between writes.
	if _, err := v.db.PurgeIneligibleEmbeddings(); err != nil {
		return p, err
	}

	// Reload if the table changed at all since this cache was built, not just
	// if *this* sweep wrote something. Rows also disappear through paths this
	// sweep doesn't own — UpdateField's opt-out purge and
	// DeleteThreadPermanently's ghost cleanup — and inferring freshness from
	// the sweep's own work left a deleted vector still resident and still
	// searchable. TestEmbedIndexer_PurgesAfterFieldOptOut is the regression
	// test; the first version of this code failed it.
	v.mu.RLock()
	stale := v.lastEpoch != v.db.EmbeddingsEpoch()
	v.mu.RUnlock()
	if stale {
		if err := v.Reload(); err != nil {
			return p, err
		}
	}
	return p, nil
}

// indexKind embeds everything outstanding of one kind, up to budget rows for
// messages (stars are few enough that the budget doesn't apply — embedding a
// couple hundred of them is one request), returning how many rows it wrote
// and what the provider reported costing.
//
// Split out of IndexOnce so a kind-filtered Backfill genuinely only touches
// that kind. It didn't at first: IndexOnce always did both, so
// `polaris embeddings backfill --kind message` silently embedded stars too
// and, worse, reported a message count that didn't match what was left to do.
// The live check against the real history is what caught it (1,198 of 1,598).
func (v *embedService) indexKind(ctx context.Context, kind string, budget int) (int, float64, error) {
	switch kind {
	case store.EmbeddingKindMessage:
		msgs, err := v.db.MessagesNeedingEmbedding(v.model, 0, budget)
		if err != nil || len(msgs) == 0 {
			return 0, 0, err
		}
		texts := make([]string, len(msgs))
		for i, m := range msgs {
			texts[i] = m.Text
		}
		res, err := v.client.EmbedBatch(ctx, texts)
		if err != nil {
			return 0, 0, err
		}
		for i, m := range msgs {
			if err := v.db.UpsertEmbedding(store.EmbeddingKindMessage, m.ID, v.model,
				res.Dims, embed.EncodeInt8(res.Vectors[i]), m.Text); err != nil {
				// Cost still returned: the batch was already paid for.
				return i, res.CostUSD, err
			}
		}
		return len(msgs), res.CostUSD, nil

	case store.EmbeddingKindStar:
		stars, err := v.db.StarsNeedingEmbedding(v.model)
		if err != nil || len(stars) == 0 {
			return 0, 0, err
		}
		texts := make([]string, len(stars))
		for i, s := range stars {
			texts[i] = s.Text
		}
		res, err := v.client.EmbedBatch(ctx, texts)
		if err != nil {
			return 0, 0, err
		}
		for i, s := range stars {
			if err := v.db.UpsertEmbedding(store.EmbeddingKindStar, s.ID, v.model,
				res.Dims, embed.EncodeInt8(res.Vectors[i]), s.Text); err != nil {
				return i, res.CostUSD, err
			}
		}
		return len(stars), res.CostUSD, nil

	default:
		return 0, 0, fmt.Errorf("embeddings: unknown kind %q", kind)
	}
}

// Backfill embeds every eligible row of kind, in batches, calling progress
// after each batch (may be nil). kind may be store.EmbeddingKindMessage,
// store.EmbeddingKindStar, or "" / "all" for both.
//
// Deliberately one long-running call rather than a queue of small ones: the
// caller is an operator command with a generous timeout (cmd/embeddings.go),
// and a partial backfill is resumable for free because "needs embedding" is
// derived from what's already stored.
func (v *embedService) Backfill(ctx context.Context, kind string, limit int, progress func(total IndexProgress)) (IndexProgress, error) {
	var total IndexProgress
	if v == nil {
		return total, errEmbeddingsUnavailable
	}
	// Deferred so a backfill that dies at batch 7 of 8 (timeout, provider
	// error, cancelled CLI) still books what the first six cost.
	defer func() { v.recordSpend(total.CostUSD) }()
	kinds := []string{store.EmbeddingKindMessage, store.EmbeddingKindStar}
	switch kind {
	case store.EmbeddingKindMessage:
		kinds = kinds[:1]
	case store.EmbeddingKindStar:
		kinds = kinds[1:]
	case "", "all":
	default:
		return total, fmt.Errorf("embeddings: unknown kind %q", kind)
	}

	for {
		worked := 0
		for _, k := range kinds {
			n, cost, err := v.indexKind(ctx, k, indexBudget)
			total.CostUSD += cost
			if k == store.EmbeddingKindMessage {
				total.Messages += n
			} else {
				total.Stars += n
			}
			if err != nil {
				return total, err
			}
			worked += n
			if limit > 0 && total.Messages+total.Stars >= limit {
				break
			}
		}
		if progress != nil {
			progress(total)
		}
		if worked == 0 || (limit > 0 && total.Messages+total.Stars >= limit) {
			break
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}

	// One purge + one cache refresh at the end, rather than per batch: a
	// backfill has no readers to keep coherent mid-run, and the sweep inside
	// IndexOnce would otherwise reload the whole cache on every 200 rows.
	if _, err := v.db.PurgeIneligibleEmbeddings(); err != nil {
		return total, err
	}
	if total.Messages > 0 || total.Stars > 0 {
		if err := v.Reload(); err != nil {
			return total, err
		}
	}
	return total, nil
}

// recordSpend books provider-reported embedding spend to the aux ledger.
// No-op for zero (a local Ollama reports no cost).
func (v *embedService) recordSpend(costUSD float64) {
	if costUSD <= 0 {
		return
	}
	if err := v.db.RecordAuxCost(store.AuxKindEmbedding, costUSD); err != nil {
		log.Warn("embeddings: recording spend failed", "err", err)
	}
}

var errEmbeddingsUnavailable = errors.New("embeddings are not configured")

// SimilarityHit is one semantic search result: which row, and how close.
type SimilarityHit struct {
	RefID int64
	Score float64
}

// Search scores every cached vector of kind against query, returning the
// top limit by cosine similarity.
//
// Brute force over up to ~50k vectors is the deliberate choice (see the
// plan doc): 50k × 1,024 ≈ 51M multiply-adds runs in tens of milliseconds
// in Go, which is noise next to the ~0.5s the query's own embedding call
// costs. An ANN index before that point buys nothing and costs a dependency
// plus an index-rebuild story.
func (v *embedService) Search(kind string, query []float32, limit int) []SimilarityHit {
	if v == nil || len(query) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}

	v.mu.RLock()
	entries := v.vectors[kind]
	v.mu.RUnlock()

	if len(entries) == 0 {
		return nil
	}

	hits := make([]SimilarityHit, 0, len(entries))
	for _, e := range entries {
		if len(e.vec) != len(query) {
			continue
		}
		hits = append(hits, SimilarityHit{RefID: e.refID, Score: embed.CosineSimilarity(query, e.vec)})
	}
	// Partial selection would matter at a much larger corpus; at this scale a
	// full sort is simpler than a heap and the difference is microseconds.
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// CachedCount reports how many vectors are in the cache for kind — the
// "semantic retrieval is actually available" check callers make before
// bothering to embed a query.
func (v *embedService) CachedCount(kind string) int {
	if v == nil {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.vectors[kind])
}

// Coverage reports indexed/eligible/stale-model counts for both kinds,
// backing the settings panel's index status row.
func (v *embedService) Coverage() map[string]store.EmbeddingCoverage {
	out := map[string]store.EmbeddingCoverage{}
	if v == nil {
		return out
	}
	for _, kind := range []string{store.EmbeddingKindMessage, store.EmbeddingKindStar} {
		cov, err := v.db.EmbeddingCoverage(kind, v.model)
		if err != nil {
			log.Warn("embeddings: coverage query failed", "kind", kind, "err", err)
			continue
		}
		out[kind] = cov
	}
	return out
}

// embeddingIndexStatus reports the settings panel's semantic-index row.
// Zero-valued (Configured false) when no service exists, so a deployment
// with embeddings off shows no row at all.
func (s *Server) embeddingIndexStatus() EmbeddingIndexStatus {
	if s.embeds == nil {
		return EmbeddingIndexStatus{}
	}
	out := EmbeddingIndexStatus{Configured: true, Model: s.embeds.Model()}
	cov := s.embeds.Coverage()
	for kind, dst := range map[string]*EmbeddingKindProgress{
		store.EmbeddingKindMessage: &out.Messages,
		store.EmbeddingKindStar:    &out.Stars,
	} {
		c, ok := cov[kind]
		if !ok {
			continue
		}
		dst.Indexed = c.Indexed
		dst.Eligible = c.Eligible
		dst.StaleModel = c.StaleModel
	}
	return out
}
