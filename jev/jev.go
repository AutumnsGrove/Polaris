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
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Model is the OpenRouter model id confirmed live to work — "jev-latest"
// (also "jev-1.13"/"typesafe/jev-1.13" work; "typesafe/jev-latest" and bare
// "jev" both 400 "does not exist").
const Model = "jev-latest"

// Client is nil-safe like brave.Client/parallel.Client/tavily.Client — see
// NewClient.
type Client struct {
	baseURL string // OpenRouter's base URL (no trailing slash), e.g. https://openrouter.ai/api/v1 — "/systemone" is appended per-request
	apiKey  string
	http    *http.Client
}

// NewClient returns nil if apiKey is empty, mirroring brave.NewClient's
// nil-means-unconfigured convention — reuses the exact same OpenRouter key
// that already authenticates /chat/completions, no separate secret.
func NewClient(baseURL, apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
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
	body, err := json.Marshal(requestBody{Model: Model, State: state, Questions: wire})
	if err != nil {
		return nil, fmt.Errorf("encoding jev request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/systemone", bytes.NewReader(body))
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
