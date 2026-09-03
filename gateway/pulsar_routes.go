// pulsar_routes.go is the REST API for managing Pulsar routines — list/
// create/edit/archive/unarchive, plus a routine's pulse history and the
// unread counts backing the amber indicator. See
// docs/plans/pulsar-routines.md's "UI structure" for what each of these
// backs client-side.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"polaris/store"
)

// handleListPulsarRoutines returns active routines by default, or
// archived ones with ?archived=true — the two lists /pulsar shows as
// separate sections (see the plan doc's "UI structure"), fetched
// separately rather than one combined list with a client-side filter,
// since the archive section is generally not shown by default.
func (s *Server) handleListPulsarRoutines(w http.ResponseWriter, r *http.Request) {
	var routines []store.PulsarRoutine
	var err error
	if r.URL.Query().Get("archived") == "true" {
		routines, err = s.db.ListArchivedPulsarRoutines()
	} else {
		routines, err = s.db.ListActivePulsarRoutines()
	}
	if err != nil {
		log.Warn("listing pulsar routines failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, routines)
}

// pulsarRoutineRequest is the create/edit form's shared request shape —
// one struct for both POST (create) and PATCH (edit), matching the plan
// doc's "one form doing double duty" UI.
type pulsarRoutineRequest struct {
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	FocusMode      string `json:"focus_mode"`
	DeepResearch   bool   `json:"deep_research"`
	ScheduleType   string `json:"schedule_type"`
	ScheduleParams string `json:"schedule_params"`
	TimeOfDay      string `json:"time_of_day"`
}

// validateSchedule checks a routine request's schedule fields are
// something the scheduler (see pulsar_scheduler.go's
// mostRecentScheduledTime) can actually compute a fire time from —
// rejected here, at write time, rather than silently never firing once
// saved.
func validateSchedule(req pulsarRoutineRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("model is required")
	}
	if _, _, ok := parseTimeOfDay(req.TimeOfDay); !ok {
		return errors.New(`time_of_day must be "HH:MM" (24-hour, server-local)`)
	}
	switch req.ScheduleType {
	case "daily":
		return nil
	case "weekly":
		if _, ok := parseWeekday(req.ScheduleParams); !ok {
			return errors.New("schedule_params must be a weekday name for a weekly routine")
		}
		return nil
	case "monthly":
		if _, ok := parseDayOfMonth(req.ScheduleParams); !ok {
			return errors.New("schedule_params must be a day of month (1-31) for a monthly routine")
		}
		return nil
	default:
		return errors.New("schedule_type must be one of daily, weekly, monthly")
	}
}

// handleCreatePulsarRoutine is "New Pulsar" — see the plan doc's UI
// structure section.
func (s *Server) handleCreatePulsarRoutine(w http.ResponseWriter, r *http.Request) {
	var req pulsarRoutineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateSchedule(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := s.db.CreatePulsarRoutine(req.Name, req.Prompt, req.Model, req.FocusMode, req.DeepResearch, req.ScheduleType, req.ScheduleParams, req.TimeOfDay)
	if err != nil {
		log.Warn("creating pulsar routine failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "pulsar", "routine created", map[string]interface{}{"routine_id": id, "name": req.Name}, "")

	routine, err := s.db.GetPulsarRoutine(id)
	if err != nil {
		log.Warn("loading just-created pulsar routine failed", "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, routine)
}

// handleUpdatePulsarRoutine is the edit half of the shared create/edit
// form — same validated fields as create, full overwrite (not partial),
// since the form always submits the complete edited state.
func (s *Server) handleUpdatePulsarRoutine(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req pulsarRoutineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateSchedule(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.db.UpdatePulsarRoutine(id, req.Name, req.Prompt, req.Model, req.FocusMode, req.DeepResearch, req.ScheduleType, req.ScheduleParams, req.TimeOfDay); err != nil {
		if errors.Is(err, store.ErrPulsarRoutineNotFound) {
			http.Error(w, "routine not found", http.StatusNotFound)
		} else {
			log.Warn("updating pulsar routine failed", "id", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "pulsar", "routine edited", map[string]interface{}{"routine_id": id, "name": req.Name}, "")

	routine, err := s.db.GetPulsarRoutine(id)
	if err != nil {
		log.Warn("loading just-updated pulsar routine failed", "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, routine)
}

// handleArchivePulsarRoutine is the routine edit screen's "Delete" action
// — see the plan doc's "Delete is always soft".
func (s *Server) handleArchivePulsarRoutine(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := s.db.ArchivePulsarRoutine(id); err != nil {
		if errors.Is(err, store.ErrPulsarRoutineNotFound) {
			http.Error(w, "routine not found", http.StatusNotFound)
		} else {
			log.Warn("archiving pulsar routine failed", "id", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "pulsar", "routine archived", map[string]interface{}{"routine_id": id}, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleUnarchivePulsarRoutine is /pulsar's archive section's own
// "restore" action — see the plan doc's "Pause" discussion (archive/
// unarchive standing in for a separate pause state in v1).
func (s *Server) handleUnarchivePulsarRoutine(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := s.db.UnarchivePulsarRoutine(id); err != nil {
		if errors.Is(err, store.ErrPulsarRoutineNotFound) {
			http.Error(w, "routine not found", http.StatusNotFound)
		} else {
			log.Warn("unarchiving pulsar routine failed", "id", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "pulsar", "routine unarchived", map[string]interface{}{"routine_id": id}, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleListPulsarPulses backs a routine's detail screen — its pulse
// history, thread-row-style (see the plan doc's "UI structure").
func (s *Server) handleListPulsarPulses(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pulses, err := s.db.ListPulsarPulses(id)
	if err != nil {
		log.Warn("listing pulsar pulses failed", "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// InProgress per pulse — see server.go's markTurnInFlight doc comment
	// for why the frontend can't derive this from the pulse's own
	// messages alone. Computed here, not folded into store.PulsarPulseSummary,
	// since it's in-memory server state, not anything a DB query could
	// answer.
	type pulseWithStatus struct {
		store.PulsarPulseSummary
		InProgress bool `json:"in_progress"`
	}
	out := make([]pulseWithStatus, len(pulses))
	for i, p := range pulses {
		out[i] = pulseWithStatus{PulsarPulseSummary: p, InProgress: s.IsTurnInFlight(p.ThreadID)}
	}
	writeJSON(w, out)
}

// handlePulsarUnreadCounts backs the amber dot/count indicator — see the
// plan doc's "Amber indicator semantics". Keyed by routine id as a
// string (JSON object keys can't be numeric), matching how the frontend
// looks these up per routine row.
func (s *Server) handlePulsarUnreadCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := s.db.UnreadPulseCounts()
	if err != nil {
		log.Warn("getting pulsar unread counts failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byString := make(map[string]int, len(counts))
	for id, n := range counts {
		byString[fmt.Sprintf("%d", id)] = n
	}
	writeJSON(w, byString)
}
