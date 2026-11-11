package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ollamaProvider calls a local Ollama instance's /api/embed.
//
// Deliberately /api/embed rather than the older one-text-at-a-time
// /api/embeddings this package used to call: /api/embed takes an array and
// returns an array, which is what makes a backfill on a local model even
// remotely practical. Verified against a real Ollama before switching —
// the response shape below is the one it actually returns, not the one the
// docs describe.
//
// The two endpoints are NOT byte-identical, and the difference is worth
// knowing: /api/embeddings returns the raw vector while /api/embed returns
// it L2-normalized (verified live: cos = 1.000000000 between them, with
// norms of 22.686 vs 1.000 for the same text). Under the cosine similarity
// everything here actually uses, a uniform scale factor is invisible — but
// nothing in this package may start relying on vector magnitude without
// addressing that difference first.
type ollamaProvider struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOllama returns nil if baseURL is empty — callers check for nil to
// know whether embeddings are available at all, mirroring
// NewOpenRouter/brave.NewClient. model defaults to DefaultOllamaModel when
// empty.
func NewOllama(baseURL, model string) *Client {
	if baseURL == "" {
		return nil
	}
	if model == "" {
		model = DefaultOllamaModel
	}
	return newClient(&ollamaProvider{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		// Long, because this carries backfill batches: a 128-message batch
		// on a small CPU-only model is genuinely minutes of work on an
		// SBC, not seconds. The single-text path is bounded separately by
		// singleEmbedTimeout in Client.Embed, so a hung query embedding
		// still can't stall a research loop — which is what the 5s timeout
		// this client used to carry itself was really protecting.
		http: &http.Client{Timeout: 5 * time.Minute},
	})
}

func (p *ollamaProvider) Model() string { return p.model }

// Local is true: Ollama runs beside Polaris (that's the whole point of this
// provider), so a cold-start warm-up is worth doing.
func (p *ollamaProvider) Local() bool { return true }

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (p *ollamaProvider) Embed(ctx context.Context, texts []string) (Result, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: p.model, Input: texts})
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("calling ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var out ollamaEmbedResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return Result{}, fmt.Errorf("decoding ollama response: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return Result{}, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(out.Embeddings), len(texts))
	}
	dims := len(out.Embeddings[0])
	for i, v := range out.Embeddings {
		if len(v) == 0 {
			return Result{}, fmt.Errorf("ollama returned an empty embedding for input %d", i)
		}
		if len(v) != dims {
			return Result{}, fmt.Errorf("ollama returned inconsistent dimensions (input %d is %d, input 0 is %d)", i, len(v), dims)
		}
	}

	// CostUSD stays 0: a local model costs no money, which is a real
	// answer rather than an unreported one. PromptTokens stays 0 too —
	// /api/embed doesn't report one, and inventing an estimate would put a
	// fabricated number next to the provider-reported costs it's displayed
	// beside.
	return Result{Vectors: out.Embeddings, Model: p.model, Dims: dims}, nil
}
