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
	"context"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"polaris/brave"
	"polaris/config"
	"polaris/embed"
	"polaris/logger"
	"polaris/models"
	"polaris/parallel"
	"polaris/places"
	"polaris/search"
	"polaris/store"
	"polaris/tavily"
	"polaris/voice"
)

var log = logger.WithPrefix("gateway")

type Server struct {
	// cfg is the last config.yaml load, refreshed on demand by liveConfig
	// rather than read once at startup — see liveConfig for why.
	cfg     *config.Config
	cfgPath string
	cfgMu   sync.RWMutex

	// version is the build-injected version (main.Version, via -ldflags
	// -X, threaded through from main.go/cmd) — empty for a plain local
	// `go build` with no ldflags, in which case handleVersion falls back
	// to computeVersion's git shell-out (see version.go). A Docker image
	// has no .git directory (see .dockerignore) and no reliable git
	// binary, so that shell-out would just fail there — this is what
	// lets /api/version report something real under Docker instead of
	// silently pinning at "dev" forever, which would break the settings
	// panel's "did the update actually land" reload check.
	version string

	db         *store.Store
	searxng    *search.SearXNGClient
	blocklist  *search.Blocklist
	foursquare *places.FoursquareClient // nil if not configured
	tavily     *tavily.Client           // nil if not configured
	brave      *brave.Client            // nil if not configured
	parallel   *parallel.Client         // nil if not configured
	embed      *embed.Client            // nil if ollama.base_url isn't set — disables agent's query-similarity signal only
	stt        *voice.STTClient
	tts        *voice.TTSClient
	mux        *http.ServeMux

	// updateStatus tracks the one self-update that can run at a time —
	// see its doc comment in update.go for why this needs to survive
	// past the single request that triggered it.
	updateStatus updateStatus

	// shuttingDown/activeTurns let cmd/run.go drain in-flight agent turns
	// before the process actually exits on SIGTERM — see BeginShutdown's
	// doc comment for why this matters specifically for the self-update
	// restart path. Both are guarded by turnsMu, not left as a bare
	// atomic, because "check shuttingDown, then Add(1)" (TryStartTurn)
	// must never interleave with BeginShutdown flipping the flag: without
	// a shared lock, a turn could Add(1) after WaitForActiveTurns has
	// already observed the counter at zero and returned — sync.WaitGroup
	// explicitly documents that as a misuse that can panic ("Add ... must
	// happen before Wait").
	turnsMu      sync.Mutex
	shuttingDown bool
	activeTurns  sync.WaitGroup

	// turnSends lets AbortActiveTurns below reach every in-flight turn's
	// WebSocket the instant WaitForActiveTurns gives up on them, instead of
	// those turns just vanishing when this process exits moments later —
	// see AbortActiveTurns' doc comment for why a silent kill here is
	// exactly the "bumped back to an old conversation" symptom TryStartTurn's
	// doc comment describes. Keyed by an opaque handle (not thread ID —
	// a brand-new thread's ID may not be known yet) so registerTurnSend's
	// caller can deregister the exact entry it added, not any entry that
	// happens to share a thread ID.
	sendsMu    sync.Mutex
	turnSends  map[int64]func(ServerEvent)
	nextSendID int64
}

// New builds the server. cfgPath is kept around so liveConfig can re-read
// config.yaml on demand; staticFS is the embedded SvelteKit build (see
// web/embed.go) — pass nil to run API/WS-only, which is what local dev
// does while `vite dev` serves the frontend and proxies through instead.
// version is main.Version, or "" if the caller has none to give (tests,
// mainly) — see the Server.version field's doc comment.
func New(cfg *config.Config, cfgPath string, db *store.Store, staticFS fs.FS, version string) *Server {
	blocklist, err := search.LoadBlocklist(cfg.BlockedSourcesFile)
	if err != nil {
		log.Warn("loading source blocklist failed, continuing with no blocked sources", "path", cfg.BlockedSourcesFile, "err", err)
		blocklist = nil
	}

	s := &Server{
		cfg:        cfg,
		cfgPath:    cfgPath,
		version:    version,
		db:         db,
		searxng:    search.NewSearXNGClient(cfg.SearXNG.BaseURL, blocklist).WithDomainRankings(cfg.DomainRankingsFile),
		blocklist:  blocklist,
		foursquare: places.NewFoursquareClient(cfg.Foursquare.APIKey),
		tavily:     tavily.NewClient(cfg.Tavily.APIKey),
		brave:      brave.NewClient(cfg.Brave.APIKey),
		parallel:   parallel.NewClient(cfg.Parallel.APIKey),
		embed:      embed.NewClient(cfg.Ollama.BaseURL, cfg.Ollama.EmbedModel),
		stt:        voice.NewSTTClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, cfg.Voice.STTModel, cfg.Voice.STTFallbackModel),
		tts:        voice.NewTTSClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, cfg.Voice.TTSModel, cfg.Voice.TTSVoice, cfg.Voice.TTSFormat, cfg.Voice.TTSProvider),
		mux:        http.NewServeMux(),
		turnSends:  make(map[int64]func(ServerEvent)),
	}
	s.routes(staticFS)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// TryStartTurn registers a new turn goroutine with the server's shutdown
