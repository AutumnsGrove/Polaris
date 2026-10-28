// Package embed turns text into vectors, through a provider that is
// either this deployment's own local Ollama instance or a hosted model on
// OpenRouter.
//
// The original version of this package wrapped Ollama's /api/embeddings
// and nothing else, for one caller — agent's query-similarity stale-search
// signal (see agent/query_similarity.go). It is now the shared entry point
// for every embedding in Polaris, including the persisted semantic index
// (see docs/plans/embeddings.md), so the shape here is deliberately
// provider-shaped rather than endpoint-shaped: callers hand over text and
// get back vectors plus what they cost, and never learn which backend
// answered.
//
// Deliberately still narrow: text in, vectors out, no retry/fallback
// chain. A failed embedding should disable one signal or skip one index
// batch, never hold up a research loop or a turn.
package embed

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Default models. Both are "safe by default" rather than required config:
// pplx-embed is cheap enough that a hosted default costs a fraction of a
// cent for a full history backfill, and nomic-embed-text is what the
// existing dev machine and the potato already have pulled.
const (
	// DefaultOpenRouterModel is perplexity/pplx-embed-v1-0.6b — 1024
	// dims, 32k context, $0.004/M tokens (measured: a 1.0M-token full
	// history backfill costs $0.004). Chosen over a local model because
	// the production target is a Le Potato SBC: see
	// docs/plans/embeddings.md's probe table for the measurements behind
	// that call.
	DefaultOpenRouterModel = "perplexity/pplx-embed-v1-0.6b"

	// DefaultOllamaModel is nomic-embed-text — small (274MB), fast, and
	// already pulled on both the dev machine and the potato. Note its
	// 2048-token context: it silently truncates anything longer, which is
	// one of the measured reasons the hosted model is the default for the
	// persisted index (105 of 2344 real messages exceed it).
	DefaultOllamaModel = "nomic-embed-text"

	// DefaultOpenRouterBaseURL matches config.Config's own default.
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
)

// MaxBatch is the largest number of inputs a single provider request
// carries. Client.EmbedBatch chunks to this automatically so no caller
// can trip a provider's per-request limit; 128 was verified against
// OpenRouter with real messages at ~24k tokens per request (about 1.9s),
// well inside the model's 32k window and the API's payload limit.
const MaxBatch = 128

// singleEmbedTimeout bounds Client.Embed's one-text path. That path is the
// latency-critical one — every web_search call in a research turn waits on
// it — so it gets a hard ceiling of its own even though a provider's HTTP
// client is configured for much longer batches. A hung embedding must
// degrade to "signal unavailable this call", never stall the loop.
//
// 5s is the timeout the previous Ollama-only client applied, kept
// deliberately: phase 1 of docs/plans/embeddings.md is a behavior-preserving
// refactor, and a hosted query embedding was measured at ~0.5s against real
// history, so there's no case for loosening it.
const singleEmbedTimeout = 5 * time.Second

// Result is one provider call's vectors and what they cost.
type Result struct {
	// Vectors[i] is texts[i]'s embedding, always len(texts) entries each
	// Dims long.
	Vectors [][]float32
	// Model/Dims describe what actually produced Vectors, and callers
	// persist both alongside the vector: CosineSimilarity returns 0 on a
	// length mismatch rather than erroring, so a model swap with no
	// recorded provenance degrades to silently "nothing matches" instead
	// of a detectable mismatch.
	Model string
	Dims  int
	// CostUSD is the provider's own reported cost for this call. 0 for a
	// local provider — a real answer, not an unknown one.
	CostUSD float64
	// PromptTokens is the provider's own token count for this call, when
	// it reports one (0 when it doesn't, as local Ollama doesn't).
	PromptTokens int
}

// Provider is one embeddings backend.
type Provider interface {
	// Model is the provider's configured model identifier. This is the
	// id the caller asked for, deliberately not whatever the API echoes
	// back in its response: OpenRouter returns pplx-embed's response model
	// as "pplx-embed-v1-0.6b" while the request id is
	// "perplexity/pplx-embed-v1-0.6b", and storing the echo would make
	// every startup look like a model change.
	Model() string
	// Embed embeds texts in one request. Callers should keep len(texts) at
	// or under MaxBatch (Client.EmbedBatch does).
	Embed(ctx context.Context, texts []string) (Result, error)
	// Local reports whether this provider runs on the same machine as
	// Polaris. It exists for one decision — whether a warm-up call is worth
	// making (agent's query-similarity signal warms up a local model to hide
	// a cold-start load; a hosted provider has no such load, so warming it up
	// is a wasted API call per turn).
	Local() bool
}

