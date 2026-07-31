package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTTSServer stands in for OpenRouter's /audio/speech endpoint — every
// request gets fixed audio bytes back, regardless of the input text, since
// handleSpeakStream's own chunking logic (already covered by
// voice.SplitIntoSpeechChunks' tests) is what determines how many requests
// this sees, not what's tested here.
func fakeTTSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("fake-mp3-bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readNDJSONLines(t *testing.T, body []byte) []speakStreamChunk {
	t.Helper()
	var lines []speakStreamChunk
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c speakStreamChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("unmarshaling NDJSON line %q: %v", line, err)
		}
		lines = append(lines, c)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning response body: %v", err)
	}
	return lines
}

func TestHandleSpeakStream_EmitsOneLinePerChunkThenDone(t *testing.T) {
	ttsSrv := fakeTTSServer(t)
	h := newTestHarness(t, ttsSrv.URL)

	reqBody, _ := json.Marshal(map[string]string{
		"text": "First sentence here. Second sentence here.",
	})
	resp, err := http.Post(h.url("/api/speak/stream"), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /api/speak/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}

	lines := readNDJSONLines(t, body)
	if len(lines) < 2 {
		t.Fatalf("got %d NDJSON lines, want at least 2 (one chunk + done): %+v", len(lines), lines)
	}

	last := lines[len(lines)-1]
	if !last.Done {
		t.Errorf("last line = %+v, want done: true", last)
	}

	for _, l := range lines[:len(lines)-1] {
		if l.AudioBase64 == "" {
			t.Errorf("non-final line missing audio_base64: %+v", l)
		}
		if l.ContentType == "" {
			t.Errorf("non-final line missing content_type: %+v", l)
		}
	}
}

func TestHandleSpeakStream_TextRequired(t *testing.T) {
	ttsSrv := fakeTTSServer(t)
	h := newTestHarness(t, ttsSrv.URL)

	resp, err := http.Post(h.url("/api/speak/stream"), "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("POST /api/speak/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
