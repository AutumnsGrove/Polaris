// pulsar_daily_routes.go is the REST API for Pulsar Daily — reading/
// editing the singleton config, fetching an edition, and expand-to-chat.
// See docs/plans/pulsar-daily.md.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"polaris/prompts"
	"polaris/store"
)

// handleGetDailyConfig returns the singleton config row — GetDailyConfig
// itself creates the column-default row on first read, so this never 404s.
func (s *Server) handleGetDailyConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.GetDailyConfig()
	if err != nil {
		log.Warn("getting pulsar daily config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}

// pulsarDailyConfigRequest is the setup modal's request shape — full
// overwrite, not partial, matching pulsarRoutineRequest's convention.
type pulsarDailyConfigRequest struct {
	EnabledBlocks      []string          `json:"enabled_blocks"`
	SportsTeams        string            `json:"sports_teams"`
	CustomInstructions map[string]string `json:"custom_instructions"`
	ArchitectModel     string            `json:"architect_model"`
	WriterModel        string            `json:"writer_model"`
	TimeOfDay          string            `json:"time_of_day"`
}

// validateDailyConfig checks a config request is something the pipeline
// can actually act on — rejected here, at write time, same "fail at
// save, not silently at generation time" reasoning as
// pulsar_routines.go's validateSchedule.
func validateDailyConfig(req pulsarDailyConfigRequest) error {
	if _, _, ok := parseTimeOfDay(req.TimeOfDay); !ok {
		return errors.New(`time_of_day must be "HH:MM" (24-hour, server-local)`)
	}
	if strings.TrimSpace(req.ArchitectModel) == "" {
		return errors.New("architect_model is required")
	}
	if strings.TrimSpace(req.WriterModel) == "" {
		return errors.New("writer_model is required")
	}
	sportsEnabled := false
	for _, key := range req.EnabledBlocks {
		if _, ok := dailyBlockSpecByKey(key); !ok {
			return fmt.Errorf("unknown block %q", key)
		}
		if key == "sports" {
			sportsEnabled = true
		}
	}
	// The one required per-block field — see the plan doc's "Per-block
	// settings UI": "Sports with no team/league preference is meaningless
	// (no sane default exists...)". Every other block's custom
	// instruction below is optional.
	if sportsEnabled && strings.TrimSpace(req.SportsTeams) == "" {
		return errors.New("sports_teams is required when the sports block is enabled")
	}
	for key := range req.CustomInstructions {
		if _, ok := dailyBlockSpecByKey(key); !ok {
			return fmt.Errorf("unknown block %q in custom_instructions", key)
		}
	}
	return nil
}

func (s *Server) handleUpdateDailyConfig(w http.ResponseWriter, r *http.Request) {
	var req pulsarDailyConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateDailyConfig(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.db.UpdateDailyConfig(req.EnabledBlocks, req.SportsTeams, req.CustomInstructions, req.ArchitectModel, req.WriterModel, req.TimeOfDay); err != nil {
		log.Warn("updating pulsar daily config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "pulsar_daily", "config updated", map[string]interface{}{"enabled_blocks": req.EnabledBlocks}, "")

	cfg, err := s.db.GetDailyConfig()
	if err != nil {
		log.Warn("loading just-updated pulsar daily config failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}

// handleGetDailyEdition serves one calendar date's edition — {date} is
// either "YYYY-MM-DD" or the literal "latest", which the frontend's
// initial page load uses to mean "whatever the most recent edition is"
// without first having to know today's date is actually populated yet
// (a request right before the scheduled generation time would otherwise
// 404 on "today" and need a second fallback request).
func (s *Server) handleGetDailyEdition(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	var edition *store.PulsarDailyEdition
	var err error
	if date == "latest" {
		// A date strictly after any real edition sorts after every row,
		// so this reuses LatestDailyEdition's "before date" query as a
		// plain "most recent edition, period" lookup — no separate query
		// needed for what's really the same "find the newest row" shape.
		edition, err = s.db.LatestDailyEdition("9999-12-31")
	} else {
		edition, err = s.db.GetDailyEdition(date)
	}
	if errors.Is(err, store.ErrDailyEditionNotFound) {
		http.Error(w, "no edition found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("getting pulsar daily edition failed", "date", date, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, edition)
}

// handleGetPreviousDailyEdition backs the "← Yesterday" button — the
// most recent edition strictly before the given date, which is what
// "yesterday" actually means once a missed day is possible (see
// store.LatestDailyEdition's doc comment).
func (s *Server) handleGetPreviousDailyEdition(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	edition, err := s.db.LatestDailyEdition(date)
	if errors.Is(err, store.ErrDailyEditionNotFound) {
		http.Error(w, "no earlier edition found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("getting previous pulsar daily edition failed", "date", date, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, edition)
}

// dailyFollowupFamily picks which of the plan doc's three expand-to-chat
// prompt families applies to a block — see "Expand-to-chat prompt
// templates" for why these three (not one bespoke template per block)
// are the right cut: it's the same watch-vs-fresh-pick distinction that
// already predicts diffability, applied to what kind of follow-up makes
// sense.
func dailyFollowupFamily(key string) string {
	switch key {
	case "picture_of_day":
		return "media"
	case "word_of_day", "on_this_day", "quote":
		return "curiosity"
	default: // headlines, trending, tech_science, local, weather, sports
		return "research"
	}
}

type pulsarDailyExpandRequest struct {
	Date     string `json:"date"`
	BlockKey string `json:"block_key"`
}

// pulsarDailyExpandResponse hands back the seeded message text (and, for
// Picture of the Day, a real attachment) instead of running a turn itself
// — see handleExpandDailyBlock's doc comment for why. AttachmentID is
// exactly what POST /api/upload would have returned for the same image,
// so the frontend sends it over the WebSocket the same way any other
// image attachment already flows (ClientMessage.AttachmentID/
// AttachmentFilename/AttachmentContentType).
type pulsarDailyExpandResponse struct {
	Content               string `json:"content"`
	AttachmentID          string `json:"attachment_id,omitempty"`
	AttachmentFilename    string `json:"attachment_filename,omitempty"`
	AttachmentContentType string `json:"attachment_content_type,omitempty"`
}

// handleExpandDailyBlock is a card's tap-to-expand affordance. It used to
// run the whole turn itself (through handleTurn) and only respond once
// the model finished answering — which meant the frontend couldn't
// navigate to the new thread until every tool call had already fired,
// with no way to watch it happen live. Now it just resolves what the
// seeded message should say (and, for an image block, downloads the
// actual image as a real attachment) and hands that back immediately;
// the frontend sends it as a normal new-thread message over its own
// WebSocket connection, the same path any live chat message already
// takes — so navigation and streaming behave exactly like a message the
// user typed themselves. Block content is looked up server-side from the
// stored edition, not trusted from the request body, since a client
// could otherwise seed an arbitrary "content" string into the model's
// framing prefix.
func (s *Server) handleExpandDailyBlock(w http.ResponseWriter, r *http.Request) {
	var req pulsarDailyExpandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Date == "" || req.BlockKey == "" {
		http.Error(w, "date and block_key are required", http.StatusBadRequest)
		return
	}

	edition, err := s.db.GetDailyEdition(req.Date)
	if errors.Is(err, store.ErrDailyEditionNotFound) {
		http.Error(w, "no edition found for that date", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Warn("expand daily block: loading edition failed", "date", req.Date, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var block *store.PulsarDailyBlock
	for i := range edition.Blocks {
		if edition.Blocks[i].Key == req.BlockKey {
			block = &edition.Blocks[i]
			break
		}
	}
	if block == nil {
		http.Error(w, "block not found in that edition", http.StatusNotFound)
		return
	}

	p := prompts.Get()
	var followup string
	switch dailyFollowupFamily(block.Key) {
	case "media":
		followup = p.PulsarDaily.MediaFollowup
	case "curiosity":
		followup = p.PulsarDaily.CuriosityFollowup
	default:
		followup = p.PulsarDaily.ResearchFollowup
	}
	seeded := fmt.Sprintf(p.PulsarDaily.ExpandPrefix, block.Title, block.Content) + " " + followup

	resp := pulsarDailyExpandResponse{Content: seeded}
	if block.ImageURL != "" {
		// The model can't "tell me more about this image" without
		// actually seeing it — block.Content is only ever a caption. A
		// real, previously-shipped bug: without this, the seeded turn
		// had no image attached at all, and the model correctly (if
		// confusingly) replied that it had nothing to look at.
		att, err := s.saveRemoteImageAttachment(r.Context(), block.ImageURL)
		if err != nil {
			log.Warn("expand daily block: fetching image attachment failed, continuing without it", "url", block.ImageURL, "err", err)
		} else {
			resp.AttachmentID = att.ID
			resp.AttachmentFilename = att.Filename
			resp.AttachmentContentType = att.ContentType
		}
	}
	writeJSON(w, resp)
}