// Client is a Provider plus the single-text convenience the existing call
// sites use, plus batching guarantees no caller has to remember.
//
// A nil *Client means "embeddings are unavailable", the same
// optional-dependency shape as brave/parallel/tavily: every method except
// Model is nil-safe-by-check at the call site, matching how
// tools.Context.Embed's consumers already guard.
type Client struct {
	provider Provider
}

func newClient(p Provider) *Client {
	if p == nil {
		return nil
	}
	return &Client{provider: p}
}

// Model reports the configured model id, or "" on a nil client.
func (c *Client) Model() string {
	if c == nil || c.provider == nil {
		return ""
	}
	return c.provider.Model()
}

// Local reports whether the provider runs on this machine, or false on a nil
// client (nothing to warm up when embeddings aren't configured).
func (c *Client) Local() bool {
	if c == nil || c.provider == nil {
		return false
	}
	return c.provider.Local()
}

// Embed returns one text's vector.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, _, err := c.EmbedWithCost(ctx, text)
	return vec, err
}

// EmbedWithCost is Embed plus the provider-reported spend for the call (0 for
// a local provider). Same singleEmbedTimeout ceiling as Embed. Callers on a
// turn's critical path use this so a hosted provider's per-query charge can
// be billed to the turn rather than vanishing.
func (c *Client) EmbedWithCost(ctx context.Context, text string) ([]float32, float64, error) {
	res, err := c.embedBatch(ctx, []string{text}, singleEmbedTimeout)
	if err != nil {
		return nil, 0, err
	}
	return res.Vectors[0], res.CostUSD, nil
}

// EmbedBatch embeds texts, chunking into MaxBatch-sized provider requests
// and summing each chunk's reported cost and token count.
func (c *Client) EmbedBatch(ctx context.Context, texts []string) (Result, error) {
	return c.embedBatch(ctx, texts, 0)
}

// embedBatch is the one implementation behind both entry points. timeout
// is applied only when non-zero, so the batch path is bounded by the
// caller's context (a backfill wants minutes) while the single-text path
// keeps its own hard ceiling.
func (c *Client) embedBatch(ctx context.Context, texts []string, timeout time.Duration) (Result, error) {
	if c == nil || c.provider == nil {
		return Result{}, fmt.Errorf("embeddings are not configured")
	}
	if len(texts) == 0 {
		return Result{}, fmt.Errorf("nothing to embed")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	out := Result{Model: c.provider.Model()}
	for start := 0; start < len(texts); start += MaxBatch {
		end := min(start+MaxBatch, len(texts))
		part, err := c.provider.Embed(ctx, texts[start:end])
		if err != nil {
			// Partial results are deliberately discarded rather than
			// returned alongside the error: a caller that indexes what it
			// got would silently leave a hole, and every caller here
			// either retries the whole batch later (the indexer's sweep)
			// or doesn't need it (the query path).
			return Result{}, err
		}
		if len(part.Vectors) != end-start {
			return Result{}, fmt.Errorf("embed: provider returned %d vectors for %d inputs", len(part.Vectors), end-start)
		}
		if out.Dims == 0 {
			out.Dims = part.Dims
		} else if part.Dims != out.Dims {
			return Result{}, fmt.Errorf("embed: provider returned inconsistent dimensions (%d then %d) within one call", out.Dims, part.Dims)
		}
		out.Vectors = append(out.Vectors, part.Vectors...)
		out.CostUSD += part.CostUSD
		out.PromptTokens += part.PromptTokens
	}
	return out, nil
}

// CosineSimilarity returns a and b's cosine similarity, in [-1, 1] for
// any non-zero vectors (embeddings from the same model are never the
// zero vector in practice). Panics-free on a length mismatch — returns 0
// instead, since that can only happen from a caller bug (comparing
// embeddings from two different models), not a runtime data condition
// worth surfacing as an error to a research loop that should degrade
// quietly.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
