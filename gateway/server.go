// Package gateway exposes the HTTP + WebSocket API the SvelteKit
// frontend talks to: a REST surface for model/thread listing, and a
// single /ws endpoint that drives one agent turn per client message,
// streaming think/tool_call/tool_result/token/done events as they happen.
//
// Handlers live grouped by resource in separate files (models.go,
// settings.go, threads.go, voice_handlers.go, ws.go, turn.go) — this file
// is just the Server type, wiring, and the live-config helper they all
// share.
package gateway

import (
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"polaris/config"
	"polaris/logger"
	"polaris/places"
	"polaris/search"
	"polaris/store"
	"polaris/voice"
)

var log = logger.WithPrefix("gateway")

type Server struct {
	// cfg is the last config.yaml load, refreshed on demand by liveConfig
	// rather than read once at startup — see liveConfig for why.
	cfg     *config.Config
	cfgPath string
	cfgMu   sync.RWMutex

	db         *store.Store
	searxng    *search.SearXNGClient
	foursquare *places.FoursquareClient // nil if not configured
	stt        *voice.STTClient
	tts        *voice.TTSClient
	mux        *http.ServeMux
}

// New builds the server. cfgPath is kept around so liveConfig can re-read
// config.yaml on demand; staticFS is the embedded SvelteKit build (see
// web/embed.go) — pass nil to run API/WS-only, which is what local dev
// does while `vite dev` serves the frontend and proxies through instead.
func New(cfg *config.Config, cfgPath string, db *store.Store, staticFS fs.FS) *Server {
	s := &Server{
		cfg:        cfg,
		cfgPath:    cfgPath,
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
	s.mux.HandleFunc("PATCH /api/threads/{id}", s.handleRenameThread)
	s.mux.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	s.mux.HandleFunc("GET /api/threads/{id}/events", s.handleThreadEvents)
	s.mux.HandleFunc("GET /api/events", s.handleRecentEvents)
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

// liveConfig re-reads config.yaml from disk before returning it, so
// day-to-day edits — adding a model, raising context_window_tokens —
// take effect on the very next request instead of requiring a restart.
// The file is a few KB of YAML, so re-parsing it per request is cheap
// relative to the LLM call every one of these handlers is either serving
// or about to kick off.
//
// Fields that construct long-lived clients at startup (OpenRouter creds
// baked into s.searxng/s.foursquare/s.stt/s.tts, the listen address) are
// NOT picked up by this — those clients would need to be rebuilt, which
// is what a restart is for. Everything else (models, default_model,
// context_window_tokens, max_agent_turns, default_location, service
// label) is read fresh via this on every request that needs it.
//
// Falls back to the last good config if the file is momentarily
// unreadable or invalid, rather than failing the request outright.
func (s *Server) liveConfig() *config.Config {
	if fresh, err := config.Load(s.cfgPath); err != nil {
		log.Warn("config reload failed, using last known config", "err", err)
		// Not thread-scoped — a bad edit to config.yaml affects every
		// thread going forward, so it belongs in the global event log
		// rather than attached to whichever request happened to trigger it.
		s.db.LogEvent("", "error", "config", "config reload failed, using last known config", map[string]interface{}{"err": err.Error()})
	} else {
		s.cfgMu.Lock()
		s.cfg = fresh
		s.cfgMu.Unlock()
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// effectiveDefaultModel is the settings-panel override if one's been set,
// otherwise cfg's default_model. Settings panel changes take effect
// immediately (no restart); config.yaml is the factory default.
func (s *Server) effectiveDefaultModel(cfg *config.Config) string {
	if v, err := s.db.GetSetting("default_model"); err == nil && v != "" {
		return v
	}
	return cfg.DefaultModel
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
