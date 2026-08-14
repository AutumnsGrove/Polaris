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
	baseURL  string
	apiKey   string
	model    string
	voice    string
	format   string // "mp3" or "pcm" — OpenRouter's Kokoro endpoint only documents these two
	provider string // OpenRouter provider name to pin to (e.g. "Together"), empty = no pin
	http     *http.Client
}

// ttsTimeout is generous relative to a typical chat-completion call —
// Kokoro's OpenRouter providers have shown double-digit-second latency
// even when healthy (see NewTTSClient's provider param), so a tighter
// timeout was producing false-positive 502s on perfectly successful
// syntheses that just hadn't finished yet.
const ttsTimeout = 45 * time.Second

func NewTTSClient(baseURL, apiKey, model, voice, format, provider string) *TTSClient {
	if format == "" {
		format = "mp3"
	}
	return &TTSClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		model:    model,
		voice:    voice,
		format:   format,
		provider: provider,
		http:     &http.Client{Timeout: ttsTimeout},
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

// kokoroCostPerMillionChars is OpenRouter's Kokoro-82M pricing, which
// varies by backing provider (see the model's /endpoints listing) — output
// is free either way, only input cost differs. The /audio/speech endpoint
// returns raw audio bytes with no JSON wrapper (unlike /audio/transcriptions),
// so there's no usage.cost field to read — this has to be computed here
// to fold TTS spend into the thread's running cost total.
var kokoroCostPerMillionChars = map[string]float64{
	"DeepInfra": 0.62,
	"Together":  4.00,
}

// kokoroDefaultCostPerMillionChars is used when provider is empty (no pin —
// OpenRouter free to route to either) or an unrecognized value; DeepInfra's
// rate, since it's the cheaper of the two and OpenRouter's own default.
const kokoroDefaultCostPerMillionChars = 0.62

func (c *TTSClient) EstimateCost(text string) float64 {
	rate, ok := kokoroCostPerMillionChars[c.provider]
	if !ok {
		rate = kokoroDefaultCostPerMillionChars
	}
	return float64(len(text)) * rate / 1_000_000
}

// Speak synthesizes text and returns the raw audio bytes.
func (c *TTSClient) Speak(text string) ([]byte, error) {
	payload := map[string]any{
		"model":           c.model,
		"input":           text,
		"voice":           c.voice,
		"response_format": c.format,
	}
	if c.provider != "" {
		// "only" (not "order"+allow_fallbacks:false) so a request never
		// silently falls back to the other provider — DeepInfra has shown
		// latency well past our own client timeout, so a silent fallback
		// there would just reintroduce the hang this pin exists to avoid.
		payload["provider"] = map[string]any{"only": []string{c.provider}}
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
