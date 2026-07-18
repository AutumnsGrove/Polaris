// Package gateway exposes the HTTP + WebSocket API the SvelteKit
// frontend talks to: a REST surface for model/thread listing, and a
// single /ws endpoint that drives one agent turn per client message,
// streaming think/tool_call/tool_result/token/done events as they happen.
package gateway

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/logger"
	"polaris/places"
	"polaris/search"
	"polaris/store"
	"polaris/tools"
	"polaris/voice"
)

var log = logger.WithPrefix("gateway")

type Server struct {
	cfg        *config.Config
	db         *store.Store
	searxng    *search.SearXNGClient
	foursquare *places.FoursquareClient // nil if not configured
	stt        *voice.STTClient
	mux        *http.ServeMux
}

// New builds the server. staticFS is the embedded SvelteKit build (see
// web/embed.go) — pass nil to run API/WS-only, which is what local dev
// does while `vite dev` serves the frontend and proxies through instead.
func New(cfg *config.Config, db *store.Store, staticFS fs.FS) *Server {
	s := &Server{
		cfg:        cfg,
		db:         db,
		searxng:    search.NewSearXNGClient(cfg.SearXNG.BaseURL),
		foursquare: places.NewFoursquareClient(cfg.Foursquare.APIKey),
		stt:        voice.NewSTTClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, cfg.Voice.STTModel, cfg.Voice.STTFallbackModel),
		mux:        http.NewServeMux(),
	}
	s.routes(staticFS)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes(staticFS fs.FS) {
	s.mux.HandleFunc("GET /api/models", s.handleModels)
	s.mux.HandleFunc("GET /api/threads", s.handleListThreads)
	s.mux.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	s.mux.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	s.mux.HandleFunc("POST /api/transcribe", s.handleTranscribe)
	s.mux.HandleFunc("GET /ws", s.handleWS)

	if staticFS != nil {
		s.mux.Handle("/", spaHandler(staticFS))
	}
}

// spaHandler serves the embedded static build, falling back to
// index.html for any path that isn't a real file — adapter-static's
// SPA mode expects the server to do this for client-side routing.
func spaHandler(staticFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(staticFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if _, err := fs.Stat(staticFS, path[1:]); err != nil {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelOut struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	out := make([]modelOut, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		out = append(out, modelOut{ID: m.ID, Name: m.Name, Default: m.ID == s.cfg.DefaultModel})
	}
	writeJSON(w, out)
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.db.ListThreads(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, threads)
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thread, err := s.db.GetThread(id)
	if err != nil {
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	messages, err := s.db.GetMessages(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		*store.Thread
		Messages []store.Message `json:"messages"`
	}{thread, messages})
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteThread(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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

var upgrader = websocket.Upgrader{
	// Tailscale-only deployment (like every other service in this
	// homelab) — no public exposure, so a permissive origin check is
	// fine here rather than maintaining an allowlist.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warn("websocket upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// gorilla/websocket connections aren't safe for concurrent writes;
	// emit() is called synchronously from the agent loop on this same
	// goroutine, so a mutex is defensive but cheap insurance against
	// future concurrent use (e.g. a heartbeat goroutine).
	var writeMu sync.Mutex
	send := func(evt ServerEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(evt)
	}

	for {
		var msg ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return // client disconnected or sent garbage
		}
		s.handleTurn(msg, send)
	}
}

func (s *Server) handleTurn(msg ClientMessage, send func(ServerEvent)) {
	threadID := msg.ThreadID
	isNewThread := threadID == ""
	if isNewThread {
		threadID = uuid.NewString()
	}

	// Retry/edit: wipe the message being replaced and everything after it
	// (no branching history) before persisting the new/unchanged content.
	if msg.EditFromID != 0 {
		if err := s.db.DeleteMessagesFrom(threadID, msg.EditFromID); err != nil {
			send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
			return
		}
	}

	modelCfg := s.cfg.ModelByID(msg.Model)
	client := llm.NewClient(s.cfg.OpenRouter.BaseURL, s.cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		WithSessionID(threadID) // sticky routing — same provider endpoint across the thread, for cache hits

	if isNewThread {
		title := msg.Content
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		if err := s.db.CreateThread(threadID, title, modelCfg.ID); err != nil {
			send(ServerEvent{Type: "error", Message: err.Error()})
			return
		}
	}

	history, err := s.loadHistory(threadID)
	if err != nil {
		send(ServerEvent{Type: "error", Message: err.Error()})
		return
	}

	// Persist the user message before running the agent, not after — so
	// it (and its ID, needed for retry/edit) survives even if the turn
	// below errors out. Previously a failed turn left no record at all.
	userMsgID, err := s.db.AddMessage(threadID, "user", msg.Content, "[]", 0)
	if err != nil {
		send(ServerEvent{Type: "error", ThreadID: threadID, Message: err.Error()})
		return
	}
	send(ServerEvent{Type: "user_message", ThreadID: threadID, UserMessageID: userMsgID})

	emit := func(eventType string, payload map[string]interface{}) {
		evt := ServerEvent{Type: eventType, ThreadID: threadID}
		if v, ok := payload["content"].(string); ok {
			evt.Content = v
		}
		if v, ok := payload["tool"].(string); ok {
			evt.Tool = v
		}
		if v, ok := payload["args"].(map[string]interface{}); ok {
			evt.Args = v
		}
		if v, ok := payload["result"].(string); ok {
			evt.Result = v
		}
		if v, ok := payload["citations"].([]tools.Citation); ok {
			evt.Citations = v
		}
		send(evt)
	}

	agentCtx := &tools.Context{
		SearXNG:         s.searxng,
		Foursquare:      s.foursquare,
		DefaultLocation: s.cfg.DefaultLocation,
		VoiceMode:       msg.VoiceMode,
		LLM:             client,
		Emit:            emit,
	}

	result, err := agent.Run(agentCtx, history, msg.Content)
	if err != nil {
		send(ServerEvent{Type: "error", ThreadID: threadID, UserMessageID: userMsgID, Message: err.Error()})
		return
	}

	citationsJSON, _ := json.Marshal(result.Citations)
	if _, err := s.db.AddMessage(threadID, "assistant", result.Answer, string(citationsJSON), result.CostUSD); err != nil {
		log.Warn("failed to persist assistant message", "err", err)
	}

	send(ServerEvent{
		Type:          "done",
		ThreadID:      threadID,
		UserMessageID: userMsgID,
		Citations:     result.Citations,
		CostUSD:       result.CostUSD,
	})
}

// loadHistory reconstructs prior turns as ChatMessage pairs so a
// resumed/continued thread has full context.
func (s *Server) loadHistory(threadID string) ([]llm.ChatMessage, error) {
	msgs, err := s.db.GetMessages(threadID)
	if err != nil {
		return nil, err
	}
	history := make([]llm.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		history = append(history, llm.ChatMessage{Role: m.Role, Content: m.Content})
	}
	return history, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func boolPtr(b bool) *bool { return &b }

var _ = time.Second // reserved for future request timeouts
