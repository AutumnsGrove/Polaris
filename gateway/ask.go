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
}

// AskResponse is the full result of one turn, assembled from the same
// events a WebSocket client would receive as they stream in.
type AskResponse struct {
	ThreadID      string           `json:"thread_id"`
	Answer        string           `json:"answer"`
	Citations     []tools.Citation `json:"citations"`
	Suggestions   []string         `json:"suggestions"`
	CostUSD       float64          `json:"cost_usd"`
	ContextTokens int              `json:"context_tokens"`
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

	msg := ClientMessage{
		Type:     "message",
		ThreadID: req.ThreadID,
		Content:  req.Content,
		Model:    req.Model,
		Source:   req.Source,
	}

	var answer strings.Builder
	var final ServerEvent
	var turnErr string

	s.handleTurn(r.Context(), msg, func(evt ServerEvent) {
		switch evt.Type {
		case "token":
			answer.WriteString(evt.Content)
		case "done":
			final = evt
		case "error":
			turnErr = evt.Message
		}
	})

	w.Header().Set("Content-Type", "application/json")
	if turnErr != "" {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": turnErr})
		return
	}

	json.NewEncoder(w).Encode(AskResponse{
		ThreadID:      final.ThreadID,
		Answer:        answer.String(),
		Citations:     final.Citations,
		Suggestions:   final.Suggestions,
		CostUSD:       final.CostUSD,
		ContextTokens: final.ContextTokens,
	})
}
