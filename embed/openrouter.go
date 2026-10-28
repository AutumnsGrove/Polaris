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

// openRouterProvider calls OpenRouter's OpenAI-compatible embeddings
// endpoint. Verified live against perplexity/pplx-embed-v1-0.6b: 1024
// dims, 32k context, batch input up to at least 128 texts, real
// per-request cost in usage.cost, and bit-identical vectors across repeat
// calls (which is what makes content_hash a valid cache key — see
// docs/plans/embeddings.md).
//
// No retry/fallback chain, same reasoning as the package doc: a failed
// embedding disables one signal or skips one index batch, and the indexer
// sweep picks it up on the next tick.
type openRouterProvider struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenRouter returns nil if apiKey is empty — callers check for nil to
// know whether embeddings are available at all, the same
// optional-dependency pattern as brave.NewClient/parallel.NewClient.
// baseURL defaults to DefaultOpenRouterBaseURL and model to
// DefaultOpenRouterModel when empty.
func NewOpenRouter(apiKey, baseURL, model string) *Client {
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = DefaultOpenRouterBaseURL
	}
	if model == "" {
		model = DefaultOpenRouterModel
	}
	return newClient(&openRouterProvider{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		// Generous, because this client also carries backfill batches of
		// up to MaxBatch texts — measured at ~2s for 128 real messages
		// (~24k tokens). The latency-critical single-text path is bounded
		// separately by singleEmbedTimeout in Client.Embed.
		http: &http.Client{Timeout: 90 * time.Second},
	})
}

func (p *openRouterProvider) Model() string { return p.model }

// Local is false: the model runs on the provider's hardware, so there's no
// cold start to warm up — see Provider.Local.
func (p *openRouterProvider) Local() bool { return false }

// openRouterEmbedRequest is the subset of OpenRouter's request schema this
// package uses. encoding_format is pinned to "float" explicitly rather
// than left to the server default: the response type is `number[] |
// string` (base64), and a silent switch to base64 would decode as a
// confusing unmarshal error instead of a clear one.
type openRouterEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type openRouterEmbedResponse struct {
	Model string `json:"model"`
	Data  []struct {
		// Index is documented as optional, so the mapping back to inputs
		// tolerates its absence (falling back to response order) rather
		// than trusting position unconditionally — a reordered batch
		// would otherwise attach every vector to the wrong text, which is
		// silent and nearly impossible to notice in a search index.
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage *struct {
		PromptTokens int      `json:"prompt_tokens"`
		Cost         *float64 `json:"cost"`
	} `json:"usage"`
}

func (p *openRouterProvider) Embed(ctx context.Context, texts []string) (Result, error) {
	body, err := json.Marshal(openRouterEmbedRequest{
		Model:          p.model,
		Input:          texts,
		EncodingFormat: "float",
	})
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("calling openrouter embeddings: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("openrouter embeddings returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var out openRouterEmbedResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return Result{}, fmt.Errorf("decoding openrouter embeddings response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return Result{}, fmt.Errorf("openrouter embeddings returned %d vectors for %d inputs", len(out.Data), len(texts))
	}

	vectors := make([][]float32, len(texts))
	for pos, d := range out.Data {
		idx := pos
		if d.Index != nil {
			idx = *d.Index
		}
		if idx < 0 || idx >= len(texts) {
			return Result{}, fmt.Errorf("openrouter embeddings returned index %d for %d inputs", idx, len(texts))
		}
		if len(d.Embedding) == 0 {
			return Result{}, fmt.Errorf("openrouter embeddings returned an empty vector for input %d", idx)
		}
		vectors[idx] = d.Embedding
	}

	dims := len(vectors[0])
	for i, v := range vectors {
		if len(v) != dims {
			return Result{}, fmt.Errorf("openrouter embeddings returned inconsistent dimensions (input %d is %d, input 0 is %d)", i, len(v), dims)
		}
	}

	res := Result{
		Vectors: vectors,
		// The configured id, not out.Model — see Provider.Model's doc
		// comment on why storing the response echo would look like a model
		// change on every startup.
		Model: p.model,
		Dims:  dims,
	}
	if out.Usage != nil {
		res.PromptTokens = out.Usage.PromptTokens
		if out.Usage.Cost != nil {
			res.CostUSD = *out.Usage.Cost
		}
	}
	return res, nil
}
