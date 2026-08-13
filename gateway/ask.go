// ask.go exposes a synchronous HTTP alternative to /ws for programmatic
// callers (e.g. another agent doing its own research) that just want a
// finished, cited answer back — not a live event stream. It runs the
// exact same handleTurn path as the WebSocket client, so the resulting
// thread/messages/events are indistinguishable from a normal chat turn
// in the database; only the transport differs.
package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"polaris/tools"
)

// AskRequest is the POST /api/ask body. ThreadID continues an existing
// thread (same semantics as ClientMessage.ThreadID); omit it to start a
// new one. Source tags a brand-new thread's origin — see
// ClientMessage.Source — and is ignored when continuing an existing thread.
type AskRequest struct {
	Content  string `json:"content"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	Source   string `json:"source,omitempty"`
	// FocusMode/DeepResearch mirror ClientMessage's fields of the same
	// name — see protocol.go's doc comments. Optional: a programmatic
	// caller not exercising these can just omit them.
	FocusMode    string `json:"focus_mode,omitempty"`
	DeepResearch bool   `json:"deep_research,omitempty"`
	// AttachmentID/AttachmentFilename/AttachmentContentType mirror
	// ClientMessage's fields of the same name — see attachments.go. A
	// caller uploads via POST /api/upload first, then passes its ID here.
	AttachmentID          string `json:"attachment_id,omitempty"`
	AttachmentFilename    string `json:"attachment_filename,omitempty"`
	AttachmentContentType string `json:"attachment_content_type,omitempty"`
}

// AskResponse is the full result of one turn, assembled from the same
// events a WebSocket client would receive as they stream in.
type AskResponse struct {
	ThreadID      string           `json:"thread_id"`
	Answer        string           `json:"answer"`
	Citations     []tools.Citation `json:"citations"`
	Cards         []tools.Card     `json:"cards,omitempty"`
	Suggestions   []string         `json:"suggestions"`
	CostUSD       float64          `json:"cost_usd"`
	ContextTokens int              `json:"context_tokens"`
	// DurationMs is how long agent.Run took to produce the answer — see
	// ServerEvent.DurationMs's doc comment in protocol.go.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Title is the thread's current title — the LLM-generated one if
	// this turn's generateTitle call succeeded (new threads only), or
	// otherwise the truncated-question placeholder CreateThread set.
	// Fetched fresh after handleTurn returns rather than threaded through
	// ServerEvent, since title generation happens out-of-band from the
	// normal event stream and a WebSocket client never needs it pushed —
	// it just reads Thread.Title from GET /api/threads.
	Title string `json:"title,omitempty"`
}

// handleAsk runs one full agent turn and blocks until it's done, unlike
// /ws which streams progress as separate frames. answer is reassembled
// from "token" chunks — the same content a WebSocket client renders
// live — since handleTurn's "done" event carries cost/citations/
// suggestions but not the answer text itself (the frontend doesn't need
// it repeated there; a sync caller does).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	// Same shutdown-draining registration /ws's handleWS does before
	// calling handleTurn — without it, this turn is invisible to
	// WaitForActiveTurns (a self-update restart wouldn't wait for it to
	// finish) and TryStartTurn's "reject new turns once a restart is
	// underway" guard never applies to this endpoint either, leaving a
	// window for a kill mid-DB-write identical to the one TryStartTurn's
	// doc comment describes for /ws.
	if !s.TryStartTurn() {
		http.Error(w, "the server is restarting — please retry in a few seconds", http.StatusServiceUnavailable)
		return
	}
	defer s.FinishTurn()

	msg := ClientMessage{
		Type:                  "message",
		ThreadID:              req.ThreadID,
		Content:               req.Content,
		Model:                 req.Model,
		Source:                req.Source,
		FocusMode:             req.FocusMode,
		DeepResearch:          req.DeepResearch,
		AttachmentID:          req.AttachmentID,
		AttachmentFilename:    req.AttachmentFilename,
		AttachmentContentType: req.AttachmentContentType,
	}

	var answer strings.Builder
	var final ServerEvent
	var turnErr string

	// No live WebSocket on this path — nil requestLocation means
	// ResolveLocation just falls straight through to DefaultLocation, same
	// as before this feature existed.
	s.handleTurn(r.Context(), msg, func(evt ServerEvent) {
		switch evt.Type {
		case "token":
			answer.WriteString(evt.Content)
		case "done":
			final = evt
		case "error":
			turnErr = evt.Message
		}
	}, nil)

	w.Header().Set("Content-Type", "application/json")
	if turnErr != "" {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": turnErr})
		return
	}

	// Best-effort — a title lookup failing shouldn't fail the whole
	// response when the answer itself already succeeded.
	var title string
	if thread, err := s.db.GetThread(final.ThreadID); err == nil {
		title = thread.Title
	}

	json.NewEncoder(w).Encode(AskResponse{
		ThreadID:      final.ThreadID,
		Answer:        answer.String(),
		Citations:     final.Citations,
		Cards:         final.Cards,
		Suggestions:   final.Suggestions,
		CostUSD:       final.CostUSD,
		ContextTokens: final.ContextTokens,
		DurationMs:    final.DurationMs,
		Title:         title,
	})
}
