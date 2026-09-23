// ask.go exposes a synchronous HTTP alternative to /ws for programmatic
// callers (e.g. another agent doing its own research) that just want a
// finished, cited answer back — not a live event stream. It runs the
// exact same handleTurn path as the WebSocket client, so the resulting
// thread/messages/events are indistinguishable from a normal chat turn
// in the database; only the transport differs. The one deliberate
// exception is AskRequest.Anonymous (ghost mode, issue #67): set that and
// nothing is persisted at all, same as a ghost turn over /ws — see
// ClientMessage.Anonymous's doc comment in protocol.go.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

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
	// FocusMode/DeepResearch/QuickMode mirror ClientMessage's fields of
	// the same name — see protocol.go's doc comments. Optional: a
	// programmatic caller not exercising these can just omit them.
	FocusMode    string `json:"focus_mode,omitempty"`
	DeepResearch bool   `json:"deep_research,omitempty"`
	QuickMode    bool   `json:"quick_mode,omitempty"`
	// Attachments mirrors ClientMessage.Attachments — see attachments.go.
	// A JSON caller uploads each file via POST /api/upload first, then
	// passes their IDs here. A multipart/form-data caller can skip that
	// step entirely and attach one or more files inline instead — see
	// decodeAskRequest — in which case this is populated automatically
	// and doesn't need to be set directly.
	Attachments []AttachmentRef `json:"attachments,omitempty"`
	// Anonymous mirrors ClientMessage.Anonymous — starts (or continues) a
	// ghost thread (issue #67): nothing about this turn is persisted
	// anywhere (no thread/message/event rows), and its real LLM cost is
	// only ever recorded in aggregate via store.Store's ghost_usage table,
	// never tied back to this request's thread or content — see
	// ClientMessage.Anonymous's doc comment in protocol.go for the full
	// semantics, including why History (below) is required to continue one.
	Anonymous bool `json:"anonymous,omitempty"`
	// History mirrors ClientMessage.History — a ghost thread's prior turns,
	// held and replayed by the caller itself, since nothing about a ghost
	// thread is ever written to store.Store for this endpoint to load a
	// continuation's history from the way ThreadID normally does. Only
	// meaningful when Anonymous is true; ignored otherwise. Omit it (or
	// leave it empty) on a ghost thread's first message.
	History []GhostTurn `json:"history,omitempty"`
	// WaitVerification mirrors ClientMessage.WaitVerification — a debug/
	// stress-testing knob only, see its doc comment. When true,
	// AskResponse.Verification carries the full per-claim result
	// (including confidence) instead of the caller needing to poll GET
	// /api/threads/{id} for messages.verification to eventually populate
	// asynchronously.
	WaitVerification bool `json:"wait_verification,omitempty"`
	// FullTurnHistory mirrors ClientMessage.FullTurnHistoryOverride — a
	// *bool, not bool, so omitting it means "use the operator's real
	// full_turn_history setting" rather than silently forcing it off.
	// Lets a caller A/B the setting per-request (e.g. comparing a
	// follow-up's trace/cost with and against the shared operator
	// setting) without mutating PUT /api/settings for every other client.
	FullTurnHistory *bool `json:"full_turn_history,omitempty"`
}

