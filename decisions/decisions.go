// Package decisions is a small client for OpenRouter's decision models: models
// that never generate text, reached through a dedicated endpoint rather than
// the normal /chat/completions path. You send a `state` (source text, or a
// structured array of {source, text} for a multi-source comparison) plus a set
// of typed questions, evaluated in parallel and in isolation against that same
// state, and get back typed answers with calibrated probabilities. Request/
// response shapes here are confirmed against the real API (see
// docs/plans/source-verification.md's "Live spike results"), not just read from
// docs — in particular, a Choice question's request shape is `instructions` +
// `criteria` (criteria's keys ARE the option set); an earlier informal
// `question`/`options` shape 400s.
//
// Polaris runs TypeSafe AI's Jev here (see DefaultModel), but nothing in this
// package is Jev-specific: the same request/response shape serves every decision
// model in OpenRouter's catalog, confirmed live 2026-10 against Jev 1.13/1.14,
// Inception, Microsoft, Cloudflare, Perplexity, OpenAI and Liquid — 22/22
// answers each, identical fields. Every request goes to the Decisions API
// (POST {host}/api/alpha/decisions). Jev was first reached through a Jev-only
// beta endpoint (POST {base}/systemone) that this client used until 2026-10;
// the Decisions API is the superset, and a live A/B on the pinned model showed
// the same answers and latency (p50 ~0.8s on both from a laptop, no failures in
// 30 calls). WithModel selects any catalog id, which is how the Oracle model
// benchmark (docs/plans/oracle-benchmark.md) compares candidates;
// ListDecisionModels reads the live roster.
package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultModel is the default OpenRouter decision-model id, pinned to Jev 1.14
// (2026-10-15) — the one place "Jev" legitimately names the model in code.
// Production used to send the "jev-latest" alias, which silently moves to
// whatever Typesafe releases next; Oracle fires on every turn and nobody watches
// its answers, so an unannounced model swap would be invisible. The Oracle
// benchmark (docs/plans/oracle-benchmark.md) measured 1.14 and 1.13 as a tie at
// ~300ms p50, with 1.14 marginally ahead, so this freezes what the alias already
// resolved to rather than changing behavior.
//
// "typesafe/jev-1.14" follows 1.14 patch builds (currently
// typesafe/jev-1.14-20261014, the build that was benchmarked). Pinning the
// dated id instead is also accepted and fully reproducible, but a retired
// snapshot would 400 and Oracle would quietly go off, so the version-level id
// is the safer pin. The Decisions API takes vendor-prefixed catalog ids like
// this one (the old Jev-only endpoint also accepted bare "jev-1.14" and 400'd on
// "typesafe/jev-latest"/bare "jev" as "does not exist") — see WithModel. To move
// to a new release, re-run `polaris oracle-bench` first.
const DefaultModel = "typesafe/jev-1.14"

// Client is nil-safe like brave.Client/parallel.Client/tavily.Client — see
// NewClient. Its setters are nil-safe too, so a chain started on an
// unconfigured (nil) client stays nil instead of panicking.
type Client struct {
	baseURL string // OpenRouter's API base, e.g. https://openrouter.ai/api/v1 — only its scheme and host are used, see decisionsURL
	apiKey  string
	http    *http.Client
	model   string
	// maxQuestions caps how many questions go in one request; 0 means no cap.
	// See WithMaxQuestions.
	maxQuestions int
}

// NewClient returns nil if apiKey is empty, mirroring brave.NewClient's
// nil-means-unconfigured convention — reuses the exact same OpenRouter key
// that already authenticates /chat/completions, no separate secret.
func NewClient(baseURL, apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}, model: DefaultModel}
}

// WithModel overrides the model id this client sends. Nil-safe: a chain off an
// unconfigured client stays nil. An empty id keeps the default.
func (c *Client) WithModel(id string) *Client {
	if c == nil || id == "" {
		return c
	}
	c.model = id
	return c
}

