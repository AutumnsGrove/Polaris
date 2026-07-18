// tts.go implements text-to-speech via OpenRouter's dedicated audio/speech
// endpoint (separate from chat completions), using Kokoro-82M. Unlike the
// STT endpoint, this one returns raw audio bytes directly, not JSON.
package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type TTSClient struct {
	baseURL string
	apiKey  string
	model   string
	voice   string
	format  string // "mp3" or "pcm" — OpenRouter's Kokoro endpoint only documents these two
	http    *http.Client
}

func NewTTSClient(baseURL, apiKey, model, voice, format string) *TTSClient {
	if format == "" {
		format = "mp3"
	}
	return &TTSClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		voice:   voice,
		format:  format,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ContentType returns the MIME type matching the configured response
// format, for setting the HTTP response header.
func (c *TTSClient) ContentType() string {
	if c.format == "pcm" {
		return "audio/pcm"
	}
	return "audio/mpeg"
}

// kokoroCostPerMillionChars is OpenRouter's Kokoro-82M pricing: $0.62 per
// million input characters, output free. The /audio/speech endpoint
// returns raw audio bytes with no JSON wrapper (unlike /audio/transcriptions),
// so there's no usage.cost field to read — this has to be computed here
// to fold TTS spend into the thread's running cost total.
const kokoroCostPerMillionChars = 0.62

func (c *TTSClient) EstimateCost(text string) float64 {
	return float64(len(text)) * kokoroCostPerMillionChars / 1_000_000
}

// Speak synthesizes text and returns the raw audio bytes.
func (c *TTSClient) Speak(text string) ([]byte, error) {
	payload := map[string]any{
		"model":           c.model,
		"input":           text,
		"voice":           c.voice,
		"response_format": c.format,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TTS request failed: %w", err)
	}
	defer resp.Body.Close()

	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading TTS response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TTS server returned %d: %s", resp.StatusCode, string(audio))
	}

	return audio, nil
}