// AskResponse is the full result of one turn, assembled from the same
// events a WebSocket client would receive as they stream in.
type AskResponse struct {
	ThreadID      string           `json:"thread_id"`
	Answer        string           `json:"answer"`
	Citations     []tools.Citation `json:"citations"`
	Cards         []tools.Card     `json:"cards,omitempty"`
	Chart         *tools.ChartSpec `json:"chart,omitempty"`
	Suggestions   []string         `json:"suggestions"`
	CostUSD       float64          `json:"cost_usd"`
	ContextTokens int              `json:"context_tokens"`
	// DurationMs is how long agent.Run took to produce the answer — see
	// ServerEvent.DurationMs's doc comment in protocol.go.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// PromptTokens/CacheReadTokens — see ServerEvent's doc comment on the
	// same fields (issue #107).
	PromptTokens    int `json:"prompt_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	// Title is the thread's current title — the LLM-generated one if
	// this turn's generateTitle call succeeded (new threads only), or
	// otherwise the truncated-question placeholder CreateThread set.
	// Fetched fresh after handleTurn returns rather than threaded through
	// ServerEvent, since title generation happens out-of-band from the
	// normal event stream and a WebSocket client never needs it pushed —
	// it just reads Thread.Title from GET /api/threads.
	Title string `json:"title,omitempty"`
	// Verification is only ever populated when the request set
	// WaitVerification — see its doc comment. Nil otherwise, same as a
	// normal chat turn where the caller has to wait for the async
	// "verification" WebSocket event (or reload the thread) instead. This
	// is the filtered "supported, at/above threshold" view — the same
	// data a real chat turn's badge would show.
	Verification []VerificationMark `json:"verification,omitempty"`
	// VerificationDebug is every claim runVerification actually
	// considered, unfiltered — choice, confidence, and a Reason for any
	// claim that couldn't be checked at all — only populated alongside
	// Verification when WaitVerification was set. See
	// gateway.ClaimVerification's doc comment.
	VerificationDebug []ClaimVerification `json:"verification_debug,omitempty"`
}

// decodeAskRequest reads an AskRequest from either a plain JSON body (the
// original, still-supported shape — an existing caller doing the
// upload-then-reference two-step via POST /api/upload followed by this
// with attachment_id set is unaffected) or a multipart/form-data body
// carrying an inline "file" part alongside the same fields as ordinary
// form values. The multipart shape exists so a caller can upload and ask
// in a single round trip — e.g. `curl -F file=@modelcard.pdf -F
// content="the highlights from page 50"` — instead of needing a separate
// POST /api/upload first just to get an ID to reference. Detected by
// Content-Type, not a query param or extra field, so it's fully additive.
// Writes its own error response and returns ok=false on any failure —
// callers should just return immediately when ok is false.
func (s *Server) decodeAskRequest(w http.ResponseWriter, r *http.Request) (req AskRequest, ok bool) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return AskRequest{}, false
		}
		return req, true
	}

	// Same size cap and slack as handleUpload — a caller attaching a file
	// here goes through the exact same saveUploadedFile path, so the same
	// limit applies.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "invalid or too-large multipart body (max 100MB file): "+err.Error(), http.StatusBadRequest)
		return AskRequest{}, false
	}

	req = AskRequest{
		Content:          r.FormValue("content"),
		Model:            r.FormValue("model"),
		ThreadID:         r.FormValue("thread_id"),
		Source:           r.FormValue("source"),
		FocusMode:        r.FormValue("focus_mode"),
		DeepResearch:     formBool(r, "deep_research"),
		QuickMode:        formBool(r, "quick_mode"),
		Anonymous:        formBool(r, "anonymous"),
		WaitVerification: formBool(r, "wait_verification"),
	}
	// history has no natural multipart form-field shape (it's a list of
	// {role, content} pairs, not a scalar) — accepted as a JSON-encoded
	// string under the same field name instead, so a ghost thread that
	// also wants to attach a file inline isn't forced to give up either
	// capability.
	if h := r.FormValue("history"); h != "" {
		if err := json.Unmarshal([]byte(h), &req.History); err != nil {
			http.Error(w, "invalid \"history\" field: must be a JSON array of {role,content}", http.StatusBadRequest)
			return AskRequest{}, false
		}
	}

	// r.MultipartForm.File["file"] rather than r.FormFile("file") — the
	// latter only ever returns the first part under that key, which is
	// how this stayed single-file-only even after ClientMessage/AskRequest
	// went multi-valued. A caller sending several -F file=@a -F file=@b
	// parts under the same field name gets every one of them attached.
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		// A multipart request with only text fields and no file part is a
		// legitimate (if unusual) way to ask without an attachment — not
		// an error.
		return req, true
	}
	for _, header := range headers {
		file, ferr := header.Open()
		if ferr != nil {
			http.Error(w, "reading \"file\" field: "+ferr.Error(), http.StatusBadRequest)
			return AskRequest{}, false
		}
		uploaded, uerr := s.saveUploadedFile(file, header)
		file.Close()
		if uerr != nil {
			var ue *uploadError
			status := http.StatusInternalServerError
			if errors.As(uerr, &ue) {
				status = ue.status
			}
			http.Error(w, uerr.Error(), status)
			return AskRequest{}, false
		}
		req.Attachments = append(req.Attachments, AttachmentRef{
			ID:          uploaded.ID,
			Filename:    uploaded.Filename,
			ContentType: uploaded.ContentType,
		})
	}
	return req, true
}

func formBool(r *http.Request, key string) bool {
	v := r.FormValue(key)
	return v == "true" || v == "1"
}

