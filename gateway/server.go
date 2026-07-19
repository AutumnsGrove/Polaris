// Package gateway exposes the HTTP + WebSocket API the SvelteKit
// frontend talks to: a REST surface for model/thread listing, and a
// single /ws endpoint that drives one agent turn per client message,
// streaming think/tool_call/tool_result/token/done events as they happen.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/logger"
	"polaris/places"
	"polaris/procmgr"
	"polaris/search"
	"polaris/store"
	"polaris/tools"
	"polaris/updater"
	"polaris/voice"
)

var log = logger.WithPrefix("gateway")

type Server struct {
	cfg        *config.Config
	db         *store.Store
	searxng    *search.SearXNGClient
	foursquare *places.FoursquareClient // nil if not configured
	stt        *voice.STTClient
	tts        *voice.TTSClient
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
		tts:        voice.NewTTSClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, cfg.Voice.TTSModel, cfg.Voice.TTSVoice, cfg.Voice.TTSFormat),
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
	s.mux.HandleFunc("POST /api/speak", s.handleSpeak)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	s.mux.HandleFunc("POST /api/update", s.handleUpdate)
	s.mux.HandleFunc("GET /ws", s.handleWS)

	if staticFS != nil {
		s.mux.Handle("/", spaHandler(staticFS))
	}
}

// spaHandler serves the embedded static build, falling back to
// index.html for any path that isn't a real file — adapter-static's
// SPA mode expects the server to do this for client-side routing.
//
// Explicit Cache-Control matters more than usual here: after a
// self-update rebuilds and restarts the binary, the browser has no way
// to know the server-side files changed unless told. Vite/SvelteKit's
// build hashes every filename under _app/immutable/ from its content, so
// those are safe to cache forever — a changed file gets a new URL,
// never the same one with different bytes. Everything else (index.html
// above all, since it's what points at the current hashes) must never
// be cached at all, or a stale index.html keeps requesting
// long-since-deleted hashed asset files after an update. Without this,
// browsers fall back to heuristic caching — Safari in particular caches
// aggressively enough that only a hard-refresh (impossible on mobile)
// would ever see a new build.
func spaHandler(staticFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(staticFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		if strings.HasPrefix(path, "/_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
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
	defaultID := s.effectiveDefaultModel()
	out := make([]modelOut, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		out = append(out, modelOut{ID: m.ID, Name: m.Name, Default: m.ID == defaultID})
	}
	writeJSON(w, out)
}

// effectiveDefaultModel is the settings-panel override if one's been set,
// otherwise config.yaml's default_model. Settings panel changes take
// effect immediately (no restart); config.yaml is the factory default.
func (s *Server) effectiveDefaultModel() string {
	if v, err := s.db.GetSetting("default_model"); err == nil && v != "" {
		return v
	}
	return s.cfg.DefaultModel
}

const (
	settingTheme        = "theme"       // "dark" or "light"
	settingShowPrices   = "show_prices" // "true" or "false"
	settingDefaultModel = "default_model"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.db.AllSettings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	theme := all[settingTheme]
	if theme == "" {
		theme = "dark"
	}
	showPrices := true
	if v, ok := all[settingShowPrices]; ok {
		showPrices = v == "true"
	}

	writeJSON(w, map[string]interface{}{
		"theme":                 theme,
		"show_prices":           showPrices,
		"default_model":         s.effectiveDefaultModel(),
		"context_window_tokens": s.cfg.ContextWindowTokens,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Theme        *string `json:"theme"`
		ShowPrices   *bool   `json:"show_prices"`
		DefaultModel *string `json:"default_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Theme != nil {
		if *req.Theme != "dark" && *req.Theme != "light" {
			http.Error(w, "theme must be 'dark' or 'light'", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingTheme, *req.Theme); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ShowPrices != nil {
		value := "false"
		if *req.ShowPrices {
			value = "true"
		}
		if err := s.db.SetSetting(settingShowPrices, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.DefaultModel != nil {
		if s.cfg.ModelByID(*req.DefaultModel).ID != *req.DefaultModel {
			http.Error(w, "unknown model id", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingDefaultModel, *req.DefaultModel); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleUpdate runs the same git-pull-and-rebuild steps as `polaris
// update`, then restarts the service — triggered from the settings
// panel instead of an SSH session. The response is flushed to the
// client *before* restarting: systemctl/launchctl kills this very
// process, so the client needs its answer in hand first.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	repoPath, err := updater.RepoPath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := updater.Run(repoPath)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"log":     result.PullOutput + "\n" + result.BuildOutput,
		})
		return
	}

	mgr, mgrErr := procmgr.New(s.cfg.Service.Label)
	restarting := mgrErr == nil && mgr.IsManaged()

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"log":        result.PullOutput + "\nbuild successful",
		"restarting": restarting,
	})

	if restarting {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		go func() {
			time.Sleep(300 * time.Millisecond) // give the response time to reach the client
			if err := mgr.Restart(); err != nil {
				log.Error("self-update restart failed", "err", err)
			}
		}()
	}
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

	// Only one turn runs at a time per connection (the frontend disables
	// the composer while busy), so a single cancel slot is enough to
	// support "stop". Each turn now runs in its own goroutine so this read
	// loop can keep pulling frames off the wire concurrently — otherwise a
	// "stop" message sent mid-turn would just sit unread until the turn
	// finished on its own, defeating the point.
	var cancelMu sync.Mutex
	var cancelTurn context.CancelFunc

	for {
		var msg ClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			cancelMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
			}
			cancelMu.Unlock()
			return // client disconnected or sent garbage
		}

		if msg.Type == "stop" {
			cancelMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
			}
			cancelMu.Unlock()
			continue
		}

		turnCtx, cancel := context.WithCancel(context.Background())
		cancelMu.Lock()
		cancelTurn = cancel
		cancelMu.Unlock()

		go func(ctx context.Context, cancel context.CancelFunc, msg ClientMessage) {
			defer cancel()
			s.handleTurn(ctx, msg, send)
			cancelMu.Lock()
			cancelTurn = nil
			cancelMu.Unlock()
		}(turnCtx, cancel, msg)
	}
}

