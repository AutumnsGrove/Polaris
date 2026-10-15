// Package jev is a small client for TypeSafe AI's Jev "System One" model,
// reached through OpenRouter's beta endpoint (POST {base}/systemone) rather
// than the normal /chat/completions path — see
// docs/plans/source-verification.md. Jev never generates text: you send a
// `state` (source text, or a structured array of {source, text} for a
// multi-source comparison) plus a set of typed questions, evaluated in
// parallel and in isolation against that same state, and get back typed
// answers with calibrated probabilities. Request/response shapes here are
// confirmed against the real API (see the plan doc's "Live spike results"),
// not just read from docs — in particular, a Choice question's request
// shape is `instructions` + `criteria` (criteria's keys ARE the option set);
// an earlier informal `question`/`options` shape 400s.
//
// The same request/response shape serves every decision model on OpenRouter
// through the Decisions API (POST {host}/api/alpha/decisions), confirmed live
// 2026-10 against Jev 1.13/1.14, Inception, Microsoft, Cloudflare, Perplexity,
// OpenAI and Liquid — 22/22 answers each, identical fields. WithDecisionsAPI
// switches a client to that surface and WithModel selects any catalog id,
// which is how the Oracle model benchmark (docs/plans/oracle-benchmark.md)
// compares candidates; ListDecisionModels reads the live roster.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Model is the default OpenRouter model id — "jev-latest", what production has
// always sent (also "jev-1.13"/"typesafe/jev-1.13" work; "typesafe/jev-latest"
// and bare "jev" both 400 "does not exist" on the System One surface). The
// Decisions API takes the vendor-prefixed catalog ids instead, e.g.
// "typesafe/jev-1.14" — see WithModel.
const Model = "jev-latest"

// Client is nil-safe like brave.Client/parallel.Client/tavily.Client — see
// NewClient. Its setters are nil-safe too, so a chain started on an
// unconfigured (nil) client stays nil instead of panicking.
type Client struct {
	baseURL string // OpenRouter's API base, e.g. https://openrouter.ai/api/v1 — AskChoice appends the selected surface's own path
	apiKey  string
	http    *http.Client
	model   string
	// decisions selects the Decisions API surface ({host}/api/alpha/decisions,
	// every decision model) over the System One one ({base}/systemone, Jev
	// only). Both speak the same request/response shape — see the package doc
	// and WithDecisionsAPI.
	decisions bool
}

// NewClient returns nil if apiKey is empty, mirroring brave.NewClient's
// nil-means-unconfigured convention — reuses the exact same OpenRouter key
// that already authenticates /chat/completions, no separate secret.
func NewClient(baseURL, apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}, model: Model}
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

// WithDecisionsAPI switches the client to OpenRouter's Decisions API
// ({host}/api/alpha/decisions) instead of the Jev-only System One endpoint —
// the surface every decision model in the live catalog speaks. Nil-safe.
func (c *Client) WithDecisionsAPI() *Client {
	if c == nil {
		return c
	}
	c.decisions = true
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

// endpoint is the full URL for this client's selected surface. System One
// lives under the versioned API base ("{base}/systemone"); the Decisions API
// lives at a fixed path on the host instead, not under that base.
func (c *Client) endpoint() string {
	if c.decisions {
		return decisionsURL(c.baseURL)
	}
	return strings.TrimRight(c.baseURL, "/") + "/systemone"
}

// decisionsURL derives {scheme}://{host}/api/alpha/decisions from the
// configured API base (https://openrouter.ai/api/v1). Appending is the
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
	wire := make(map[string]questionWire, len(questions))
	for k, q := range questions {
		wire[k] = questionWire{Type: "choice", Instructions: q.Instructions, Criteria: q.Criteria}
	}
	body, err := json.Marshal(requestBody{Model: c.model, State: state, Questions: wire})
	if err != nil {
		return nil, fmt.Errorf("encoding jev request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling jev: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading jev response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var e errorBody
		if json.Unmarshal(respBody, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("jev error (status %d): %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("jev error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var out Response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parsing jev response: %w", err)
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
