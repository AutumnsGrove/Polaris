package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// fakeProvider is the Provider double the Client-level tests use — the
// concrete providers have their own tests against real HTTP servers.
type fakeProvider struct {
	model string
	dims  int
	// calls records the size of each Embed request, so chunking is
	// observable.
	calls []int
	// costPerCall is added to every Result.CostUSD.
	costPerCall float64
	// local mirrors Provider.Local.
	local bool
	err   error
}

func (f *fakeProvider) Model() string { return f.model }

func (f *fakeProvider) Local() bool { return f.local }

func (f *fakeProvider) Embed(_ context.Context, texts []string) (Result, error) {
	f.calls = append(f.calls, len(texts))
	if f.err != nil {
		return Result{}, f.err
	}
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = make([]float32, f.dims)
		vecs[i][0] = float32(i)
	}
	return Result{Vectors: vecs, Model: f.model, Dims: f.dims, CostUSD: f.costPerCall, PromptTokens: len(texts)}, nil
}

func TestNewClients_NilWhenUnconfigured(t *testing.T) {
	if c := NewOllama("", ""); c != nil {
		t.Errorf("NewOllama(\"\", \"\") = %v, want nil", c)
	}
	if c := NewOpenRouter("", "", ""); c != nil {
		t.Errorf("NewOpenRouter with no key = %v, want nil", c)
	}
}

func TestNewClients_DefaultModels(t *testing.T) {
	if got := NewOllama("http://localhost:11434", "").Model(); got != DefaultOllamaModel {
		t.Errorf("ollama default model = %q, want %q", got, DefaultOllamaModel)
	}
	if got := NewOpenRouter("key", "", "").Model(); got != DefaultOpenRouterModel {
		t.Errorf("openrouter default model = %q, want %q", got, DefaultOpenRouterModel)
	}
	if got := NewOpenRouter("key", "", "custom/model").Model(); got != "custom/model" {
		t.Errorf("openrouter explicit model = %q, want custom/model", got)
	}
}

func TestNilClient_IsSafe(t *testing.T) {
	var c *Client
	if c.Model() != "" {
		t.Errorf("nil Client.Model() = %q, want empty", c.Model())
	}
	if _, err := c.Embed(context.Background(), "q"); err == nil {
		t.Error("nil Client.Embed should error, got nil")
	}
	if _, err := c.EmbedBatch(context.Background(), []string{"q"}); err == nil {
		t.Error("nil Client.EmbedBatch should error, got nil")
	}
}

func TestEmbedBatch_ChunksAtMaxBatch(t *testing.T) {
	p := &fakeProvider{model: "m", dims: 2}
	c := newClient(p)

	n := MaxBatch + 5
	texts := make([]string, n)
	for i := range texts {
		texts[i] = fmt.Sprintf("text %d", i)
	}
	res, err := c.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(res.Vectors) != n {
		t.Fatalf("got %d vectors, want %d", len(res.Vectors), n)
	}
	if len(p.calls) != 2 || p.calls[0] != MaxBatch || p.calls[1] != 5 {
		t.Errorf("provider calls = %v, want [%d 5]", p.calls, MaxBatch)
	}
	// Vectors must stay in input order across chunk boundaries — the
	// indexer writes them positionally.
	if res.Vectors[0][0] != 0 || res.Vectors[MaxBatch][0] != 0 {
		t.Errorf("chunk boundary vectors = %v, %v; want per-chunk first-index 0", res.Vectors[0][0], res.Vectors[MaxBatch][0])
	}
}

func TestEmbedBatch_SumsCostAcrossChunks(t *testing.T) {
	p := &fakeProvider{model: "m", dims: 2, costPerCall: 0.0001}
	c := newClient(p)
	texts := make([]string, MaxBatch+1)
	res, err := c.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if want := 0.0002; res.CostUSD < want-1e-12 || res.CostUSD > want+1e-12 {
		t.Errorf("CostUSD = %v, want %v (two calls summed)", res.CostUSD, want)
	}
	if res.PromptTokens != len(texts) {
		t.Errorf("PromptTokens = %d, want %d", res.PromptTokens, len(texts))
	}
}

func TestEmbedBatch_ErrorDiscardsPartialResults(t *testing.T) {
	p := &fakeProvider{model: "m", dims: 2, err: errors.New("boom")}
	c := newClient(p)
	res, err := c.EmbedBatch(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if len(res.Vectors) != 0 {
		t.Errorf("Vectors = %v, want none on error", res.Vectors)
	}
}

func TestEmbedBatch_RejectsEmptyInput(t *testing.T) {
	c := newClient(&fakeProvider{model: "m", dims: 2})
	if _, err := c.EmbedBatch(context.Background(), nil); err == nil {
		t.Error("empty input should error, got nil")
	}
}

func TestEmbedBatch_RejectsInconsistentDimsAcrossChunks(t *testing.T) {
	// A provider that changes width between chunked requests would
	// silently produce a store whose vectors can't be compared across the
	// boundary; make it loud instead. (With-in one request is caught by the
	// provider implementations themselves.)
	c := newClient(&shiftingDimsProvider{})
	texts := make([]string, MaxBatch+1)
	if _, err := c.EmbedBatch(context.Background(), texts); err == nil {
		t.Error("inconsistent dimensions should error, got nil")
	}
}

type shiftingDimsProvider struct{ calls int32 }

func (p *shiftingDimsProvider) Model() string { return "m" }

func (p *shiftingDimsProvider) Local() bool { return true }
func (p *shiftingDimsProvider) Embed(_ context.Context, texts []string) (Result, error) {
	n := int(atomic.AddInt32(&p.calls, 1))
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = make([]float32, n+1)
	}
	return Result{Vectors: vecs, Model: "m", Dims: n + 1}, nil
}

