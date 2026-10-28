// Embeddings admin endpoints — the server side of `polaris embeddings`,
// plus the read `polaris embeddings status` uses. Same thin-client split as
// the rest of the Docker-only CLI surface: the database lives inside the
// container's volume, so a host-side binary can never touch it directly
// (see cmd/docker_client.go).
package gateway

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"polaris/store"
)

// embeddingBackfillRunning is the single-flight guard for backfill/reindex.
// A second concurrent run gets 409 rather than doubling the provider spend
// and racing writes on the same rows — same shape, and same reasoning, as
// store.ErrBackfillAlreadyRunning for Constellation.
//
// It's also part of handleServerBusy's signal, for the reason recorded
// there: a --force-recreate during an update kills the in-flight goroutine
// outright rather than draining it, so a backfill that got cut off mid-way
// would leave the operator staring at a coverage number that silently
// stopped moving.
var embeddingBackfillRunning atomic.Bool

// embeddingBackfillTimeout bounds one CLI-triggered run. Generous: measured
// at 80 seconds for the real 2,344-message history against a hosted model,
// and a local Ollama on an SBC can be an order of magnitude slower per
// message. The per-request provider timeouts still bound any single call.
const embeddingBackfillTimeout = 30 * time.Minute

// EmbeddingBackfillResponse is POST /api/embeddings/{backfill,reindex}'s
// body — real counts and the provider's reported spend, never estimates.
type EmbeddingBackfillResponse struct {
	Messages int     `json:"messages"`
	Stars    int     `json:"stars"`
	CostUSD  float64 `json:"cost_usd"`
	Kind     string  `json:"kind"`
	Reindex  bool    `json:"reindex"`
}

// handleEmbeddingsBackfill embeds every eligible row that isn't indexed yet
// — the initial load for an install with existing history, and the resume
// path after an interruption.
func (s *Server) handleEmbeddingsBackfill(w http.ResponseWriter, r *http.Request) {
	s.runEmbeddingBackfill(w, r, false)
}

// handleEmbeddingsReindex drops every stored vector and rebuilds from
// scratch. Deliberately a separate verb rather than a --force flag: it
// deletes data, and that should be legible in the command that does it.
func (s *Server) handleEmbeddingsReindex(w http.ResponseWriter, r *http.Request) {
	s.runEmbeddingBackfill(w, r, true)
}

func (s *Server) runEmbeddingBackfill(w http.ResponseWriter, r *http.Request, reindex bool) {
	if s.embeds == nil {
		// 409 rather than 404: the endpoint exists, the deployment just
		// hasn't configured an embeddings backend. The CLI turns this into
		// "set embeddings.provider in config.yaml" rather than a mystery.
		http.Error(w, "embeddings are not configured", http.StatusConflict)
		return
	}
	kind, ok := parseEmbeddingKind(r.URL.Query().Get("kind"))
	if !ok {
		http.Error(w, "kind must be message, star, or all", http.StatusBadRequest)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		limit = n
	}

	if !embeddingBackfillRunning.CompareAndSwap(false, true) {
		http.Error(w, "an embedding backfill is already running", http.StatusConflict)
		return
	}
	defer embeddingBackfillRunning.Store(false)

	if reindex {
		if err := s.db.DeleteAllEmbeddings(); err != nil {
			log.Warn("embeddings: clearing the index failed", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.embeds.Reload(); err != nil {
			log.Warn("embeddings: reload after reindex clear failed", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// The request context is deliberately NOT used: this is a CLI-driven job
	// whose timeout is embeddingBackfillTimeout, and a client that hangs up
	// mid-run should not abandon a half-written index (the run is resumable
	// either way, but finishing is cheaper than restarting).
	ctx, cancel := context.WithTimeout(context.Background(), embeddingBackfillTimeout)
	defer cancel()

	progress, err := s.embeds.Backfill(ctx, kind, limit, func(p IndexProgress) {
		log.Info("embeddings: backfill progress", "messages", p.Messages, "stars", p.Stars, "cost_usd", p.CostUSD)
	})
	if err != nil {
		log.Warn("embeddings: backfill failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, EmbeddingBackfillResponse{
		Messages: progress.Messages,
		Stars:    progress.Stars,
		CostUSD:  progress.CostUSD,
		Kind:     kind,
		Reindex:  reindex,
	})
}

// parseEmbeddingKind normalizes the ?kind query value. "" and "all" mean
// both kinds, matching the CLI's default.
func parseEmbeddingKind(v string) (string, bool) {
	switch v {
	case "", "all":
		return "all", true
	case store.EmbeddingKindMessage:
		return store.EmbeddingKindMessage, true
	case store.EmbeddingKindStar:
		return store.EmbeddingKindStar, true
	default:
		return "", false
	}
}

// EmbeddingStatusResponse is GET /api/embeddings/status's body — what
// `polaris embeddings status` prints, and the same numbers the settings
// panel shows.
type EmbeddingStatusResponse struct {
	Configured bool                         `json:"configured"`
	Model      string                       `json:"model,omitempty"`
	Index      map[string]EmbeddingCoverage `json:"index,omitempty"`
	CostUSD    float64                      `json:"cost_usd"`
}

// EmbeddingCoverage mirrors store.EmbeddingCoverage for the wire.
type EmbeddingCoverage struct {
	Indexed    int `json:"indexed"`
	Eligible   int `json:"eligible"`
	StaleModel int `json:"stale_model"`
}

func (s *Server) handleEmbeddingsStatus(w http.ResponseWriter, r *http.Request) {
	if s.embeds == nil {
		writeJSON(w, EmbeddingStatusResponse{Configured: false})
		return
	}
	cost, err := s.db.AuxCostByKind(store.AuxKindEmbedding)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := EmbeddingStatusResponse{
		Configured: true,
		Model:      s.embeds.Model(),
		Index:      map[string]EmbeddingCoverage{},
		CostUSD:    cost,
	}
	for kind, cov := range s.embeds.Coverage() {
		out.Index[kind] = EmbeddingCoverage{Indexed: cov.Indexed, Eligible: cov.Eligible, StaleModel: cov.StaleModel}
	}
	writeJSON(w, out)
}
