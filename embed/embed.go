// Package embed wraps a local Ollama instance's embeddings endpoint —
// used only by agent's query-similarity stale-search signal (see
// agent/query_similarity.go), never for chat completions. Deliberately
// narrow: one method, one purpose, no retry/fallback chain, since a
// failure here should just disable that one signal for the turn, not
// hold up the actual research loop.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// defaultModel is nomic-embed-text — small (274MB), fast, and already
// pulled on both the dev machine and the potato at the time this was
// written, so it's a safe default rather than requiring every deployment
// to set config.yaml's ollama.embed_model explicitly.
const defaultModel = "nomic-embed-text"

type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient returns nil if baseURL is empty — callers check for nil to
// know whether the query-similarity signal is available at all, mirroring
// tavily.NewClient/places.NewFoursquareClient's optional-dependency
// pattern. model defaults to defaultModel when empty.
func NewClient(baseURL, model string) *Client {
	if baseURL == "" {
		return nil
	}
	if model == "" {
		model = defaultModel
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		// Local-network embedding of a short search query — should return
		// in well under a second on the hardware this runs on (a Mac or
		// the potato's own CPU, no GPU needed for a 137M-parameter model).
		// 5s is generous headroom before giving up and disabling the
		// signal for this call rather than stalling the research loop.
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns text's embedding vector via Ollama's /api/embeddings.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Prompt: text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var out embedResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decoding ollama response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned an empty embedding")
	}
	return out.Embedding, nil
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