// WithMaxQuestions makes AskChoice split a call with more than n questions into
// the fewest balanced requests that each fit, sent concurrently and merged back
// into one Response. Some decision models cap a single request (Nace's Drex 2.0
// and Max reject anything over 16 with a 422, measured 2026-10-25); Oracle asks
// up to 23. A non-positive n keeps the default of no cap. Nil-safe.
//
// A split is not perfectly lossless: the same question's probability can move a
// little depending on which other questions share its request (measured on Drex
// Max: usually under 0.01, once 0.08), and the split is deterministic (sorted
// keys) so repeated calls at least agree with each other. Any result gathered
// through a split should be reported as such.
func (c *Client) WithMaxQuestions(n int) *Client {
	if c == nil || n <= 0 {
		return c
	}
	c.maxQuestions = n
	return c
}

// WithTimeout overrides the per-request HTTP timeout. The 20s default is a
// ceiling for a turn-blocking call; a caller with a tighter budget (Oracle
// bounds its own call to 2.5s) or a benchmark measuring slow candidates sets
// its own. Nil-safe.
func (c *Client) WithTimeout(d time.Duration) *Client {
	if c == nil || d <= 0 {
		return c
	}
	c.http.Timeout = d
	return c
}

// endpoint is the full URL requests go to.
func (c *Client) endpoint() string {
	return decisionsURL(c.baseURL)
}

// decisionsURL derives {scheme}://{host}/api/alpha/decisions from the
// configured API base (https://openrouter.ai/api/v1) — the Decisions API
// lives at a fixed path on the host, not under the versioned API base, so a
// base path like /api/v1 is dropped rather than appended to. Appending is the
// fallback for a base that doesn't parse as a URL (tests, a proxy), so the
// endpoint never comes back empty.
func decisionsURL(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return strings.TrimRight(baseURL, "/") + "/api/alpha/decisions"
	}
	return u.Scheme + "://" + u.Host + "/api/alpha/decisions"
}

// ChoiceQuestion is one Choice-type question — Criteria's keys are the
// option set itself, each mapped to a short description of when that option
// applies.
type ChoiceQuestion struct {
	Instructions string
	Criteria     map[string]string
}

type questionWire struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type requestBody struct {
	Model     string                  `json:"model"`
	State     interface{}             `json:"state"`
	Questions map[string]questionWire `json:"questions"`
}

// SourceState is one entry of a multi-source `state` array — confirmed live
// to keep each source's content cleanly separated, with no bleeding between
// them, unlike delimiter-concatenating multiple texts into one string.
type SourceState struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// ChoiceAnswer is one question's result.
type ChoiceAnswer struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Usage.CostUSD is the real, exact dollar cost of this call, straight off
// OpenRouter's own response — no estimation needed, unlike token-based LLM
// cost math elsewhere in this codebase.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost"`
}

// Response is one call's full result.
type Response struct {
	Answers map[string]ChoiceAnswer `json:"answers"`
	Usage   Usage                   `json:"usage"`
	// Model is the build that actually answered. The Decisions API resolves an
	// alias like ~typesafe/jev-latest to a dated snapshot and reports it here;
	// the Oracle benchmark records it, since a result for an alias is
	// uninterpretable once the alias moves. Empty if the server omits it.
	Model string `json:"model,omitempty"`
}

