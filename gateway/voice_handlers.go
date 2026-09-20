package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"polaris/voice"
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
		// No thread_id yet — transcription happens before the message it
		// becomes is ever sent, so this can only be a global event.
		s.db.LogEvent("", "warn", "voice.transcribe", "transcription failed", map[string]interface{}{"err": err.Error(), "format": format}, "")
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
		s.db.LogEvent(req.ThreadID, "warn", "voice.speak", "TTS failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	cost := s.tts.EstimateCost(req.Text)
	if req.ThreadID != "" {
		if err := s.db.AddCost(req.ThreadID, cost); err != nil {
			log.Warn("failed to record TTS cost", "err", err)
			s.db.LogEvent(req.ThreadID, "warn", "voice.speak", "recording TTS cost failed", map[string]interface{}{"err": err.Error()}, "")
		}
	}

	w.Header().Set("Content-Type", s.tts.ContentType())
	w.Header().Set("X-Tts-Cost-Usd", fmt.Sprintf("%.6f", cost))
	w.Write(audio)
}

// speakStreamChunk is one line of handleSpeakStream's NDJSON response —
// either a synthesized chunk (Seq + AudioBase64 populated), a fatal error
// partway through (Seq + Error), or the final summary line (Done +
// CostUSD, plus File once the persisted audio has actually been written).
// Kept as one struct with omitempty tags rather than three separate shapes
// so the frontend only needs one parse path per line.
type speakStreamChunk struct {
	Seq         int     `json:"seq"`
	AudioBase64 string  `json:"audio_base64,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
	Error       string  `json:"error,omitempty"`
	Done        bool    `json:"done,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	// File is the persisted read-aloud audio's URL (GET
	// /api/workspace/:thread_id/:filename), set only on the Done line and
	// only when MessageID/ThreadID were both given — see the doc comment
	// below on why persistence is skipped otherwise.
	File string `json:"file,omitempty"`
}

