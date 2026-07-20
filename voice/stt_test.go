package voice

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("some other error"), false},
		{errors.New("STT server returned 502: bad gateway"), true},
		{errors.New("STT server returned 503: unavailable"), true},
		{errors.New("STT server returned 504: timeout"), true},
		{errors.New("context deadline exceeded"), true},
		{errors.New("request timeout"), true},
		{errors.New("STT server returned 401: unauthorized"), false},
	}
	for _, c := range cases {
		if got := isRetriable(c.err); got != c.want {
			t.Errorf("isRetriable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestTranscribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"text":  "hello world",
			"usage": map[string]interface{}{"cost": 0.0015},
		})
	}))
	defer srv.Close()

	client := NewSTTClient(srv.URL, "key", "primary-model", "")
	result, err := client.Transcribe([]byte("fake audio"), "webm")
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}
	if result.Text != "hello world" {
		t.Errorf("Text = %q, want %q", result.Text, "hello world")
	}
	if result.Cost != 0.0015 {
		t.Errorf("Cost = %v, want 0.0015", result.Cost)
	}
}

func TestTranscribe_EmptyTextIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"text": "   "})
	}))
	defer srv.Close()

	client := NewSTTClient(srv.URL, "key", "primary-model", "")
	if _, err := client.Transcribe([]byte("fake audio"), "webm"); err == nil {
		t.Fatal("expected an error for empty/whitespace-only transcribed text")
	}
}

func TestTranscribe_FallsBackOnRetriableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "primary-model" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unavailable"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"text": "from fallback"})
	}))
	defer srv.Close()

	client := NewSTTClient(srv.URL, "key", "primary-model", "fallback-model")
	result, err := client.Transcribe([]byte("fake audio"), "webm")
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}
	if result.Text != "from fallback" {
		t.Errorf("Text = %q, want the fallback model's response", result.Text)
	}
}

func TestTranscribe_NoFallbackConfigured_ErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	client := NewSTTClient(srv.URL, "key", "primary-model", "")
	if _, err := client.Transcribe([]byte("fake audio"), "webm"); err == nil {
		t.Fatal("expected an error with no fallback model configured")
	}
}

func TestTranscribe_NonRetriableErrorSkipsFallback(t *testing.T) {
	var fallbackCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "fallback-model" {
			fallbackCalled = true
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("bad key"))
	}))
	defer srv.Close()

	client := NewSTTClient(srv.URL, "key", "primary-model", "fallback-model")
	if _, err := client.Transcribe([]byte("fake audio"), "webm"); err == nil {
		t.Fatal("expected an error")
	}
	if fallbackCalled {
		t.Error("fallback model should not be tried for a non-retriable (401) error")
	}
}