type errorBody struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// AskChoice sends one or more Choice questions against a shared state — a
// plain string for single-source evidence, or []SourceState for a
// multi-source comparison. Every question in the map is evaluated in
// parallel and in isolation against that same state, so fanning out several
// questions in one call is the normal, cheap way to use this (confirmed
// live: 8 questions in one call, 419ms; 3 sources / 3 pairwise questions,
// 652ms) rather than one call per question.
func (c *Client) AskChoice(ctx context.Context, state interface{}, questions map[string]ChoiceQuestion) (*Response, error) {
	if c.maxQuestions <= 0 || len(questions) <= c.maxQuestions {
		return c.askChunk(ctx, state, questions)
	}
	chunks := splitQuestions(questions, c.maxQuestions)

	// One failed chunk fails the whole call, matching an unsplit call's
	// all-or-nothing behavior (Oracle treats any error as "off for this turn"),
	// and cancels its siblings rather than paying for answers nobody will use.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resps := make([]*Response, len(chunks))
	// firstErr is the error that actually caused the failure. It is recorded
	// before the cancel, because the siblings that cancel then aborts all
	// return "context canceled": returning the first error by chunk order
	// instead let that echo mask the real cause (the Drex 2.0 run logged 102
	// "context canceled" calls whose real error was an upstream 429).
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for i, chunk := range chunks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			resps[i], err = c.askChunk(ctx, state, chunk)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	merged := &Response{Answers: make(map[string]ChoiceAnswer, len(questions))}
	for _, resp := range resps {
		for k, a := range resp.Answers {
			merged.Answers[k] = a
		}
		merged.Usage.InputTokens += resp.Usage.InputTokens
		merged.Usage.OutputTokens += resp.Usage.OutputTokens
		merged.Usage.CostUSD += resp.Usage.CostUSD
		if merged.Model == "" {
			merged.Model = resp.Model
		}
	}
	return merged, nil
}

// splitQuestions cuts questions into the fewest chunks of at most max, as even
// as possible (23 at max 16 is 12+11, not 16+7, so neither request is much
// slower than the other and the call waits on the slower one). Keys are sorted
// first so the same question set always splits the same way; map order would
// otherwise change which questions share a request from call to call.
func splitQuestions(questions map[string]ChoiceQuestion, max int) []map[string]ChoiceQuestion {
	keys := make([]string, 0, len(questions))
	for k := range questions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := (len(keys) + max - 1) / max
	per := (len(keys) + n - 1) / n
	chunks := make([]map[string]ChoiceQuestion, 0, n)
	for start := 0; start < len(keys); start += per {
		end := min(start+per, len(keys))
		chunk := make(map[string]ChoiceQuestion, end-start)
		for _, k := range keys[start:end] {
			chunk[k] = questions[k]
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

// askChunk is one real request.
func (c *Client) askChunk(ctx context.Context, state interface{}, questions map[string]ChoiceQuestion) (*Response, error) {
	wire := make(map[string]questionWire, len(questions))
	for k, q := range questions {
		wire[k] = questionWire{Type: "choice", Instructions: q.Instructions, Criteria: q.Criteria}
	}
	body, err := json.Marshal(requestBody{Model: c.model, State: state, Questions: wire})
	if err != nil {
		return nil, fmt.Errorf("encoding decisions request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating decisions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling decisions API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading decisions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var e errorBody
		if json.Unmarshal(respBody, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("decisions error (status %d): %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("decisions error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var out Response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parsing decisions response: %w", err)
	}
	return &out, nil
}

// DecisionModel is one entry from OpenRouter's live decision-model catalog —
// every model whose architecture.output_modalities contains "decisions" and so
// accepts the Decisions API. Only the fields the Oracle benchmark needs are
// kept; Pricing values are OpenRouter's decimal strings of dollars per token.
type DecisionModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// ListDecisionModels reads the live decision-model catalog
// (GET {baseURL}/models?output_modalities=decisions) — the roster probe for the
// Oracle benchmark, so candidates are never hardcoded while OpenRouter's lineup
// moves (it went from 19 to 23 entries in a week during planning). apiKey may
// be empty (the catalog is public). A non-200 or unparseable body comes back as
// an error, not an empty slice, so a caller can't mistake an outage for "no
// candidates".
func ListDecisionModels(ctx context.Context, baseURL, apiKey string) ([]DecisionModel, error) {
	u := strings.TrimRight(baseURL, "/") + "/models?output_modalities=decisions"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating decision-model catalog request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing decision models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading decision-model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decision-model catalog error (status %d): %s", resp.StatusCode, string(body))
	}
	var out struct {
		Data []DecisionModel `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parsing decision-model catalog: %w", err)
	}
	return out.Data, nil
}
