package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeImageAwareLLMServer is fakeLLMServer's sibling: it also answers the
// vision-model's one-off, non-streaming DescribeImage call (see
// llm/vision.go), with an artificial delay standing in for a real vision
// model's multi-second latency. The two request shapes are told apart the
// same way OpenRouter itself would have to: doRequest (the tool-capable/
// plain-chat path) always sets "stream": true; visionRequest always sets
// it to false (see both structs' Stream field) — no other signal is this
// reliable across both request bodies.
func fakeImageAwareLLMServer(t *testing.T, visionDescription string, visionDelay time.Duration, answer string) *httptest.Server {
	t.Helper()
	chunk, err := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": answer}}},
	})
	if err != nil {
		t.Fatalf("marshaling fake SSE chunk: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading fake LLM request body: %v", err)
		}
		var probe struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			t.Fatalf("unmarshaling fake LLM request body: %v", err)
		}

		if !probe.Stream {
			// The vision call (DescribeImage) — simulate real vision-model
			// latency before answering, exactly what used to leave the
			// frontend blank for several seconds with nothing to show.
			time.Sleep(visionDelay)
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{{"message": map[string]interface{}{"content": visionDescription}}},
				"usage":   map[string]interface{}{"cost": 0.002},
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Fatalf("encoding fake vision response: %v", err)
			}
			return
		}

		// The main turn's streaming chat completion (and/or generateTitle/
		// generateSuggestions, which reuse this same fake server).
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", chunk)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWebSocket_ImageAttachment_ShowsSyntheticToolCallDuringVisionDelay is
// a full, real end-to-end simulation of the exact scenario reported: a
// user uploads a photo and the screen goes blank for several seconds while
// the vision model describes it, with nothing on the frontend to show for
// it. It drives the real HTTP upload endpoint, a real WebSocket
// connection, and a fake OpenRouter that actually sleeps for visionDelay
// on the vision call — then inspects wall-clock arrival times of the raw
// JSON events a browser would receive, not just the Go-level payloads
// resolveAttachment hands to emit().
//
// What this proves that the narrower resolveAttachment-level test doesn't:
//  1. The describe_image tool_call event reaches the client BEFORE the slow
//     vision call returns (arrives promptly after user_message, well under
//     visionDelay) — not batched up and flushed alongside the result.
//  2. The describe_image tool_result event's arrival is actually gated on
//     the vision call finishing (the observed gap is close to visionDelay,
//     not near-zero) — i.e. this is a real synthetic event around a real
//     slow call, not a coincidentally-adjacent pair of unrelated events.
//  3. The events round-trip through real JSON serialization over a real
//     socket with the right shape (tool, args.filename, result).
func TestWebSocket_ImageAttachment_ShowsSyntheticToolCallDuringVisionDelay(t *testing.T) {
	const visionDelay = 200 * time.Millisecond
	srv := fakeImageAwareLLMServer(t, "A red bicycle leaning against a brick wall.", visionDelay, "It looks like a red bicycle.")
	h := newTestHarness(t, srv.URL)

	uploadBody, contentType := multipartUploadBody(t, "bike.jpg", "image/jpeg", []byte("fake-jpeg-bytes-not-a-real-image"))
	uploadResp, err := http.Post(h.url("/api/upload"), contentType, uploadBody)
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer uploadResp.Body.Close()
	var uploaded UploadResponse
	if err := json.NewDecoder(uploadResp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decoding upload response: %v", err)
	}

	conn := dialWS(t, h)
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "what's in this photo?", "model": "mimo-pro",
		"attachment_id": uploaded.ID, "attachment_filename": "bike.jpg", "attachment_content_type": "image/jpeg",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	type timedEvent struct {
		at  time.Time
		evt map[string]interface{}
	}
	var events []timedEvent
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading event: %v (events so far: %+v)", err, events)
		}
		events = append(events, timedEvent{at: time.Now(), evt: evt})
		if evt["type"] == "done" || evt["type"] == "error" {
			break
		}
	}

	// Log the full timeline for a human to eyeball — this is the "blank
	// screen" gap made visible, or not.
	t.Logf("event timeline (t=0 at WriteJSON):")
	t0 := events[0].at
	for _, te := range events {
		t.Logf("  +%-8s %-14s tool=%-14v result_len=%d", te.at.Sub(t0).Round(time.Millisecond),
			te.evt["type"], te.evt["tool"], len(fmt.Sprint(te.evt["result"])))
	}

	userMsgIdx, callIdx, resultIdx := -1, -1, -1
	for i, te := range events {
		switch {
		case te.evt["type"] == "user_message" && userMsgIdx == -1:
			userMsgIdx = i
		case te.evt["type"] == "tool_call" && te.evt["tool"] == "describe_image" && callIdx == -1:
			callIdx = i
		case te.evt["type"] == "tool_result" && te.evt["tool"] == "describe_image" && resultIdx == -1:
			resultIdx = i
		}
	}
	if userMsgIdx == -1 {
		t.Fatalf("never saw a user_message event: %+v", events)
	}
	if callIdx == -1 {
		t.Fatalf("never saw a describe_image tool_call event — the synthetic event isn't firing: %+v", events)
	}
	if resultIdx == -1 {
		t.Fatalf("never saw a describe_image tool_result event: %+v", events)
	}
	if !(userMsgIdx < callIdx && callIdx < resultIdx) {
		t.Fatalf("event order = user_message@%d, tool_call@%d, tool_result@%d — want strictly increasing", userMsgIdx, callIdx, resultIdx)
	}

	args, _ := events[callIdx].evt["args"].(map[string]interface{})
	if args["filename"] != "bike.jpg" {
		t.Errorf("tool_call args = %+v, want filename %q", events[callIdx].evt["args"], "bike.jpg")
	}

	result, _ := events[resultIdx].evt["result"].(string)
	if !strings.Contains(result, "red bicycle") {
		t.Errorf("tool_result result = %q, want it to contain the vision model's description", result)
	}

	// The user_message -> tool_call gap should be near-instant: this is
	// the moment that used to be a silent multi-second wait with nothing
	// on screen. Now there should be something to show within
	// milliseconds, well before the vision call itself has even returned.
	toCall := events[callIdx].at.Sub(events[userMsgIdx].at)
	if toCall >= visionDelay/2 {
		t.Errorf("user_message -> tool_call gap = %v, want well under the %v vision delay — "+
			"the synthetic tool_call should appear immediately, before the slow call even starts", toCall, visionDelay)
	}

	// The tool_call -> tool_result gap should track the simulated vision
	// latency — proof this is wrapping the real slow call, not two events
	// that just happen to be emitted back-to-back.
	gap := events[resultIdx].at.Sub(events[callIdx].at)
	if gap < visionDelay/2 {
		t.Errorf("tool_call -> tool_result gap = %v, want at least ~%v (the simulated vision-model latency) — "+
			"if this is too small the tool_call fired only after DescribeImage already returned, not before it",
			gap, visionDelay)
	}

	last := events[len(events)-1]
	if last.evt["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done", last.evt)
	}
}
