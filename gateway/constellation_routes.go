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
	"time"

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
	if section == "inbox" && len(stars) > 0 {
		// Inbox cards show Weaver's own "why" instead of the drafted
		// summary (StarCard's reason prop) — the same reasoning text the
		// Review screen's "Why this needs a look" block already surfaces,
		// just at the list level too, one bulk query instead of N.
		ids := make([]int64, len(stars))
		for i, s := range stars {
			ids[i] = s.ID
		}
		reasoning, err := s.db.LatestCandidateReasoningBulk(ids)
		if err != nil {
			log.Warn("bulk-loading inbox reasoning failed", "err", err)
		} else {
			for i := range stars {
				stars[i].Reasoning = reasoning[stars[i].ID]
			}
		}
	}
	writeJSON(w, stars)
}

// handleConstellationBusy backs the Docker update watcher's pre-restart
// wait (compose/watcher/update.sh) — an unauthenticated read of whether a
// shooting-star run is currently mid-flight, polled from the host over the
// container's own already-exposed port rather than needing DB access from
// outside the container (see issue #57). Deliberately scoped to
// Constellation's own runs only for now, not a general "is anything busy"
// check — see the issue's open questions on whether in-flight chat turns
// or Pulsar Daily should ever factor in here too.
func (s *Server) handleConstellationBusy(w http.ResponseWriter, r *http.Request) {
	busy, err := s.db.HasInFlightShootingStarRun()
	if err != nil {
		log.Warn("checking constellation busy state failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"busy": busy})
}

// handleSearchConstellationStars backs the Library's search box —
// SearchLibraryStars, not SearchStars (Weaver's own internal lead, which
// deliberately includes rejected stars — see its doc comment for why
// that'd be wrong to surface here).
func (s *Server) handleSearchConstellationStars(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, []store.Star{})
		return
	}
	results, err := s.db.SearchLibraryStars(query, 30)
	if err != nil {
		log.Warn("searching constellation stars failed", "err", err, "query", query)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, results)
}

// constellationStarDetail is the Star detail screen's payload — the star
// itself plus its "Linked articles" (Sources) and "Nearby in the
// constellation" (Edges) blocks in one response, since both are always
// read together on that screen.
type constellationStarDetail struct {
	Star    store.Star         `json:"star"`
	Sources []store.StarSource `json:"sources"`
	Edges   []store.StarEdge   `json:"edges"`
	// Reasoning: shooting_star_candidates' own "why" for this star, most
	// recent row — see store.Store.LatestCandidateReasoning's doc comment.
	// "" for a star with no candidate row (shouldn't normally happen).
	Reasoning string `json:"reasoning"`
	// FirstDiscussedAt is the earliest source thread's own created_at —
	// when the topic was actually first talked about, as opposed to
	// Star.CreatedAt (when this row was inserted, i.e. whenever Weaver's
	// run happened to process it, which for a backlog run can be weeks
	// after the real conversation). nil only for a star with no sources at
	// all, which shouldn't normally happen. The UI's "first noted" label
	// should read this, not Star.CreatedAt.
	FirstDiscussedAt *time.Time `json:"first_discussed_at"`
}

