package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_EmptyBaseURLReturnsNil(t *testing.T) {
	if c := NewClient("", ""); c != nil {
		t.Errorf("NewClient(\"\", \"\") = %v, want nil", c)
	}
}

func TestNewClient_DefaultsModelWhenEmpty(t *testing.T) {
	c := NewClient("http://localhost:11434", "")
	if c.model != defaultModel {
		t.Errorf("model = %q, want %q", c.model, defaultModel)
	}
}

func TestEmbed_SendsModelAndPromptReturnsVector(t *testing.T) {
	var gotReq embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		json.NewEncoder(w).Encode(embedResponse{Embedding: []float32{0.1, 0.2, 0.3}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "custom-model")
	vec, err := c.Embed(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("vec = %v, want length 3", vec)
	}
	if gotReq.Model != "custom-model" || gotReq.Prompt != "test query" {
		t.Errorf("request = %+v, want model=custom-model prompt=%q", gotReq, "test query")
	}
}

func TestEmbed_EmptyEmbeddingIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embedResponse{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if _, err := c.Embed(context.Background(), "q"); err == nil {
		t.Error("Embed with an empty response embedding should return an error, got nil")
	}
}

func TestEmbed_NonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if _, err := c.Embed(context.Background(), "q"); err == nil {
		t.Error("Embed with a 500 response should return an error, got nil")
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