// handleAsk runs one full agent turn and blocks until it's done, unlike
// /ws which streams progress as separate frames. answer is reassembled
// from "token" chunks — the same content a WebSocket client renders
// live — since handleTurn's "done" event carries cost/citations/
// suggestions but not the answer text itself (the frontend doesn't need
// it repeated there; a sync caller does).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeAskRequest(w, r)
	if !ok {
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
		Type:                    "message",
		ThreadID:                req.ThreadID,
		Content:                 req.Content,
		Model:                   req.Model,
		Source:                  req.Source,
		FocusMode:               req.FocusMode,
		DeepResearch:            req.DeepResearch,
		QuickMode:               req.QuickMode,
		Attachments:             req.Attachments,
		Anonymous:               req.Anonymous,
		History:                 req.History,
		WaitVerification:        req.WaitVerification,
		FullTurnHistoryOverride: req.FullTurnHistory,
	}

	var answer strings.Builder
	var final ServerEvent
	var verification []VerificationMark
	var verificationDebug []ClaimVerification
	var turnErr string

	// No live WebSocket on this path — nil requestLocation means
	// ResolveLocation just falls straight through to DefaultLocation, same
	// as before this feature existed.
	s.handleTurn(r.Context(), msg, func(evt ServerEvent) {
		switch evt.Type {
		case "token":
			answer.WriteString(evt.Content)
		case "commentary":
			// Matches the WebSocket frontend's handling exactly (see
			// ServerEvent's doc comment on "commentary" above) — whatever
			// streamed as "token" before a tool call was preamble, not the
			// real final answer, and must not survive into it. Without this
			// reset, a turn that talks before searching (a real, observed
			// pattern — e.g. "Let me check the release history page...")
			// got that sentence permanently glued onto the front of Answer.
			answer.Reset()
		case "done":
			final = evt
		case "verification":
			verification = evt.Verification
			verificationDebug = evt.VerificationDebug
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
		ThreadID:          final.ThreadID,
		Answer:            answer.String(),
		Citations:         final.Citations,
		Cards:             final.Cards,
		Chart:             final.Chart,
		Suggestions:       final.Suggestions,
		CostUSD:           final.CostUSD,
		ContextTokens:     final.ContextTokens,
		DurationMs:        final.DurationMs,
		PromptTokens:      final.PromptTokens,
		CacheReadTokens:   final.CacheReadTokens,
		Title:             title,
		Verification:      verification,
		VerificationDebug: verificationDebug,
	})
}

// handleAskStream is handleAsk's streaming twin — same request shape, but
// forwards every ServerEvent live as it happens (NDJSON, one event per
// line, same wire shape /ws sends over the WebSocket) instead of blocking
// until the whole turn finishes. Built for Atlas's Quick Answer, where
// waiting out a full agent turn in silence before anything appears is
// exactly the "nothing then everything at once" problem streaming fixes —
// but the shape is generic, not Quick-Answer-specific, so any future
// caller wanting progressive output over plain HTTP (no WebSocket) can
// reuse it as-is.
//
// Deliberately NDJSON over a flushed response body, not Server-Sent
// Events — matches this codebase's existing streaming precedent
// (handleSpeakStream) rather than introducing a second convention, and a
// plain POST body is simpler to read from `fetch` than SSE's GET-only
// EventSource API would have been.
func (s *Server) handleAskStream(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeAskRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Same shutdown-draining registration as handleAsk — see its doc
	// comment for why this must happen before handleTurn, not after.
	if !s.TryStartTurn() {
		http.Error(w, "the server is restarting — please retry in a few seconds", http.StatusServiceUnavailable)
		return
	}
	defer s.FinishTurn()

	msg := ClientMessage{
		Type:                    "message",
		ThreadID:                req.ThreadID,
		Content:                 req.Content,
		Model:                   req.Model,
		Source:                  req.Source,
		FocusMode:               req.FocusMode,
		DeepResearch:            req.DeepResearch,
		QuickMode:               req.QuickMode,
		Attachments:             req.Attachments,
		Anonymous:               req.Anonymous,
		History:                 req.History,
		FullTurnHistoryOverride: req.FullTurnHistory,
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)

	// handleTurn spawns a detached goroutine for follow-up suggestions
	// that outlives handleTurn's own return (see its doc comment on why —
	// the point is not blocking the response on a second, invisible LLM
	// call) and calls send() again once that finishes, well after this
	// handler function itself has already returned. For handleAsk that's
	// harmless — its own send callback only mutates local variables
	// nobody reads anymore — but here send() writes to the real
	// http.ResponseWriter, and doing that once the handler has returned
	// is unsafe: net/http may have already reused or torn down state
	// backing it, which surfaced as a real nil-pointer panic (recovered,
	// not crashing, but a real bug) the very first time this endpoint ran
	// end-to-end against a live model. done, set right after handleTurn
	// returns below, makes every event from that point on (suggestions
	// chief among them) a silent no-op instead — Atlas's Quick Answer
	// doesn't render suggestions anyway, so nothing is lost by dropping
	// them here specifically.
	//
	// The check ("is it safe to write?") and the write itself share
	// sendMu, and done is only ever flipped while holding it too — a bare
	// atomic.Bool checked before the write left a real (if narrow) window
	// where a goroutine could pass the check and then get preempted before
	// its enc.Encode/Flush actually ran, with done.Store(true) landing in
	// between and reproducing the exact panic this exists to prevent.
	// Serializing the check-and-write against the done-flip closes that
	// window structurally instead of just narrowing it.
	var sendMu sync.Mutex
	done := false
	s.handleTurn(r.Context(), msg, func(evt ServerEvent) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if done {
			return
		}
		// Unlike handleAsk's own accumulation, no commentary-reset logic
		// is needed here: this just forwards the raw event stream, and the
		// consumer (search.svelte.ts's askQuickAnswer, mirroring
		// state.svelte.ts's handleEvent) applies that same reset itself
		// when it sees a "commentary" event, exactly like the WebSocket
		// chat client already does.
		_ = enc.Encode(evt)
		flusher.Flush()
	}, nil)
	sendMu.Lock()
	done = true
	sendMu.Unlock()
}
