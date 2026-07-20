package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxAudioBytes caps push-to-talk uploads at ~15MB — generous for a
// voice memo (webm/opus at typical bitrates runs well under 1MB/minute),
// tight enough to not let a stuck recording flood the server.
const maxAudioBytes = 15 << 20

// handleTranscribe accepts a raw audio body from the browser's
// MediaRecorder (format given via ?format=webm, matching the blob's
// mime type) and returns the transcribed text via OpenRouter Whisper.
func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "webm"
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAudioBytes+1))
	if err != nil {
		http.Error(w, "reading audio body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxAudioBytes {
		http.Error(w, "audio too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) == 0 {
		http.Error(w, "empty audio body", http.StatusBadRequest)
		return
	}

	result, err := s.stt.Transcribe(body, format)
	if err != nil {
		log.Warn("transcription failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]interface{}{"text": result.Text, "cost_usd": result.Cost})
}

// handleSpeak synthesizes text via Kokoro and returns raw audio bytes.
// The cost (computed manually — this endpoint's response has no JSON
// usage field to read it from) is folded into the thread's running total
// via the X-Tts-Cost-Usd response header, since the body is audio, not JSON.
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text     string `json:"text"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}

	audio, err := s.tts.Speak(req.Text)
	if err != nil {
		log.Warn("TTS failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	cost := s.tts.EstimateCost(req.Text)
	if req.ThreadID != "" {
		if err := s.db.AddCost(req.ThreadID, cost); err != nil {
			log.Warn("failed to record TTS cost", "err", err)
		}
	}

	w.Header().Set("Content-Type", s.tts.ContentType())
	w.Header().Set("X-Tts-Cost-Usd", fmt.Sprintf("%.6f", cost))
	w.Write(audio)
}