// handleSpeakStream synthesizes text one sentence-chunk at a time (see
// voice.SplitIntoSpeechChunks) and streams each chunk back as its own
// NDJSON line the instant it's ready, instead of handleSpeak's single
// request/response that makes the browser wait for the entire answer to
// finish synthesizing before any audio can start playing. This is
// OpenRouter's own documented pattern for long text (chunk + concatenate
// client-side), not a special streaming mode of the TTS endpoint itself —
// see voice/chunk.go's doc comment.
//
// Each chunk is requested as raw "pcm" (not the client's configured
// tts_format) regardless of site-wide config — see voice.WrapPCMAsWAV's
// doc comment for why: raw PCM chunks concatenate byte-exact with no
// frame-boundary artifacts, unlike independently-encoded MP3 chunks naively
// joined end to end. Each chunk is wrapped in its own throwaway WAV header
// before being sent to the browser (so it stays immediately playable via
// `new Audio(blobURL)`, exactly as before), while the same raw bytes are
// also accumulated server-side and wrapped in one real WAV header once
// streaming finishes — that becomes the persisted, reloadable, scrubbable
// file. Same underlying samples, two different header wrappings for two
// different purposes; no double synthesis, no format renegotiation.
func (s *Server) handleSpeakStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text      string `json:"text"`
		ThreadID  string `json:"thread_id"`
		MessageID int64  `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}

	chunks := voice.SplitIntoSpeechChunks(req.Text)
	if len(chunks) == 0 {
		http.Error(w, "nothing to speak after removing formatting", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Shouldn't happen with Go's stdlib server, but a proxy in front
		// (or a future non-flushing ResponseWriter wrapper) could lose
		// this — fail loudly rather than silently buffering the whole
		// response and defeating the point of streaming at all.
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)

	// Persistence (this turn's read-aloud becoming a real, reloadable file)
	// needs a message row to attach to — a ghost/anonymous turn never gets
	// one (see turn.go's assistantMsgID doc comment), and an older/anonymous
	// caller might not send MessageID at all. In that case we still stream
	// chunks for immediate playback, exactly as before this change; there's
	// just nothing to write to disk or a database row to update.
	persist := req.ThreadID != "" && req.MessageID != 0
	var pcmBuf bytes.Buffer

	var totalCost float64
	for i, chunk := range chunks {
		pcm, err := s.tts.SpeakWithFormat(chunk, "pcm")
		if err != nil {
			log.Warn("TTS stream chunk failed", "seq", i, "err", err)
			s.db.LogEvent(req.ThreadID, "warn", "voice.speak", "TTS stream chunk failed",
				map[string]interface{}{"err": err.Error(), "seq": i}, "")
			// Headers (200 OK) are already sent — an HTTP status code can't
			// change mid-stream, so a mid-stream failure is reported as a
			// line the frontend checks for, not an HTTP error response.
			// Whatever synthesized successfully before this point still
			// gets to play; this just stops queuing more.
			enc.Encode(speakStreamChunk{Seq: i, Error: err.Error()})
			flusher.Flush()
			break
		}
		totalCost += s.tts.EstimateCost(chunk)
		if persist {
			pcmBuf.Write(pcm)
		}
		enc.Encode(speakStreamChunk{
			Seq:         i,
			AudioBase64: base64.StdEncoding.EncodeToString(voice.WrapPCMAsWAV(pcm)),
			ContentType: "audio/wav",
		})
		flusher.Flush()
	}

	if req.ThreadID != "" && totalCost > 0 {
		if err := s.db.AddCost(req.ThreadID, totalCost); err != nil {
			log.Warn("failed to record TTS stream cost", "err", err)
			s.db.LogEvent(req.ThreadID, "warn", "voice.speak", "recording TTS stream cost failed",
				map[string]interface{}{"err": err.Error()}, "")
		}
	}

	var file string
	if persist && pcmBuf.Len() > 0 {
		var err error
		file, err = s.persistTTSAudio(req.ThreadID, req.MessageID, pcmBuf.Bytes())
		if err != nil {
			// Persistence failing shouldn't invalidate a synthesis the
			// browser already heard live — log and move on with an empty
			// File field, same tolerance as every other post-hoc-UPDATE
			// failure in this codebase (SetMessageDuration, SetMessageCards,
			// etc.), rather than turning this into an HTTP error this late.
			log.Warn("failed to persist read-aloud audio", "err", err)
			s.db.LogEvent(req.ThreadID, "warn", "voice.speak", "persisting read-aloud audio failed",
				map[string]interface{}{"err": err.Error()}, "")
		}
	}

	enc.Encode(speakStreamChunk{Done: true, CostUSD: totalCost, File: file})
	flusher.Flush()
}

// persistTTSAudio writes a turn's fully-synthesized read-aloud audio (raw
// PCM, already accumulated chunk by chunk) as one real WAV file in the
// thread's code_exec workspace directory, and records its addressable
// filename on the message row — mirrors resolveOneAttachment's
// (gateway/attachments.go) generated-id/workspace-dir/permissions pattern,
// the existing "write a file a model or browser can address by a short id"
// convention in this codebase. Returns the URL the frontend can point a
// player at directly (GET /api/workspace/:thread_id/:filename,
// gateway/workspace.go — unchanged, already serves whatever's on disk).
func (s *Server) persistTTSAudio(threadID string, messageID int64, pcm []byte) (string, error) {
	shortID, err := generateShortFileID()
	if err != nil {
		return "", fmt.Errorf("generating file id: %w", err)
	}
	filename := shortID + ".wav"

	cfg := s.liveConfig()
	workspaceDir := filepath.Join(cfg.CodeExec.WorkspaceDir, threadID)
	if err := os.MkdirAll(workspaceDir, 0o777); err != nil {
		return "", fmt.Errorf("preparing workspace directory: %w", err)
	}
	if err := os.Chmod(workspaceDir, 0o777); err != nil {
		return "", fmt.Errorf("setting workspace directory permissions: %w", err)
	}

	destPath := filepath.Join(workspaceDir, filename)
	if err := os.WriteFile(destPath, voice.WrapPCMAsWAV(pcm), 0o644); err != nil {
		return "", fmt.Errorf("writing persisted audio: %w", err)
	}

	if err := s.db.SetMessageTTSAudioFileID(messageID, filename); err != nil {
		return "", fmt.Errorf("recording persisted audio on message: %w", err)
	}

	return fmt.Sprintf("/api/workspace/%s/%s", threadID, filename), nil
}
