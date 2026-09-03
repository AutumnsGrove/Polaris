package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"polaris/store"
)

// maxThreadTitleLen bounds a manually-renamed title — generous enough
// for a real title (the auto-generated ones cap much shorter, at
// maxTitleLen in turn.go) while still keeping the sidebar's fixed-width
// row from having to deal with an arbitrarily long string.
const maxThreadTitleLen = 120

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.db.ListThreads(100)
	if err != nil {
		log.Warn("listing threads failed", "err", err)
		s.db.LogEvent("", "error", "thread", "listing threads failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, threads)
}

// handleSearchThreads backs the sidebar's search box — full-text search
// over every past message's content, not just thread titles (see
// store.Store.SearchMessages's doc comment for why title-only wouldn't
// actually answer "find that thing I asked last month"). An empty/missing
// q returns an empty list rather than erroring, since the frontend calls
// this live as the user types and a not-yet-typed query isn't a client
// mistake.
func (s *Server) handleSearchThreads(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, []store.MessageSearchResult{})
		return
	}
	results, err := s.db.SearchMessages(query, 30)
	if err != nil {
		log.Warn("searching threads failed", "query", query, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, results)
}

// VariantGroup describes the alternatives available at one message
// position — every reply an edit/regenerate at that spot has ever
// produced, oldest first, plus which one is currently being shown.
type VariantGroup struct {
	IDs    []string `json:"ids"`
	Active string   `json:"active"`
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thread, err := s.db.GetThread(id)
	if err != nil {
		log.Warn("getting thread failed", "thread", id, "err", err)
		s.db.LogEvent(id, "warn", "thread", "getting thread failed", map[string]interface{}{"err": err.Error()}, "")
		// sql.ErrNoRows (the id genuinely doesn't exist/is disabled/is a
		// hidden variant — see GetThread's own doc comment) is the only
		// case that actually means "not found". Anything else — a busy
		// timeout, a locked file, any other transient database error —
		// used to collapse into the same 404, which openThread()
		// (state.svelte.ts) treats as "nothing to show, do nothing" with
		// no retry and no visible error. A transient DB hiccup is exactly
		// what the restart window can produce (see cmd/run.go: the old
		// process's db.Close() doesn't run until it's done draining,
		// which can overlap the new process already serving live
		// traffic) — reporting it as "not found" instead of a retryable
		// failure left the UI silently stuck on stale content with no
		// indication anything went wrong, which looks a lot like landing
		// on the wrong thread even though the data itself was never bad.
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "thread not found", http.StatusNotFound)
		} else {
			http.Error(w, "temporarily unable to load thread — please retry", http.StatusServiceUnavailable)
		}
		return
	}

	// The first time an Atlas Quick Answer's thread is actually opened
	// here (e.g. via its "Continue in Assistant" link) is what makes it
	// start showing up in ListThreads — see continued_in_assistant's
	// schema comment. Best-effort and off the response path, same
	// reasoning as RecordSearch: a thread that loaded fine is worth
	// showing even if this particular flip fails.
	if thread.Source == "atlas" {
		if err := s.db.MarkThreadContinued(id); err != nil {
			log.Warn("marking thread continued failed", "thread", id, "err", err)
		}
	}

	// Same "flip on first real open" shape as MarkThreadContinued above —
	// this is the entire mechanism behind Pulsar's amber unread indicator
	// (see the plan doc's "seen" schema comment on threads). Best-effort
	// for the same reason.
	if thread.Source == "pulsar" {
		if err := s.db.MarkPulseSeen(id); err != nil {
			log.Warn("marking pulse seen failed", "thread", id, "err", err)
		}
	}

	effectiveID, err := s.db.EffectiveThreadID(id)
	if err != nil {
		log.Warn("resolving active variant failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "resolving active variant failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Cost/context/compaction state describe whichever variant is
	// actually being shown, not necessarily root's own row — a
	// browsed-to variant was generated (and cost money) on its own,
	// independent of root's own content.
	if effectiveID != id {
		if effective, err := s.db.GetThreadRaw(effectiveID); err != nil {
			log.Warn("loading active variant's own thread row failed", "thread", id, "variant", effectiveID, "err", err)
		} else {
			thread.CostUSD = effective.CostUSD
			thread.ContextTokens = effective.ContextTokens
			thread.CompactedSummary = effective.CompactedSummary
			thread.CompactedThroughID = effective.CompactedThroughID
		}
	}

	messages, err := s.db.GetMessages(effectiveID)
	if err != nil {
		log.Warn("getting thread messages failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "getting thread messages failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	variants, err := s.buildVariantsMap(id, effectiveID)
	if err != nil {
		// Non-fatal — the thread itself loaded fine, it just won't show
		// any variant switchers this time. Worth logging, not worth
		// failing the whole request over.
		log.Warn("building variants map failed", "thread", id, "err", err)
		s.db.LogEvent(id, "warn", "thread", "building variants map failed", map[string]interface{}{"err": err.Error()}, "")
		variants = nil
	}

	writeJSON(w, struct {
		*store.Thread
		Messages       []store.Message      `json:"messages"`
		Variants       map[int]VariantGroup `json:"variants,omitempty"`
		TurnInProgress bool                 `json:"turn_in_progress"`
	}{thread, messages, variants, s.IsTurnInFlight(id)})
}

// buildVariantsMap collects every position rootID has been edited/
// regenerated at into the shape the frontend needs: for each, the full
// ordered list of alternatives and which one is currently active. A
// position with only one "variant" (nothing's actually been forked there)
// is dropped — that's the signal the frontend uses to not show a switcher
// for an ordinary, never-edited turn.
func (s *Server) buildVariantsMap(rootID, effectiveID string) (map[int]VariantGroup, error) {
	indices, err := s.db.VariantIndices(rootID)
	if err != nil {
		return nil, err
	}
	if len(indices) == 0 {
		return nil, nil
	}
	result := make(map[int]VariantGroup, len(indices))
	for _, idx := range indices {
		ids, err := s.db.VariantsAt(rootID, idx)
		if err != nil {
			return nil, err
		}
		if len(ids) < 2 {
			continue
		}
		result[idx] = VariantGroup{IDs: ids, Active: effectiveID}
	}
	return result, nil
}

// handleSwapVariant switches which variant of rootID's conversation is
// currently shown — see store.SetActiveVariant. Responds with the same
// shape as GetThread so the frontend can replace its state directly from
// this call instead of a second round-trip.
func (s *Server) handleSwapVariant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		VariantID string `json:"variant_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.VariantID) == "" {
		http.Error(w, "variant_id is required", http.StatusBadRequest)
		return
	}

	// Only ever swap to an id VariantsAt actually returned for this
	// thread — trusting an arbitrary client-supplied thread id here
	// would let one thread's turn start writing into a completely
	// unrelated thread.
	valid, err := s.isKnownVariant(id, req.VariantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !valid {
		http.Error(w, "not a variant of this thread", http.StatusBadRequest)
		return
	}

	if err := s.db.SetActiveVariant(id, req.VariantID); err != nil {
		log.Warn("swapping variant failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "swapping variant failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent(id, "info", "thread", "switched active variant", map[string]interface{}{"variant_id": req.VariantID}, "")

	s.handleGetThread(w, r)
}

func (s *Server) isKnownVariant(rootID, variantID string) (bool, error) {
	if variantID == rootID {
		return true, nil
	}
	indices, err := s.db.VariantIndices(rootID)
	if err != nil {
		return false, err
	}
	for _, idx := range indices {
		ids, err := s.db.VariantsAt(rootID, idx)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if id == variantID {
				return true, nil
			}
		}
	}
	return false, nil
}

// handleUpdateThread applies a partial update to a thread — rename
// and/or favorite, either or both in one request. The only other way a
// title changes is the one-time LLM-generated title right after a new
// thread's first turn (see turn.go's generateTitle); a manual rename
// here always wins over that, whether it happens before or after.
func (s *Server) handleUpdateThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Title    *string `json:"title"`
		Favorite *bool   `json:"favorite"`
		// Model/FocusMode/DeepResearch update the thread's sticky turn
		// config directly — used when the composer's selectors are
		// changed without also sending a message (handleTurn writes
		// through the same config on every turn instead). Any of the
		// three being set triggers one combined SetThreadConfig call,
		// since that's a single UPDATE covering all three columns —
		// whichever of the three wasn't sent here is filled in from the
		// thread's current row rather than overwritten with a zero value.
		Model        *string `json:"model"`
		FocusMode    *string `json:"focus_mode"`
		DeepResearch *bool   `json:"deep_research"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		if len(title) > maxThreadTitleLen {
			title = title[:maxThreadTitleLen]
		}
		if err := s.db.SetThreadTitle(id, title); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "thread not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		s.db.LogEvent(id, "info", "thread", "thread renamed", map[string]interface{}{"title": title}, "")
	}

	if req.Favorite != nil {
		if err := s.db.SetThreadFavorite(id, *req.Favorite); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "thread not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		s.db.LogEvent(id, "info", "thread", "thread favorite changed", map[string]interface{}{"favorite": *req.Favorite}, "")
	}

	if req.Model != nil || req.FocusMode != nil || req.DeepResearch != nil {
		current, err := s.db.GetThreadRaw(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "thread not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		model, focusMode, deepResearch := current.Model, current.FocusMode, current.DeepResearch
		if req.Model != nil {
			model = *req.Model
		}
		if req.FocusMode != nil {
			focusMode = *req.FocusMode
		}
		if req.DeepResearch != nil {
			deepResearch = *req.DeepResearch
		}
		if err := s.db.SetThreadConfig(id, model, focusMode, deepResearch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "thread not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		s.db.LogEvent(id, "info", "thread", "thread turn config changed", map[string]interface{}{"model": model, "focus_mode": focusMode, "deep_research": deepResearch}, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleRegenerateTitle re-runs title generation using the thread's full
// message history instead of just its opening message — see turn.go's
// regenerateTitle. This is the "Regenerate title" menu action, distinct
// from the one-time automatic title handleTurn generates right after a
// brand-new thread's first turn. A manual rename (handleUpdateThread)
// still always wins over either, whether it happens before or after.
func (s *Server) handleRegenerateTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// effectiveID, not id — a browsed-to variant (see handleGetThread) has
	// its own messages and its own cost ledger, so titling and billing
	// this call both need to follow whichever variant is actually being
	// shown, same as handleGetThread's cost/context fields already do.
	effectiveID, err := s.db.EffectiveThreadID(id)
	if err != nil {
		log.Warn("resolving active variant failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "resolving active variant failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	thread, err := s.db.GetThreadRaw(effectiveID)
	if err != nil {
		log.Warn("getting thread failed", "thread", id, "err", err)
		// See handleGetThread's doc comment on this same distinction.
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "thread not found", http.StatusNotFound)
		} else {
			http.Error(w, "temporarily unable to load thread — please retry", http.StatusServiceUnavailable)
		}
		return
	}

	history, err := s.loadHistory(effectiveID, 0)
	if err != nil {
		log.Warn("loading thread history failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "loading thread history for title regeneration failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(history) == 0 {
		http.Error(w, "thread has no messages to title", http.StatusBadRequest)
		return
	}

	cfg := s.liveConfig()
	modelCfg := cfg.ModelByID(thread.Model)

	title, cost, err := s.regenerateTitle(cfg, modelCfg, history)
	if err != nil {
		log.Warn("thread title regeneration failed", "thread", id, "err", err)
		s.db.LogEvent(id, "warn", "title", "thread title regeneration failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, "title regeneration failed", http.StatusBadGateway)
		return
	}
	if title == "" {
		s.db.LogEvent(id, "warn", "title", "model returned no usable title", nil, "")
		http.Error(w, "model returned no usable title", http.StatusBadGateway)
		return
	}

	// id (the root, client-facing thread), not effectiveID — same target
	// SetThreadTitle always uses, so the sidebar/URL/ThreadMenu entry
	// updates regardless of which variant happens to be active.
	if err := s.db.SetThreadTitle(id, title); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cost > 0 {
		if err := s.db.AddThreadCost(effectiveID, cost); err != nil {
			log.Warn("recording title regeneration cost failed", "thread", id, "err", err)
		}
	}
	s.db.LogEvent(id, "info", "title", "thread title regenerated", map[string]interface{}{"title": title, "cost_usd": cost}, "")

	writeJSON(w, struct {
		Title string `json:"title"`
	}{title})
}

// handleDeleteThread soft-deletes a thread — see store.DeleteThread's
// doc comment. The row and its messages/events stay in the database;
// this just stops it from showing up anywhere in the API.
func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteThread(id); err != nil {
		log.Warn("deleting thread failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "deleting thread failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "thread", "thread disabled (soft delete)", map[string]interface{}{"thread_id": id}, "")
	w.WriteHeader(http.StatusNoContent)
}
