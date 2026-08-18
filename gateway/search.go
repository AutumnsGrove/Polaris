package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"polaris/brave"
	"polaris/search"
)

// braveVirtualPageSize is how many results Atlas shows per page when
// falling back to Brave — half of Brave's own maxCount-per-request (20),
// so one real Brave fetch covers two Atlas pages before a second real
// request is needed. This is purely a display-side split, not something
// Brave's API itself knows about: braveFallbackSearch below maps an
// Atlas page number to a (real Brave offset, half) pair and slices the
// one real fetch accordingly.
const braveVirtualPageSize = 10

// handleSearch backs Atlas's results page — a thin wrapper around the same
// search.SearXNGClient the web_search agent tool uses (see search.go's
// Server.searxng field), so domain ranking and the blocklist apply
// identically whether a query comes from a person typing into Atlas or the
// assistant calling web_search on their behalf.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	category := r.URL.Query().Get("category")

	// 20, not the old 8 — SearXNG's own single-page response for a
	// general query routinely already contains 20-30+ merged results
	// (use_default_settings: true pulls in dozens of engines); the old
	// cap was throwing away most of what SearXNG had already fetched,
	// not actually limiting how much work anything did.
	maxResults := 20
	if raw := r.URL.Query().Get("max_results"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxResults = n
		}
	}

	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}

	resp, err := s.searxng.Search(r.Context(), query, maxResults, category, page)
	if err != nil {
		log.Warn("atlas search: searxng request failed, trying brave fallback", "query", query, "category", category, "err", err)
		if braveResp, ok := s.braveFallbackSearch(r.Context(), query, page); ok {
			resp = braveResp
		} else {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else if len(resp.Results) == 0 && resp.Degraded {
		// Same degraded-vs-genuinely-empty distinction web_search's own
		// fallback chain makes (see tools/web_search.go) — SearXNG's own
		// engines are down, not "this query has no results". If Brave
		// can't rescue it either, fall through and hand the frontend the
		// degraded resp as-is (empty results + Degraded/UnresponsiveEngines
		// set) so it can show its own "search is degraded" state.
		log.Warn("atlas search: searxng degraded, trying brave fallback", "query", query, "unresponsive_engines", resp.UnresponsiveEngines)
		if braveResp, ok := s.braveFallbackSearch(r.Context(), query, page); ok {
			resp = braveResp
		}
	}

	// record=0 is set by search.svelte.ts when the query came from
	// clicking an existing sidebar history entry, not a fresh search —
	// re-running the same query to redisplay it shouldn't bump it back to
	// the top of that same list. Default (missing/anything else) is to
	// record, same as every other query parameter here degrading
	// gracefully rather than erroring on an unexpected value.
	if r.URL.Query().Get("record") != "0" {
		// Best-effort, off the response path: a search that succeeded is
		// worth showing even if recording it to history fails for some
		// reason (a locked/corrupt DB shouldn't take Atlas itself down).
		if err := s.db.RecordSearch(query); err != nil {
			log.Warn("recording search history failed", "query", query, "err", err)
		}
	}

	writeJSON(w, resp)
}

// braveFallbackSearch tries Brave once SearXNG itself has failed or
// confirmed degraded (see handleSearch). page is Atlas's own page
// number (1-indexed); mapped to a (real Brave offset, half) pair so one
// real Brave fetch (maxCount=20, see brave.Client.Search) covers two
// Atlas pages of braveVirtualPageSize each — page 1-2 come from Brave
// offset 0, page 3-4 from offset 1, and so on. Returns ok=false on any
// failure (not configured, over the monthly cap, or the request itself
// erroring) so the caller falls through to the plain degraded response.
func (s *Server) braveFallbackSearch(ctx context.Context, query string, page int) (*search.SearchResponse, bool) {
	if s.brave == nil {
		return nil, false
	}
	used, err := s.db.GetAPIUsage("brave")
	if err != nil {
		log.Warn("atlas search: checking brave usage failed, skipping brave fallback", "query", query, "err", err)
		return nil, false
	}
	if used >= brave.MonthlyCap {
		log.Warn("atlas search: brave monthly cap reached, skipping brave fallback", "query", query, "used", used, "cap", brave.MonthlyCap)
		return nil, false
	}

	if page < 1 {
		page = 1
	}
	realOffset := (page - 1) / 2
	half := (page - 1) % 2

	resp, err := s.brave.Search(ctx, query, realOffset)
	if err != nil {
		log.Warn("atlas search: brave fallback request failed", "query", query, "err", err)
		return nil, false
	}
	if _, incErr := s.db.IncrementAPIUsage("brave"); incErr != nil {
		log.Warn("atlas search: recording brave usage failed", "query", query, "err", incErr)
	}

	start := half * braveVirtualPageSize
	if start > len(resp.Results) {
		start = len(resp.Results)
	}
	end := start + braveVirtualPageSize
	if end > len(resp.Results) {
		end = len(resp.Results)
	}

	results := make([]search.SearchResult, end-start)
	for i, r := range resp.Results[start:end] {
		results[i] = search.SearchResult{Title: r.Title, URL: r.URL, Content: r.Content, Engine: "brave"}
	}

	// hasMore: the first half of a real fetch has more available the
	// instant the second half exists in the same response (no extra
	// request needed to know that); the second half only has more if
	// Brave itself says another real page exists beyond this one.
	hasMore := false
	if half == 0 {
		hasMore = len(resp.Results) > braveVirtualPageSize
	} else {
		hasMore = resp.MoreAvailable
	}

	return &search.SearchResponse{Query: query, Results: results, Page: page, HasMore: hasMore}, true
}

// handleListSearchHistory backs Atlas's sidebar — same shape as
// handleListThreads for the chat sidebar.
func (s *Server) handleListSearchHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.db.ListSearchHistory(100)
	if err != nil {
		log.Warn("listing search history failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

// handleUpdateSearchHistory toggles a search entry's Favorites-section
// membership — the search-history analog of handleUpdateThread's favorite
// field, minus rename (a past query has no separate title to edit).
func (s *Server) handleUpdateSearchHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req struct {
		Favorite *bool `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Favorite != nil {
		if err := s.db.SetSearchHistoryFavorite(id, *req.Favorite); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "search history entry not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleSetDomainRanking backs the ranking popover's Block/Lower/Default/
// Raise/Pin control — writes through to whatever file s.searxng itself
// reads (see SearXNGClient.DomainRankingsPath), so a change here is live
// on the very next search with no restart, and applies identically to
// Atlas and the assistant's web_search tool since they share that client.
func (s *Server) handleSetDomainRanking(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		State  string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}

	path := s.searxng.DomainRankingsPath()
	if path == "" {
		http.Error(w, "domain rankings are not configured", http.StatusServiceUnavailable)
		return
	}

	if err := search.SetDomainRanking(path, domain, search.RankState(req.State)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
