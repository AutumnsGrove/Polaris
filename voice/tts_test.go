package voice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTTSClient_DefaultsFormatToMP3(t *testing.T) {
	client := NewTTSClient("http://example.com", "key", "model", "voice", "", "")
	if client.ContentType() != "audio/mpeg" {
		t.Errorf("ContentType() = %q, want audio/mpeg for the default mp3 format", client.ContentType())
	}
}

func TestContentType_PCM(t *testing.T) {
	client := NewTTSClient("http://example.com", "key", "model", "voice", "pcm", "")
	if client.ContentType() != "audio/pcm" {
		t.Errorf("ContentType() = %q, want audio/pcm", client.ContentType())
	}
}

func TestEstimateCost(t *testing.T) {
	client := NewTTSClient("http://example.com", "key", "model", "voice", "mp3", "DeepInfra")
	// 1,000,000 chars * $0.62/million = $0.62 exactly.
	cost := client.EstimateCost(string(make([]byte, 1_000_000)))
	if cost < 0.619 || cost > 0.621 {
		t.Errorf("EstimateCost(1M chars) = %v, want ~0.62", cost)
	}
	if client.EstimateCost("") != 0 {
		t.Errorf("EstimateCost(\"\") = %v, want 0", client.EstimateCost(""))
	}
}

func TestEstimateCost_TogetherIsPricierThanDeepInfra(t *testing.T) {
	deepinfra := NewTTSClient("http://example.com", "key", "model", "voice", "mp3", "DeepInfra")
	together := NewTTSClient("http://example.com", "key", "model", "voice", "mp3", "Together")
	text := string(make([]byte, 1_000_000))
	if together.EstimateCost(text) <= deepinfra.EstimateCost(text) {
		t.Errorf("Together cost %v should exceed DeepInfra cost %v", together.EstimateCost(text), deepinfra.EstimateCost(text))
	}
}

func TestSpeak_ReturnsAudioBytes(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("fake mp3 bytes"))
	}))
	defer srv.Close()

	client := NewTTSClient(srv.URL, "key", "kokoro", "bf_lily", "mp3", "")
	audio, err := client.Speak("Hello there")
	if err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}
	if string(audio) != "fake mp3 bytes" {
		t.Errorf("audio = %q, want the raw response body", audio)
	}
	if gotBody["voice"] != "bf_lily" || gotBody["input"] != "Hello there" {
		t.Errorf("request body = %+v, want voice=bf_lily input=%q", gotBody, "Hello there")
	}
	if _, hasProvider := gotBody["provider"]; hasProvider {
		t.Errorf("request body = %+v, want no provider field when provider is unset", gotBody)
	}
}

func TestSpeak_PinsProvider(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("fake mp3 bytes"))
	}))
	defer srv.Close()

	client := NewTTSClient(srv.URL, "key", "kokoro", "bf_lily", "mp3", "Together")
	if _, err := client.Speak("Hello there"); err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	provider, ok := gotBody["provider"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body = %+v, want a provider object", gotBody)
	}
	only, ok := provider["only"].([]interface{})
	if !ok || len(only) != 1 || only[0] != "Together" {
		t.Errorf("provider.only = %+v, want [\"Together\"]", provider["only"])
	}
}

func TestSpeak_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("upstream error"))
	}))
	defer srv.Close()

	client := NewTTSClient(srv.URL, "key", "kokoro", "bf_lily", "mp3", "")
	if _, err := client.Speak("hi"); err == nil {
		t.Fatal("expected an error for a 502 response")
	}
}