func TestEmbed_SingleReturnsFirstVector(t *testing.T) {
	c := newClient(&fakeProvider{model: "m", dims: 3})
	vec, err := c.Embed(context.Background(), "one text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("vec = %v, want 3 components", vec)
	}
}

func TestCosineSimilarity_IdenticalVectorsIsOne(t *testing.T) {
	v := []float32{1, 2, 3}
	if sim := CosineSimilarity(v, v); sim < 0.999999 || sim > 1.000001 {
		t.Errorf("CosineSimilarity(v, v) = %v, want ~1.0", sim)
	}
}

func TestCosineSimilarity_OrthogonalVectorsIsZero(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if sim := CosineSimilarity(a, b); sim < -0.000001 || sim > 0.000001 {
		t.Errorf("CosineSimilarity(a, b) = %v, want ~0", sim)
	}
}

func TestCosineSimilarity_OppositeVectorsIsNegativeOne(t *testing.T) {
	a := []float32{1, 1}
	b := []float32{-1, -1}
	if sim := CosineSimilarity(a, b); sim < -1.000001 || sim > -0.999999 {
		t.Errorf("CosineSimilarity(a, b) = %v, want ~-1.0", sim)
	}
}

func TestCosineSimilarity_MismatchedLengthIsZero(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	if sim := CosineSimilarity(a, b); sim != 0 {
		t.Errorf("CosineSimilarity(mismatched lengths) = %v, want 0", sim)
	}
}

func TestCosineSimilarity_ZeroVectorIsZero(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	if sim := CosineSimilarity(a, b); sim != 0 {
		t.Errorf("CosineSimilarity(zero vector) = %v, want 0", sim)
	}
}

// TestOllama_SendsModelAndInputAndReturnsVectors covers the batch endpoint
// shape: input array in, embeddings array out.
func TestOllama_SendsModelAndInputAndReturnsVectors(t *testing.T) {
	var got ollamaEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		})
	}))
	defer srv.Close()

	res, err := NewOllama(srv.URL, "custom-model").EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if got.Model != "custom-model" || len(got.Input) != 2 || got.Input[1] != "b" {
		t.Errorf("request = %+v, want model=custom-model input=[a b]", got)
	}
	if res.Dims != 2 || len(res.Vectors) != 2 {
		t.Errorf("res = %+v, want 2 vectors of dims 2", res)
	}
	// A local model costs no money — 0 is the right answer, not "unknown".
	if res.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a local provider", res.CostUSD)
	}
}

func TestOllama_CountMismatchIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: [][]float32{{0.1}}})
	}))
	defer srv.Close()

	if _, err := NewOllama(srv.URL, "").EmbedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("one embedding for two inputs should error, got nil")
	}
}

func TestOllama_NonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewOllama(srv.URL, "").Embed(context.Background(), "q"); err == nil {
		t.Error("a 500 response should error, got nil")
	}
}

// TestLocal_ReflectsProvider pins the one decision Provider.Local exists
// for: whether agent's per-turn embedding warm-up is worth making. Warming
// up a hosted provider is a wasted API call per turn.
func TestLocal_ReflectsProvider(t *testing.T) {
	if !NewOllama("http://localhost:11434", "").Local() {
		t.Error("Local() = false for Ollama, want true — a local model does cold-start")
	}
	if NewOpenRouter("k", "", "").Local() {
		t.Error("Local() = true for OpenRouter, want false — there's no remote cold start to hide")
	}
	var c *Client
	if c.Local() {
		t.Error("nil Client.Local() = true, want false")
	}
}

func TestTruncateInput(t *testing.T) {
	short := "hello"
	if got := truncateInput(short); got != short {
		t.Errorf("short text changed: %q", got)
	}
	// Multi-byte runes: 3 bytes each, so byte length exceeds the cap long
	// before rune count does. A byte-based cut would split a rune.
	multi := strings.Repeat("é", MaxInputChars+10)
	got := truncateInput(multi)
	if n := utf8.RuneCountInString(got); n != MaxInputChars {
		t.Errorf("rune count = %d, want %d", n, MaxInputChars)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation produced invalid UTF-8")
	}
	exact := strings.Repeat("é", MaxInputChars)
	if truncateInput(exact) != exact {
		t.Error("text exactly at the cap was altered")
	}
}

func TestEmbedBatch_DoesNotMutateCallersTexts(t *testing.T) {
	c := newClient(&fakeProvider{model: "m", dims: 2})
	long := strings.Repeat("x", MaxInputChars+5)
	texts := []string{long}
	if _, err := c.EmbedBatch(context.Background(), texts); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(texts[0]) != MaxInputChars+5 {
		t.Errorf("caller's slice was truncated in place (len %d); the indexer hashes the original", len(texts[0]))
	}
}
