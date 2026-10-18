// comet_routes.go is the REST API for Comet — reading/editing the
// singleton config, fetching an edition, listing periods for edition
// navigation, and the manual classify/generate triggers. See
// docs/plans/comet.md.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"polaris/store"
	"polaris/tz"
)

// cometConfigResponse is what the settings panel and the page's masthead
// read — the config row plus, when available, the latest edition's period
// so the page can deep-link without a second request.
type cometConfigResponse struct {
	Config *store.CometConfig `json:"config"`
	// CurrentPeriod is the period the scheduler would generate right now
	// (the most recent complete month), regardless of whether an edition
	// exists for it yet.
	CurrentPeriod string `json:"current_period"`
}

func (s *Server) handleGetCometConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.GetCometConfig()
	if err != nil {
		log.Warn("getting comet config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cometConfigResponse{Config: cfg, CurrentPeriod: cometPeriodOf(tz.Now())})
}

// validCometPeriod reports whether p is a well-formed "YYYY-MM" period.
func validCometPeriod(p string) bool {
	_, _, ok := cometPeriodBounds(p)
	return ok
}

// claimCometRun takes the same single-flight flag the scheduler's pipeline and
// the "Generate now" button use, for the two blocking admin endpoints below.
// Holding it for the whole classify/backfill run is what makes
// handleServerBusy report busy for it — the Docker update watcher would
// otherwise recreate the container between Approach batches (or mid-
// generation during a backfill, which no per-Approach marker covers) — and
// stops a backfill racing the scheduler over the same period. Callers must
// defer the returned release func.
func (s *Server) claimCometRun() (release func(), ok bool) {
	if !s.cometGenerationRunning.CompareAndSwap(false, true) {
		return nil, false
	}
	return func() { s.cometGenerationRunning.Store(false) }, true
}

type cometConfigRequest struct {
	Enabled   bool   `json:"enabled"`
	TimeOfDay string `json:"time_of_day"`
	Model     string `json:"model"`
}

func (s *Server) handleUpdateCometConfig(w http.ResponseWriter, r *http.Request) {
	var req cometConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if _, _, ok := parseTimeOfDay(req.TimeOfDay); !ok {
		http.Error(w, `time_of_day must be "HH:MM" (24-hour, server-local)`, http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateCometConfig(req.Enabled, req.TimeOfDay, strings.TrimSpace(req.Model)); err != nil {
		log.Warn("updating comet config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "comet", "config updated", map[string]interface{}{"enabled": req.Enabled, "time_of_day": req.TimeOfDay}, "")

	cfg, err := s.db.GetCometConfig()
	if err != nil {
		log.Warn("loading just-updated comet config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cometConfigResponse{Config: cfg, CurrentPeriod: cometPeriodOf(tz.Now())})
}

// handleListCometPeriods backs the edition-nav arrows: every period that
// has an edition, newest first, plus the raw periods so the frontend can
// disable "next" when there's nothing newer.
func (s *Server) handleListCometPeriods(w http.ResponseWriter, r *http.Request) {
	periods, err := s.db.ListCometPeriods()
	if err != nil {
		log.Warn("listing comet periods failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if periods == nil {
		periods = []string{}
	}
	writeJSON(w, map[string]interface{}{"periods": periods, "current_period": cometPeriodOf(tz.Now())})
}

// handleGetCometEdition serves one period's edition, or the literal
// "latest" for the most recent one — same shape as the Daily edition
// endpoint's own latest handling.
func (s *Server) handleGetCometEdition(w http.ResponseWriter, r *http.Request) {
	period := r.PathValue("period")
	var edition *store.CometEdition
	var err error
	if period == "latest" {
		edition, err = s.db.LatestCometEdition()
	} else {
		edition, err = s.db.GetCometEdition(period)
	}
	if errors.Is(err, store.ErrCometEditionNotFound) {
		http.Error(w, "no comet edition found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("getting comet edition failed", "period", period, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, edition)
}

// handleCometClassify is the manual `polaris comet classify` path — runs
// Approaches over the requested period's eligible threads and returns how
// many were processed. Like the Constellation backfill endpoint, this
// blocks for the whole run (the CLI uses a generous HTTP timeout); -n/-limit
// exists specifically so a small test run returns quickly.
func (s *Server) handleCometClassify(w http.ResponseWriter, r *http.Request) {
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = cometPeriodOf(tz.Now())
	}
	if !validCometPeriod(period) {
		http.Error(w, "invalid period — expected YYYY-MM", http.StatusBadRequest)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		limit = n
	}
	release, ok := s.claimCometRun()
	if !ok {
		http.Error(w, "a comet run is already in progress", http.StatusConflict)
		return
	}
	defer release()
	processed, err := s.RunCometClassify(period, limit)
	if errors.Is(err, store.ErrCometClassifyAlreadyRunning) {
		http.Error(w, "a comet classify pass is already running", http.StatusConflict)
		return
	}
	if err != nil {
		log.Warn("comet classify failed", "period", period, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"processed": processed, "period": period})
}

// handleCometBackfill runs Comet month by month over history — the one-time
// catch-up for a database that predates Comet, or a copied production DB
// being tested locally. Blocks for the whole run (the CLI uses a generous
// timeout), same shape as the Constellation backfill endpoint.
func (s *Server) handleCometBackfill(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	for _, p := range []string{from, to} {
		if p != "" && !validCometPeriod(p) {
			http.Error(w, "from/to must be YYYY-MM periods", http.StatusBadRequest)
			return
		}
	}
	if from != "" && to != "" && from > to {
		http.Error(w, "from is after to", http.StatusBadRequest)
		return
	}
	release, ok := s.claimCometRun()
	if !ok {
		http.Error(w, "a comet run is already in progress", http.StatusConflict)
		return
	}
	defer release()
	results, err := s.BackfillComet(r.Context(), from, to)
	if errors.Is(err, store.ErrCometClassifyAlreadyRunning) {
		http.Error(w, "a comet classify pass is already running", http.StatusConflict)
		return
	}
	if err != nil {
		log.Warn("comet backfill failed", "from", from, "to", to, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"results": results})
}

// handleGenerateCometNow is the manual "Generate now" trigger, and the
// backing endpoint for `polaris comet generate`. Like Daily's, it returns
// 202 immediately rather than blocking — a full classify+generate can run
// for minutes, and the frontend already has a poll signal (the edition
// appearing / last_generated_at changing).
func (s *Server) handleGenerateCometNow(w http.ResponseWriter, r *http.Request) {
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = cometPeriodOf(tz.Now())
	}
	if !validCometPeriod(period) {
		http.Error(w, "invalid period — expected YYYY-MM", http.StatusBadRequest)
		return
	}
	if !s.startCometGenerationIfIdle(period) {
		http.Error(w, "a comet generation is already running", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"status": "started", "period": period})
}