// earliestSourceDate finds the oldest ThreadCreatedAt across a star's
// sources — see constellationStarDetail.FirstDiscussedAt's doc comment for
// why this, and not Star.CreatedAt, is "when was this actually discussed."
func earliestSourceDate(sources []store.StarSource) *time.Time {
	var earliest *time.Time
	for i := range sources {
		t := sources[i].ThreadCreatedAt
		if earliest == nil || t.Before(*earliest) {
			earliest = &t
		}
	}
	return earliest
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
	reasoning, err := s.db.LatestCandidateReasoning(id)
	if err != nil {
		log.Warn("getting star candidate reasoning failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, constellationStarDetail{Star: *star, Sources: sources, Edges: edges, Reasoning: reasoning, FirstDiscussedAt: earliestSourceDate(sources)})
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
		if err := s.db.SetStarStatusAndRecordReview(id, "confirmed", "approved", ""); err != nil {
			log.Warn("approving star failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "discard":
		if err := s.db.SetStarStatusAndRecordReview(id, "rejected", "discarded", ""); err != nil {
			log.Warn("discarding star failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "refine":
		if strings.TrimSpace(req.Correction) == "" {
			http.Error(w, "correction is required for refine", http.StatusBadRequest)
			return
		}
		invalidated, err := s.reconcileAndSaveStar(r.Context(), id, req.Correction)
		if err != nil {
			log.Warn("refining star failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if invalidated {
			// A flat denial ("that wasn't me at all") isn't a partial
			// revision — Refine still resolves the review in one step, it
			// just resolves it as a rejection instead of a confirm, same
			// outcome Discard would have produced. Recorded as
			// "discarded" (star_reviews' action enum has no separate
			// value for this — it's the same real outcome), but with the
			// actual correction text kept as the reason, unlike a plain
			// Discard's own empty correction.
			if err := s.db.SetStarStatusAndRecordReview(id, "rejected", "discarded", req.Correction); err != nil {
				log.Warn("recording invalidated refine failed", "err", err, "id", id)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			break
		}
		// Refine corrects the star but deliberately does NOT resolve the
		// review — reconcileAndSaveStar's UpdateStar call already reset
		// status back to "proposed" (its own doc comment: resets status on
		// any content change), so the star simply stays in the review
		// queue with its revised content. RecordStarReview just logs the
		// action for history, same star_reviews row shape approve/discard
		// write, without touching status the way
		// SetStarStatusAndRecordReview would. This lets the person see
		// the revision and keep refining (or approve/discard) rather than
		// the star silently leaving the queue the moment they send one
		// correction — previously it auto-confirmed here, which is what
		// made Refine feel like it skipped straight to "accepted."
		if err := s.db.RecordStarReview(id, "refined", req.Correction); err != nil {
			log.Warn("recording refine failed", "err", err, "id", id)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
	invalidated, err := s.reconcileAndSaveStar(r.Context(), id, req.Correction)
	if err != nil {
		log.Warn("editing star failed", "err", err, "id", id)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if invalidated {
		// Edit star operates on an already-confirmed star, past the review
		// stage entirely — "rejected" is specifically Inbox-review
		// semantics (see store.Star's own status doc comment), so a flat
		// denial here means the same thing the overflow menu's own
		// Disable action means: a star the person doesn't want, distinct
		// from Weaver having gotten it wrong. star_reviews still isn't
		// written (Edit star never writes there, same as before).
		if err := s.db.SetStarDisabled(id, true); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
//
// Returns invalidated=true when the correction was a flat denial of the
// star's whole premise ("that wasn't me at all") rather than a partial
// revision — the star's content is left untouched in that case (nothing
// worth persisting), and it's the caller's job to decide what
// "invalidated" means for its own flow: Refine (handleReviewConstellationStar)
// rejects the star, Edit star (handleEditConstellationStar) disables it.
// Previously this always wrote back whatever the model produced and the
// Refine caller always confirmed the star regardless — live-observed
// producing a "confirmed" star whose entire body was the model narrating
// that the star was wrong, since nothing here or in the caller recognized
// a flat denial as different from a normal revision.
func (s *Server) reconcileAndSaveStar(reqCtx context.Context, id int64, correction string) (invalidated bool, err error) {
	star, err := s.db.GetStar(id)
	if err != nil {
		return false, err
	}
	cfgRow, err := s.db.GetConstellationConfig()
	if err != nil {
		return false, err
	}
	client := WeaverClient(s.liveConfig(), cfgRow.Model)
	title, summary, body, invalidated, costUSD, err := reconcileStarContent(reqCtx, client, *star, correction)
	if err != nil {
		return false, err
	}
	// Recorded regardless of invalidated — the completion call itself was
	// made and billed either way, only its content gets discarded on a
	// flat denial. See RecordStarReconcileCost's doc comment for why this
	// can't just reuse RecordShootingStarEvent.
	if err := s.db.RecordStarReconcileCost(id, costUSD); err != nil {
		log.Warn("recording star reconcile cost failed", "err", err, "id", id)
	}
	if invalidated {
		return true, nil
	}
	return false, s.db.UpdateStar(id, title, summary, body, star.Tags, star.Confidence, star.IsPersonal)
}

// reconcileStarContent is the actual LLM call behind reconcileAndSaveStar
// — a single completion, not a full Weaver agent.Run: no tools, no dedup/
// link judgment, just folding one piece of free-text human input into an
// already-identified star's fields (see prompts.yaml's weaver.reconcile_system).
// title is "" unless the correction actually changed what the star is
// about — see parseReconcileResponse's TITLE: handling — so a correction
// that only adds nuance to an already-accurate title never touches it.
func reconcileStarContent(reqCtx context.Context, client llm.ChatClient, star store.Star, correction string) (title, summary, body string, invalidated bool, costUSD float64, err error) {
	task := fmt.Sprintf("Current title: %s\n\nCurrent summary: %s\n\nCurrent body:\n%s\n\nWhat they just said: %s", star.Title, star.Summary, star.Body, correction)
	resp, err := client.ChatCompletionStreaming(reqCtx, []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Weaver.ReconcileSystem},
		{Role: "user", Content: task},
	}, func(string) {}, nil)
	if err != nil {
		return "", "", "", false, 0, err
	}
	title, summary, body, invalidated = parseReconcileResponse(resp.Content)
	if invalidated {
		return "", "", "", true, resp.CostUSD, nil
	}
	if summary == "" {
		summary = star.Summary
	}
	if body == "" {
		body = star.Body
	}
	return title, summary, body, false, resp.CostUSD, nil
}

// parseReconcileResponse pulls INVALIDATES:/TITLE:/SUMMARY:/BODY: out of the
// model's response — see prompts.yaml's weaver.reconcile_system for the
// exact format asked for. Falls back to empty strings (reconcileStarContent
// then keeps the star's existing values) if the model didn't follow the
// format, rather than erroring the whole request out over a formatting
// slip. An empty TITLE: line is the normal case (most corrections don't
// change what a star is fundamentally about) and is left empty rather than
// echoing the current title back, so store.UpdateStar's "" == "leave as-is"
// contract does the right thing without this function needing the star's
// current title at all.
func parseReconcileResponse(content string) (title, summary, body string, invalidated bool) {
	lines := strings.Split(content, "\n")
	var bodyLines []string
	inBody := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "INVALIDATES:"):
			invalidated = strings.TrimSpace(strings.TrimPrefix(line, "INVALIDATES:")) == "true"
		case strings.HasPrefix(line, "TITLE:"):
			title = strings.TrimSpace(strings.TrimPrefix(line, "TITLE:"))
		case strings.HasPrefix(line, "SUMMARY:"):
			inBody = false
			summary = strings.TrimSpace(strings.TrimPrefix(line, "SUMMARY:"))
		case strings.HasPrefix(line, "BODY:"):
			inBody = true
			bodyLines = append(bodyLines, strings.TrimSpace(strings.TrimPrefix(line, "BODY:")))
		case inBody:
			bodyLines = append(bodyLines, line)
		}
	}
	if invalidated {
		return "", "", "", true
	}
	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return title, summary, body, false
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
