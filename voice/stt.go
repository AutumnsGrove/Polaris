// Package voice implements speech-to-text via OpenRouter, adapted from
// her-go's voice/stt.go. Polaris only ever talks to OpenRouter (no local
// Parakeet sidecar — this is a stateless web app, not a persistent
// companion process), so this keeps just the JSON+base64 wire format
// OpenRouter's /audio/transcriptions endpoint expects — notably NOT the
// standard OpenAI multipart/form-data shape most Whisper-compatible
// servers use.
package voice

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("voice")

type STTClient struct {
	baseURL       string
	apiKey        string
	model         string
	fallbackModel string // tried once if the primary model times out / 502-504s
	http          *http.Client
}

func NewSTTClient(baseURL, apiKey, model, fallbackModel string) *STTClient {
	return &STTClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		apiKey:        apiKey,
		model:         model,
		fallbackModel: fallbackModel,
		// OpenRouter's Whisper endpoint scales with audio duration — a 40s
		// memo can take 60s+ under load, so this needs real headroom.
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

type transcriptionResponse struct {
	Text  string `json:"text"`
	Usage *struct {
		Cost float64 `json:"cost"`
	} `json:"usage,omitempty"`
}

type TranscribeResult struct {
	Text string
	Cost float64
}

// Transcribe sends audio bytes to OpenRouter and returns the transcribed
// text. format should match what the browser's MediaRecorder produced
// (typically "webm"); OpenRouter also accepts ogg/wav/mp3/flac.
func (c *STTClient) Transcribe(audioBytes []byte, format string) (TranscribeResult, error) {
	result, err := c.transcribeWithModel(audioBytes, format, c.model)
	if err != nil && c.fallbackModel != "" && isRetriable(err) {
		log.Warn("primary STT model failed, trying fallback", "primary", c.model, "fallback", c.fallbackModel, "err", err)
		return c.transcribeWithModel(audioBytes, format, c.fallbackModel)
	}
	return result, err
}

func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504")
}

func (c *STTClient) transcribeWithModel(audioBytes []byte, format, model string) (TranscribeResult, error) {
	payload := map[string]any{
		"model": model,
		"input_audio": map[string]string{
			"data":   base64.StdEncoding.EncodeToString(audioBytes),
			"format": format,
		},
		"language": "en",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return TranscribeResult{}, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/audio/transcriptions", bytes.NewReader(body))
	if err != nil {
		return TranscribeResult{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return TranscribeResult{}, fmt.Errorf("STT request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return TranscribeResult{}, fmt.Errorf("reading STT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return TranscribeResult{}, fmt.Errorf("STT server returned %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed transcriptionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return TranscribeResult{}, fmt.Errorf("parsing STT response: %w", err)
	}

	trimmed := strings.TrimSpace(parsed.Text)
	if trimmed == "" {
		return TranscribeResult{}, fmt.Errorf("transcription returned empty text (audio may be silent or too short)")
	}

	var cost float64
	if parsed.Usage != nil {
		cost = parsed.Usage.Cost
	}

	log.Info("transcription complete", "duration", time.Since(start).Round(time.Millisecond), "text_len", len(trimmed), "cost", fmt.Sprintf("$%.6f", cost))

	return TranscribeResult{Text: trimmed, Cost: cost}, nil
}