func (s *Server) handleTurn(ctx context.Context, msg ClientMessage, send func(ServerEvent)) {
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

	requestedModel := msg.Model
	if requestedModel == "" {
		requestedModel = s.effectiveDefaultModel()
	}
	modelCfg := s.cfg.ModelByID(requestedModel)
	client := llm.NewClient(s.cfg.OpenRouter.BaseURL, s.cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		WithSessionID(threadID) // sticky routing — same provider endpoint across the thread, for cache hits
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: true, Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

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
	// SttCostUSD folds in push-to-talk transcription cost, if this
	// message originated from a voice memo.
	userMsgID, err := s.db.AddMessage(threadID, "user", msg.Content, "[]", msg.SttCostUSD)
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

	result, err := agent.Run(ctx, agentCtx, history, msg.Content)
	if err != nil {
		send(ServerEvent{Type: "error", ThreadID: threadID, UserMessageID: userMsgID, Message: err.Error()})
		return
	}

	citationsJSON, _ := json.Marshal(result.Citations)
	assistantMsgID, err := s.db.AddMessage(threadID, "assistant", result.Answer, string(citationsJSON), result.CostUSD)
	if err != nil {
		log.Warn("failed to persist assistant message", "err", err)
	}

	if err := s.db.SetContextTokens(threadID, result.ContextTokens); err != nil {
		log.Warn("failed to record context tokens", "err", err)
	}

	// Auto-compact once this thread crosses the configured threshold: the
	// model summarizes everything covered so far, and future turns build
	// history from that summary instead of the full raw text. The
	// messages table itself is untouched — only what gets sent back to
	// the LLM shrinks, the visible transcript stays the true record.
	contextTokens := result.ContextTokens
	if result.ContextTokens >= s.cfg.ContextWindowTokens && assistantMsgID != 0 {
		if summary, compactCost, err := s.compactThread(client, threadID, assistantMsgID); err != nil {
			log.Warn("auto-compaction failed", "thread", threadID, "err", err)
		} else {
			contextTokens = estimateTokens(summary)
			send(ServerEvent{Type: "compacted", ThreadID: threadID, Content: summary})
			result.CostUSD += compactCost
		}
	}

	// Total cost added to the thread this turn: the agent's LLM/tool
	// spend plus any STT cost from a voice memo, plus compaction's own
	// cost if it just ran — all three were just persisted above, so the
	// frontend's running total should reflect all of them.
	send(ServerEvent{
		Type:          "done",
		ThreadID:      threadID,
		UserMessageID: userMsgID,
		Citations:     result.Citations,
		CostUSD:       result.CostUSD + msg.SttCostUSD,
		ContextTokens: contextTokens,
	})
}

// compactThread summarizes every message up to and including throughID,
// via one extra (non-streamed, not shown as a normal answer) LLM call,
// and records that summary so loadHistory substitutes it for the raw
// messages it covers on every subsequent turn.
func (s *Server) compactThread(client *llm.Client, threadID string, throughID int64) (summary string, cost float64, err error) {
	history, err := s.loadHistory(threadID)
	if err != nil {
		return "", 0, err
	}
	prompt := []llm.ChatMessage{
		{Role: "system", Content: "Summarize the following conversation concisely but completely: preserve " +
			"every fact, decision, name, number, and cited URL that might matter later. This summary will " +
			"fully replace the conversation history, so omitting something means it's gone for good. Write " +
			"it as plain prose, not a transcript."},
	}
	prompt = append(prompt, history...)

	resp, err := client.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}

	if err := s.db.CompactThread(threadID, resp.Content, throughID, resp.CostUSD, estimateTokens(resp.Content)); err != nil {
		return "", 0, err
	}
	return resp.Content, resp.CostUSD, nil
}

// estimateTokens is a rough tokens-per-character heuristic (English text
// averages ~4 chars/token) used only to seed context_tokens right after a
// compaction, before the next real LLM call reports an actual count.
func estimateTokens(s string) int {
	return len(s) / 4
}

// loadHistory reconstructs prior turns as ChatMessage pairs so a
// resumed/continued thread has full context. If the thread has been
// auto-compacted, everything at or below compacted_through_id is replaced
// by a single summary message instead of being sent in full.
func (s *Server) loadHistory(threadID string) ([]llm.ChatMessage, error) {
	thread, err := s.db.GetThread(threadID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.db.GetMessages(threadID)
	if err != nil {
		return nil, err
	}

	history := make([]llm.ChatMessage, 0, len(msgs)+1)
	if thread.CompactedSummary != "" {
		history = append(history, llm.ChatMessage{
			Role: "assistant",
			Content: "(Summary of earlier conversation, compacted to save context — the full history " +
				"is no longer available, only this summary)\n\n" + thread.CompactedSummary,
		})
	}
	for _, m := range msgs {
		if m.ID <= thread.CompactedThroughID {
			continue // covered by the summary above
		}
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