// tracking, returning false if a shutdown is already underway (see
// BeginShutdown) — the caller (handleWS) must not start the turn at all
// in that case. On true, the caller must call FinishTurn exactly once
// when the turn goroutine returns.
//
// This exists because handleTurn's goroutine deliberately runs on a
// context derived from context.Background(), not the request/connection
// that started it (see ws.go's turnCtx comment) — a dropped WebSocket was
// never meant to interrupt an in-flight turn, and neither should a
// self-update's restart. Without this, `systemctl restart` (see
// procmgr/systemd.go) sends SIGTERM and the Go runtime's default
// disposition kills the process immediately, mid-DB-write, for whatever
// turn happens to be running at that instant. store.CreateThread/
// AddMessage are bare, non-transactional Execs — a kill between "mint a
// new thread id" and "the INSERT actually committing" leaves the thread
// simply not existing, so the next request (a reconnect fetching that
// thread, or a sidebar reload) legitimately falls back to whatever was
// the previous newest thread — indistinguishable from "the app bumped me
// back to an old conversation."
//
// The check-then-Add happens under turnsMu, not as two separate steps on
// a bare atomic, specifically so it can never interleave with
// BeginShutdown/WaitForActiveTurns — see turnsMu's doc comment on the
// struct field for the WaitGroup misuse that would otherwise risk.
func (s *Server) TryStartTurn() bool {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.activeTurns.Add(1)
	return true
}

// FinishTurn marks one turn (started via a successful TryStartTurn) as
// done — always call via defer right after a successful TryStartTurn.
func (s *Server) FinishTurn() {
	s.activeTurns.Done()
}

// BeginShutdown marks the server as shutting down: TryStartTurn starts
// rejecting brand-new turns from this point on, so nothing new gets
// started that's only going to be killed mid-flight moments later. It
// does NOT touch turns already running — those are exactly what
// WaitForActiveTurns below waits on.
func (s *Server) BeginShutdown() {
	s.turnsMu.Lock()
	s.shuttingDown = true
	s.turnsMu.Unlock()
}

