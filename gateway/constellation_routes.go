// constellation_routes.go is the REST API for Constellation — the
// Library/Inbox/Star-detail/Map screens, review actions, settings, and
// usage stats. See docs/plans/constellation.md's "The UI".
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
)

func (s *Server) handleGetConstellationConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.GetConstellationConfig()
	if err != nil {
		log.Warn("getting constellation config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}

type constellationConfigRequest struct {
	Enabled             bool   `json:"enabled"`
	PollIntervalMinutes int    `json:"poll_interval_minutes"`
	Model               string `json:"model"`
}

func (s *Server) handleUpdateConstellationConfig(w http.ResponseWriter, r *http.Request) {
	var req constellationConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PollIntervalMinutes <= 0 {
		http.Error(w, "poll_interval_minutes must be positive", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateConstellationConfig(req.Enabled, req.PollIntervalMinutes, req.Model); err != nil {
		log.Warn("updating constellation config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cfg, err := s.db.GetConstellationConfig()
	if err != nil {
		log.Warn("loading just-updated constellation config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}

func (s *Server) handleGetConstellationStats(w http.ResponseWriter, r *http.Request) {
	periodDays := 0
	if v := r.URL.Query().Get("period_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "period_days must be a non-negative integer", http.StatusBadRequest)
			return
		}
		periodDays = n
	}
	stats, err := s.db.GetConstellationStats(periodDays)
	if err != nil {
		log.Warn("getting constellation stats failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

// constellationStarFilterFor maps the Library's four sections (see the
// plan doc's "The UI") to the store.StarFilter that produces them —
// personal stars get their own collapsible group ("about_you") separate
// from the plain category sections, and Inbox is every proposed star
// (personal or not) awaiting review, not a per-section queue.
func constellationStarFilterFor(section string) (store.StarFilter, error) {
	switch section {
	case "library":
		notPersonal := false
		return store.StarFilter{Statuses: []string{"auto", "confirmed"}, IsPersonal: &notPersonal}, nil
	case "about_you":
		personal := true
		return store.StarFilter{Statuses: []string{"auto", "confirmed"}, IsPersonal: &personal}, nil
	case "inbox":
		return store.StarFilter{Statuses: []string{"proposed"}}, nil
	case "rejected":
		return store.StarFilter{Statuses: []string{"rejected"}}, nil
	default:
		return store.StarFilter{}, fmt.Errorf("unknown section %q — must be one of library, about_you, inbox, rejected", section)
	}
}

func (s *Server) handleListConstellationStars(w http.ResponseWriter, r *http.Request) {
	section := r.URL.Query().Get("section")
	filter, err := constellationStarFilterFor(section)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stars, err := s.db.ListStars(filter)
	if err != nil {
		log.Warn("listing constellation stars failed", "err", err, "section", section)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stars == nil {
		stars = []store.Star{}
	}
	writeJSON(w, stars)
}

// constellationStarDetail is the Star detail screen's payload — the star
// itself plus its "Linked articles" (Sources) and "Nearby in the
// constellation" (Edges) blocks in one response, since both are always
// read together on that screen.
type constellationStarDetail struct {
	Star    store.Star        `json:"star"`
	Sources []store.StarSource `json:"sources"`
	Edges   []store.StarEdge   `json:"edges"`
}

func (s *Server) handleGetConstellationStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid star id", http.StatusBadRequest)
		return
	}
	star, err := s.db.GetStar(id)
	if err == store.ErrStarNotFound {
		http.Error(w, "star not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("getting constellation star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sources, err := s.db.StarSources(id)
	if err != nil {
		log.Warn("getting star sources failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	edges, err := s.db.StarEdges(id)
	if err != nil {
		log.Warn("getting star edges failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sources == nil {
		sources = []store.StarSource{}
	}
	if edges == nil {
		edges = []store.StarEdge{}
	}
	writeJSON(w, constellationStarDetail{Star: *star, Sources: sources, Edges: edges})
}

// constellationStarPatchRequest covers the star's own overflow menu —
// renaming the title directly (no LLM involved) and Disable, independent
// of status (see the plan doc's "A star's own overflow menu"). Both
// fields are pointers so a PATCH only touches what the caller actually
// sent.
type constellationStarPatchRequest struct {
	Title    *string `json:"title"`
	Disabled *bool   `json:"disabled"`
}

func (s *Server) handlePatchConstellationStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid star id", http.StatusBadRequest)
		return
	}
	var req constellationStarPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title != nil {
		if strings.TrimSpace(*req.Title) == "" {
			http.Error(w, "title cannot be empty", http.StatusBadRequest)
			return
		}
		if err := s.db.RenameStar(id, *req.Title); err != nil {
			log.Warn("renaming star failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Disabled != nil {
		if err := s.db.SetStarDisabled(id, *req.Disabled); err != nil {
			log.Warn("setting star disabled failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	star, err := s.db.GetStar(id)
	if err == store.ErrStarNotFound {
		http.Error(w, "star not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("loading just-patched star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, star)
}

func (s *Server) handleRestoreConstellationStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid star id", http.StatusBadRequest)
		return
	}
	// Restore moves a rejected star straight to 'confirmed', not back to
	// 'proposed' — a human deciding "actually I do want this" is a direct
	// decision, not a new Weaver proposal needing re-review (see the plan
	// doc's "Rejected stars get their own collapsible Library section").
	if err := s.db.SetStarStatus(id, "confirmed"); err != nil {
		log.Warn("restoring star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	star, err := s.db.GetStar(id)
	if err != nil {
		log.Warn("loading just-restored star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, star)
}

// constellationReviewRequest is the Review screen's three-way action bar
// (see the plan doc's "Reviewing a proposed star") — Correction is only
// meaningful for action="refine".
type constellationReviewRequest struct {
	Action     string `json:"action"` // "approve" | "discard" | "refine"
	Correction string `json:"correction"`
}

func (s *Server) handleReviewConstellationStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid star id", http.StatusBadRequest)
		return
	}
	var req constellationReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "approve":
		if err := s.db.SetStarStatus(id, "confirmed"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.RecordStarReview(id, "approved", ""); err != nil {
			log.Warn("recording star review failed", "err", err, "id", id)
		}
	case "discard":
		if err := s.db.SetStarStatus(id, "rejected"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.RecordStarReview(id, "discarded", ""); err != nil {
			log.Warn("recording star review failed", "err", err, "id", id)
		}
	case "refine":
		if strings.TrimSpace(req.Correction) == "" {
			http.Error(w, "correction is required for refine", http.StatusBadRequest)
			return
		}
		if err := s.reconcileAndSaveStar(r.Context(), id, req.Correction); err != nil {
			log.Warn("refining star failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Sending a refinement both corrects the star and resolves the
		// review in one step — there's no separate confirm-after-refine
		// tap (see the plan doc's "Refine").
		if err := s.db.SetStarStatus(id, "confirmed"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.RecordStarReview(id, "refined", req.Correction); err != nil {
			log.Warn("recording star review failed", "err", err, "id", id)
		}
	default:
		http.Error(w, "action must be one of approve, discard, refine", http.StatusBadRequest)
		return
	}

	star, err := s.db.GetStar(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, star)
}

// constellationEditRequest is Edit star's free-text correction sheet — a
// direct, one-off correction to an already-confirmed star, distinct from
// Refine (see the plan doc's "Reviewing and editing a star": Edit star is
// scoped to Inbox review only via star_reviews, so this deliberately does
// not write there).
type constellationEditRequest struct {
	Correction string `json:"correction"`
}

func (s *Server) handleEditConstellationStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid star id", http.StatusBadRequest)
		return
	}
	var req constellationEditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Correction) == "" {
		http.Error(w, "correction is required", http.StatusBadRequest)
		return
	}
	if err := s.reconcileAndSaveStar(r.Context(), id, req.Correction); err != nil {
		log.Warn("editing star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	star, err := s.db.GetStar(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, star)
}

// reconcileAndSaveStar runs the one-off LLM reconciliation pass and
// persists it — shared by Edit star and Refine, which are visually the
// same composer sheet over the same mechanism (see the plan doc).
// is_personal is read from the star's current value and passed through
// unchanged: this path never flips a star between personal/topical, only
// Weaver's own create_star/update_star judgment does that.
func (s *Server) reconcileAndSaveStar(reqCtx context.Context, id int64, correction string) error {
	star, err := s.db.GetStar(id)
	if err != nil {
		return err
	}
	cfgRow, err := s.db.GetConstellationConfig()
	if err != nil {
		return err
	}
	client := WeaverClient(s.liveConfig(), cfgRow.Model)
	summary, body, _, err := reconcileStarContent(reqCtx, client, *star, correction)
	if err != nil {
		return err
	}
	return s.db.UpdateStar(id, summary, body, star.Tags, star.Confidence, star.IsPersonal)
}

// reconcileStarContent is the actual LLM call behind reconcileAndSaveStar
// — a single completion, not a full Weaver agent.Run: no tools, no dedup/
// link judgment, just folding one piece of free-text human input into an
// already-identified star's fields (see prompts.yaml's weaver.reconcile_system).
func reconcileStarContent(reqCtx context.Context, client llm.ChatClient, star store.Star, correction string) (summary, body string, costUSD float64, err error) {
	task := fmt.Sprintf("Current summary: %s\n\nCurrent body:\n%s\n\nWhat they just said: %s", star.Summary, star.Body, correction)
	resp, err := client.ChatCompletionStreaming(reqCtx, []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Weaver.ReconcileSystem},
		{Role: "user", Content: task},
	}, func(string) {}, nil)
	if err != nil {
		return "", "", 0, err
	}
	summary, body = parseReconcileResponse(resp.Content)
	if summary == "" {
		summary = star.Summary
	}
	if body == "" {
		body = star.Body
	}
	return summary, body, resp.CostUSD, nil
}

// parseReconcileResponse pulls SUMMARY:/BODY: out of the model's response
// — see prompts.yaml's weaver.reconcile_system for the exact format asked
// for. Falls back to empty strings (reconcileStarContent then keeps the
// star's existing values) if the model didn't follow the format, rather
// than erroring the whole request out over a formatting slip.
func parseReconcileResponse(content string) (summary, body string) {
	lines := strings.Split(content, "\n")
	var bodyLines []string
	inBody := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "SUMMARY:"):
			summary = strings.TrimSpace(strings.TrimPrefix(line, "SUMMARY:"))
		case strings.HasPrefix(line, "BODY:"):
			inBody = true
			bodyLines = append(bodyLines, strings.TrimSpace(strings.TrimPrefix(line, "BODY:")))
		case inBody:
			bodyLines = append(bodyLines, line)
		}
	}
	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return summary, body
}

// constellationDigest is the Library's digest banner — pure counts plus
// one concrete example, zero LLM calls (see the plan doc's "The weekly
// digest"). Show is false when both counts are zero: "no signal, no
// output" is the same instinct the poller itself runs on.
type constellationDigest struct {
	NewCount   int    `json:"new_count"`
	LinksCount int    `json:"links_count"`
	Highlight  string `json:"highlight"`
	Show       bool   `json:"show"`
}

func (s *Server) handleGetConstellationDigest(w http.ResponseWriter, r *http.Request) {
	digest, err := s.db.GetConstellationDigest()
	if err != nil {
		log.Warn("getting constellation digest failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, constellationDigest{
		NewCount: digest.NewCount, LinksCount: digest.LinksCount,
		Highlight: digest.Highlight, Show: digest.NewCount > 0 || digest.LinksCount > 0,
	})
}

func (s *Server) handleGetConstellationWeek(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.GetConstellationWeekFeed()
	if err != nil {
		log.Warn("getting constellation week feed failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []store.ConstellationWeekItem{}
	}
	writeJSON(w, items)
}

// constellationMap is the full star-map view's payload — every non-
// disabled star plus every edge between them (see the plan doc's "Map").
type constellationMap struct {
	Stars []store.Star         `json:"stars"`
	Edges []store.StarEdgePair `json:"edges"`
}

// handleConstellationBackfill is the Docker-mode target for
// `polaris constellation backfill` — under Docker the CLI binary outside
// the container has no access to the container's polaris.db (it lives in
// a named Docker volume, not a plain host file — see CLAUDE.md), so it
// proxies here instead of running BackfillConstellation itself the way
// the bare-metal CLI path does directly. Synchronous: a backfill is a
// one-time admin operation, not something needing progress streaming, and
// cmd/constellation_backfill.go's Docker-mode HTTP client uses a generous
// timeout to match.
func (s *Server) handleConstellationBackfill(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		limit = n
	}
	cfgRow, err := s.db.GetConstellationConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	client := WeaverClient(s.liveConfig(), cfgRow.Model)
	processed, err := BackfillConstellation(r.Context(), s.db, client, limit)
	if err != nil {
		log.Warn("constellation backfill failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"processed": processed})
}

func (s *Server) handleGetConstellationMap(w http.ResponseWriter, r *http.Request) {
	stars, err := s.db.ListStars(store.StarFilter{Statuses: []string{"auto", "confirmed"}})
	if err != nil {
		log.Warn("listing stars for constellation map failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	edges, err := s.db.AllStarEdges()
	if err != nil {
		log.Warn("listing edges for constellation map failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stars == nil {
		stars = []store.Star{}
	}
	if edges == nil {
		edges = []store.StarEdgePair{}
	}
	writeJSON(w, constellationMap{Stars: stars, Edges: edges})
}
