package gateway

import (
	"net/http"
	"strconv"
	"time"

	"polaris/brave"
	"polaris/store"
	"polaris/tools"
)

// StatsResponse is /api/stats' body: everything store.GetStats reports,
// flattened at the top level exactly as before (so existing consumers of
// store.Stats — the settings panel, older CLIs — keep decoding it
// unchanged), plus the paid-API monthly caps section.
type StatsResponse struct {
	*store.Stats
	APICaps APICaps `json:"api_caps"`
}

// APICaps is where every hard monthly ceiling stands. These are calendar-
// month counters (UTC, via SQLite's strftime), unlike the rest of Stats,
// which is scoped by ?days — ?days has no effect here.
type APICaps struct {
	Month    string       `json:"month"`     // "2026-10"; the counters below restart when this changes
	ResetsAt string       `json:"resets_at"` // RFC 3339, start of next month UTC
	Services []APICapItem `json:"services"`
}

// APICapItem is one capped service. Unit is "calls" or "usd". Used counts
// what the enforcing code counts: Tavily's counter is calls, not credits,
// so an advanced Extract (2 credits) still adds 1 here.
type APICapItem struct {
	Provider    string  `json:"provider"`
	Unit        string  `json:"unit"`
	Used        float64 `json:"used"`
	Cap         float64 `json:"cap"`
	Remaining   float64 `json:"remaining"`
	PercentUsed float64 `json:"percent_used"`
}

// buildAPICaps reads each service's month-to-date usage from the same
// ledgers the enforcing code checks (api_usage for call-counted services,
// jev_usage for Jev's dollar cap), so this can't disagree with what will
// actually be allowed. now is a parameter so tests can pin the month.
func buildAPICaps(db *store.Store, now time.Time) (APICaps, error) {
	now = now.UTC()
	caps := APICaps{
		Month:    now.Format("2006-01"),
		ResetsAt: time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}

	calls := []struct {
		provider string
		cap      int
	}{
		{"brave", brave.MonthlyCap},
		{"parallel", tools.ParallelMonthlyCap},
		{"tavily", tools.TavilyMonthlyCap},
	}
	for _, c := range calls {
		used, err := db.GetAPIUsage(c.provider)
		if err != nil {
			return APICaps{}, err
		}
		caps.Services = append(caps.Services, newAPICapItem(c.provider, "calls", float64(used), float64(c.cap)))
	}

	jevSpend, err := db.JevCostThisMonth()
	if err != nil {
		return APICaps{}, err
	}
	caps.Services = append(caps.Services, newAPICapItem("jev", "usd", jevSpend, jevMonthlyCapUSD))
	return caps, nil
}

func newAPICapItem(provider, unit string, used, cap float64) APICapItem {
	remaining := cap - used
	if remaining < 0 {
		remaining = 0
	}
	pct := 0.0
	if cap > 0 {
		pct = used / cap * 100
	}
	return APICapItem{Provider: provider, Unit: unit, Used: used, Cap: cap, Remaining: remaining, PercentUsed: pct}
}

// handleStats returns the usage/tuning summary from store.Store.GetStats
// — ?days=N scopes the period-sensitive fields to the trailing N days
// (default 30); days<=0 means all time — plus the paid-API monthly caps
// (see APICaps). Backs `polaris stats`, this endpoint itself, and the
// settings panel's small Usage section.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	stats, err := s.db.GetStats(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	caps, err := buildAPICaps(s.db, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, StatsResponse{Stats: stats, APICaps: caps})
}