// WaitForActiveTurns blocks until every turn goroutine started before
// BeginShutdown finishes, or ctx is done — whichever comes first. A
// bounded wait, not an unconditional one: a deep-research turn can
// legitimately run for minutes, and the point of a self-update restart is
// to actually happen, not to be held hostage by whichever turn happened
// to be in flight. The caller (cmd/run.go) picks the bound and just
// proceeds to exit either way once this returns — a turn still running
// past the deadline gets killed exactly as it would have without any of
// this, just less often in practice. Safe to call only after
// BeginShutdown, per turnsMu's ordering guarantee above.
func (s *Server) WaitForActiveTurns(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.activeTurns.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// registerTurnSend records a turn's WebSocket send func so AbortActiveTurns
// can reach it if WaitForActiveTurns gives up before it finishes. Call
// right after a successful TryStartTurn, alongside it (not inside
// handleTurn itself) so the registration window exactly matches the
// activeTurns tracking window. Returns a handle for unregisterTurnSend.
func (s *Server) registerTurnSend(send func(ServerEvent)) int64 {
	s.sendsMu.Lock()
	defer s.sendsMu.Unlock()
	id := s.nextSendID
	s.nextSendID++
	s.turnSends[id] = send
	return id
}

// unregisterTurnSend removes a turn's send func once it finishes on its
// own — always call via defer right after registerTurnSend, mirroring
// FinishTurn. Without this, a long-lived server would leak one map entry
// per turn ever started.
func (s *Server) unregisterTurnSend(id int64) {
	s.sendsMu.Lock()
	defer s.sendsMu.Unlock()
	delete(s.turnSends, id)
}

// AbortActiveTurns tells every turn still registered (i.e. still running
// after WaitForActiveTurns' deadline expired) that it's being cut off,
// before cmd/run.go actually exits the process. Without this, a turn that
// legitimately outran the drain deadline — the common case for a
// multi-step research thread doing several sequential web_search/web_read
// calls, confirmed live: a synthetic turn was cut off mid-tool-call after
// exactly shutdownGrace's ~25s budget — just stops dead with a bare TCP
// close. The frontend never receives a 'done' or 'error' for it, so
// AppState.busy never clears normally and the thread this turn belonged to
// never gets adopted as currentThreadId (see state.svelte.ts's 'done'
// handler) — from the user's seat, they typed a new message, watched it
// stream for a while, and then the app is just back on whatever thread was
// open before, with no explanation. Sending an explicit error here turns
// that into a normal, visible failure the user can see and retry, same as
// any other dropped turn.
func (s *Server) AbortActiveTurns(message string) {
	s.sendsMu.Lock()
	sends := make([]func(ServerEvent), 0, len(s.turnSends))
	for _, send := range s.turnSends {
		sends = append(sends, send)
	}
	s.sendsMu.Unlock()
	for _, send := range sends {
		// ThreadID intentionally left empty: a still-forming brand-new
		// thread's real ID may not be known to the client's pendingThreadId
		// yet, and handleEvent's routing gate only filters on a *non-empty*
		// thread_id (see state.svelte.ts), so an empty one reaches whichever
		// turn is currently pending regardless of which thread it's for.
		send(ServerEvent{Type: "error", Message: message})
	}
}

func (s *Server) routes(staticFS fs.FS) {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /api/models", s.handleModels)
	s.mux.HandleFunc("GET /api/threads", s.handleListThreads)
	s.mux.HandleFunc("GET /api/threads/search", s.handleSearchThreads)
	s.mux.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	s.mux.HandleFunc("PATCH /api/threads/{id}", s.handleUpdateThread)
	s.mux.HandleFunc("POST /api/threads/{id}/regenerate-title", s.handleRegenerateTitle)
	s.mux.HandleFunc("POST /api/threads/{id}/variant", s.handleSwapVariant)
	s.mux.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	s.mux.HandleFunc("GET /api/threads/{id}/events", s.handleThreadEvents)
	s.mux.HandleFunc("GET /api/events", s.handleRecentEvents)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("POST /api/transcribe", s.handleTranscribe)
	s.mux.HandleFunc("POST /api/upload", s.handleUpload)
	s.mux.HandleFunc("POST /api/speak", s.handleSpeak)
	s.mux.HandleFunc("POST /api/speak/stream", s.handleSpeakStream)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	s.mux.HandleFunc("POST /api/update", s.handleUpdate)
	s.mux.HandleFunc("POST /api/restart", s.handleRestart)
	s.mux.HandleFunc("GET /api/update/status", s.handleUpdateStatus)
	s.mux.HandleFunc("POST /api/ask", s.handleAsk)
	s.mux.HandleFunc("POST /api/ask/stream", s.handleAskStream)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/search-history", s.handleListSearchHistory)
	s.mux.HandleFunc("PATCH /api/search-history/{id}", s.handleUpdateSearchHistory)
	s.mux.HandleFunc("PUT /api/domain-rankings", s.handleSetDomainRanking)
	s.mux.HandleFunc("POST /api/debug-log", s.handleDebugLog)
	s.mux.HandleFunc("GET /api/backup", s.handleListBackups)
	s.mux.HandleFunc("POST /api/backup", s.handleCreateBackup)
	s.mux.HandleFunc("GET /api/memories", s.handleListMemories)
	s.mux.HandleFunc("PATCH /api/memories/{name}", s.handleUpdateMemory)
	s.mux.HandleFunc("DELETE /api/memories/{name}", s.handleDeleteMemory)
	s.mux.HandleFunc("POST /api/memories/chat", s.handleMemoryChat)
	s.mux.HandleFunc("GET /api/pulsar/routines", s.handleListPulsarRoutines)
	s.mux.HandleFunc("POST /api/pulsar/routines", s.handleCreatePulsarRoutine)
	s.mux.HandleFunc("PATCH /api/pulsar/routines/{id}", s.handleUpdatePulsarRoutine)
	s.mux.HandleFunc("POST /api/pulsar/routines/{id}/archive", s.handleArchivePulsarRoutine)
	s.mux.HandleFunc("POST /api/pulsar/routines/{id}/unarchive", s.handleUnarchivePulsarRoutine)
	s.mux.HandleFunc("GET /api/pulsar/routines/{id}/pulses", s.handleListPulsarPulses)
	s.mux.HandleFunc("GET /api/pulsar/unread", s.handlePulsarUnreadCounts)
	s.mux.HandleFunc("GET /ws", s.handleWS)

	if staticFS != nil {
		s.mux.Handle("/", spaHandler(staticFS))
	}
}

// liveConfig re-reads config.yaml from disk before returning it, so
// day-to-day edits — raising context_window_tokens, tuning model
// settings — take effect on the very next request instead of requiring
// a restart. The file is a few KB of YAML, so re-parsing it per request
// is cheap relative to the LLM call every one of these handlers is
// either serving or about to kick off.
//
// Fields that construct long-lived clients at startup (OpenRouter creds
// baked into s.searxng/s.foursquare/s.stt/s.tts, the listen address) are
// NOT picked up by this — those clients would need to be rebuilt, which
// is what a restart is for. Everything else (model overrides,
// default_model, context_window_tokens, max_agent_turns,
// default_location, service label) is read fresh via this on every
// request that needs it.
//
// Falls back to the last good config if the file is momentarily
// unreadable or invalid, rather than failing the request outright.
func (s *Server) liveConfig() *config.Config {
	if fresh, err := config.Load(s.cfgPath, models.Registry); err != nil {
		log.Warn("config reload failed, using last known config", "err", err)
		// Not thread-scoped — a bad edit to config.yaml affects every
		// thread going forward, so it belongs in the global event log
		// rather than attached to whichever request happened to trigger it.
		s.db.LogEvent("", "error", "config", "config reload failed, using last known config", map[string]interface{}{"err": err.Error()}, "")
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

		if _, err := fs.Stat(staticFS, path[1:]); err != nil {
			// A path under /_app/ is a build artifact (content-hashed immutable
			// assets, version.json, ...), never a client route — so there's no
			// SPA fallback for it. adapter-static falls back to index.html for
			// client-side routing only (/t/<id>, /), but a *missing asset*
			// means this URL belonged to an older build this binary no longer
			// ships, which serving index.html there would silently paper over:
			// a stale pre-self-update client (an old index.html + its cached
			// hashed files, or a browser holding an old session) would fetch an
			// HTML document at a .js/.json path and keep running old code
			// instead of failing and reloading onto the current build. A real
			// 404 makes that imported file fail loudly, so the next full load
			// naturally pulls the current index.html + current hashes.
			if strings.HasPrefix(path, "/_app/") {
				w.Header().Set("Cache-Control", "no-store")
				http.NotFound(w, r)
				return
			}
			serveIndexHTML(w, staticFS)
			return
		}

		if strings.HasPrefix(path, "/_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndexHTML writes build/index.html's bytes directly rather than
// routing through http.FileServer with a rewritten path. FileServer treats
// any request whose URL.Path ends in "/index.html" as needing a canonical
// redirect that strips the suffix (Location: "./", so /index.html and /
// never both serve identical content as two separate URLs) — for the
// original request's path itself, not the rewritten one, so the browser
// resolves that redirect against wherever it actually navigated (e.g.
// "/t/<uuid>" → "/t/"). That's still not a real file, so this same
// fallback branch fires again, and again: an infinite redirect loop the
// browser eventually gives up on ("too many redirects"). Every client
// route besides "/" hits this fallback, so it has to avoid FileServer's
// redirect behavior entirely rather than just work around it once.
func serveIndexHTML(w http.ResponseWriter, staticFS fs.FS) {
	f, err := staticFS.Open("index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.Copy(w, f)
}
