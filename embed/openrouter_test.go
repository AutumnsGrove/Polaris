package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openRouterServer serves a canned response and captures the request.
func openRouterServer(t *testing.T, resp string, got *openRouterEmbedRequest, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %s, want /embeddings", r.URL.Path)
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Fatalf("decoding request: %v", err)
			}
		}
		_, _ = w.Write([]byte(resp))
	}))
}

func TestOpenRouter_SendsModelInputAndAuth(t *testing.T) {
	var got openRouterEmbedRequest
	var auth string
	srv := openRouterServer(t, `{"data":[{"index":0,"embedding":[0.5,0.25]}],"usage":{"prompt_tokens":7,"cost":0.0000042}}`, &got, &auth)
	defer srv.Close()

	res, err := NewOpenRouter("test-key", srv.URL, "perplexity/pplx-embed-v1-0.6b").
		EmbedBatch(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", auth)
	}
	if got.Model != "perplexity/pplx-embed-v1-0.6b" || len(got.Input) != 1 || got.Input[0] != "hello" {
		t.Errorf("request = %+v, want model=perplexity/pplx-embed-v1-0.6b input=[hello]", got)
	}
	// Pinned explicitly: the response type is number[]|string, and a
	// silent switch to base64 would decode as a confusing unmarshal error.
	if got.EncodingFormat != "float" {
		t.Errorf("encoding_format = %q, want float", got.EncodingFormat)
	}
	if res.CostUSD != 0.0000042 {
		t.Errorf("CostUSD = %v, want the reported 0.0000042", res.CostUSD)
	}
	if res.PromptTokens != 7 {
		t.Errorf("PromptTokens = %d, want 7", res.PromptTokens)
	}
	if res.Dims != 2 {
		t.Errorf("Dims = %d, want 2", res.Dims)
	}
	// The configured id, not the response echo — see Provider.Model.
	if res.Model != "perplexity/pplx-embed-v1-0.6b" {
		t.Errorf("Model = %q, want the configured id", res.Model)
	}
}

func TestOpenRouter_MapsByIndexNotPosition(t *testing.T) {
	// A reordered batch must not attach every vector to the wrong text:
	// that failure is silent and nearly invisible in a search index.
	srv := openRouterServer(t, `{"data":[
		{"index":1,"embedding":[2,2]},
		{"index":0,"embedding":[1,1]}
	]}`, nil, nil)
	defer srv.Close()

	res, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if res.Vectors[0][0] != 1 || res.Vectors[1][0] != 2 {
		t.Errorf("vectors = %v, want input order [1 1], [2 2]", res.Vectors)
	}
}

func TestOpenRouter_AbsentIndexFallsBackToPosition(t *testing.T) {
	srv := openRouterServer(t, `{"data":[{"embedding":[1,1]},{"embedding":[2,2]}]}`, nil, nil)
	defer srv.Close()

	res, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if res.Vectors[0][0] != 1 || res.Vectors[1][0] != 2 {
		t.Errorf("vectors = %v, want positional mapping", res.Vectors)
	}
}

func TestOpenRouter_IndexOutOfRangeIsAnError(t *testing.T) {
	srv := openRouterServer(t, `{"data":[{"index":9,"embedding":[1,1]}]}`, nil, nil)
	defer srv.Close()

	if _, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a"}); err == nil {
		t.Error("an out-of-range index should error, got nil")
	}
}

func TestOpenRouter_CountMismatchIsAnError(t *testing.T) {
	srv := openRouterServer(t, `{"data":[{"index":0,"embedding":[1,1]}]}`, nil, nil)
	defer srv.Close()

	if _, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("one vector for two inputs should error, got nil")
	}
}

func TestOpenRouter_EmptyVectorIsAnError(t *testing.T) {
	srv := openRouterServer(t, `{"data":[{"index":0,"embedding":[]}]}`, nil, nil)
	defer srv.Close()

	if _, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a"}); err == nil {
		t.Error("an empty vector should error, got nil")
	}
}

func TestOpenRouter_NonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Missing Authentication header"}}`))
	}))
	defer srv.Close()

	_, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("a 401 response should error, got nil")
	}
	// The provider's own message must survive into the error — that's the
	// difference between "embeddings broke" and "the key is wrong".
	if want := "401"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to mention %q", err, want)
	}
}

func TestOpenRouter_MissingUsageLeavesCostZero(t *testing.T) {
	srv := openRouterServer(t, `{"data":[{"index":0,"embedding":[1,1]}]}`, nil, nil)
	defer srv.Close()

	res, err := NewOpenRouter("k", srv.URL, "").EmbedBatch(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if res.CostUSD != 0 || res.PromptTokens != 0 {
		t.Errorf("CostUSD/PromptTokens = %v/%v, want 0/0 with no usage block", res.CostUSD, res.PromptTokens)
	}
}
