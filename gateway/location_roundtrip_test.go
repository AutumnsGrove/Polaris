package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"polaris/tools"
)

// test_location_probe is a dependency-free stand-in for nearby_search/
// weather, registered only for this test binary. Real location-hungry
// tools need a live network call (geocoding) after resolving a location,
// which would make this test flaky/slow for reasons that have nothing to
// do with the round trip itself — this exercises the exact same
// ResolveLocation -> RequestLocation path they use, without that.
func init() {
	tools.Register("test_location_probe", func(argsJSON string, ctx *tools.Context) string {
		ctx.Emit("tool_call", map[string]interface{}{"tool": "test_location_probe"})
		loc := ctx.ResolveLocation("")
		ctx.Emit("tool_result", map[string]interface{}{"tool": "test_location_probe", "result": loc})
		return loc
	})
}

// locationProbeLLMServer fakes a model that calls test_location_probe once,
// then answers — mirrors reasoningToolServer's two-round shape (see
// turn_reasoning_test.go) but with a plain, non-reasoning tool call.
func locationProbeLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	return sseToolCallServer(t, []string{
		`{"index":0,"id":"call_1","type":"function","function":{"name":"test_location_probe","arguments":"{}"}}`,
	})
}

// twoLocationProbesLLMServer fakes a model that calls test_location_probe
// *twice* in the same round — dispatchToolCallsConcurrently runs both at
// once (see agent/driver.go), the same shape a turn calling both
// nearby_search and weather would take.
func twoLocationProbesLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	return sseToolCallServer(t, []string{
		`{"index":0,"id":"call_1","type":"function","function":{"name":"test_location_probe","arguments":"{}"}}`,
		`{"index":1,"id":"call_2","type":"function","function":{"name":"test_location_probe","arguments":"{}"}}`,
	})
}

func sseToolCallServer(t *testing.T, toolCallsJSON []string) *httptest.Server {
	t.Helper()
	var reqCount int32

	round1 := []string{
		`data: {"choices":[{"delta":{"tool_calls":[` + strings.Join(toolCallsJSON, ",") + `]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}
	round2 := []string{
		`data: {"choices":[{"delta":{"content":"done"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := round1
		if n >= 2 {
			lines = round2
		}
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
}

// TestWebSocket_LocationRequestRoundTrip exercises the full path added for
// on-demand location: a tool call reaches ResolveLocation with no explicit
// location and no client-cached fallback, the server asks the browser for
// one via a "location_request" event, the (fake) browser answers over the
// same connection, and that exact value is what the tool call receives —
// proving ws.go's connLocationBroker and turn.go's requestLocation wiring
// actually connect end to end through the real handleWS goroutine, not
// just in isolation like location_broker_test.go's unit tests.
func TestWebSocket_LocationRequestRoundTrip(t *testing.T) {
	srv := locationProbeLLMServer(t)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "where am I", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// Read until the server asks for a location — deliberately not using
	// readEventsUntilDone here, since the turn is expected to still be
	// blocked waiting on our reply at this point.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var events []map[string]interface{}
	var requestThreadID string
	sawRequest := false
	for !sawRequest {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading events before location_request: %v (events so far: %+v)", err, events)
		}
		events = append(events, evt)
		switch evt["type"] {
		case "location_request":
			sawRequest = true
			requestThreadID, _ = evt["thread_id"].(string)
		case "done", "error":
			t.Fatalf("turn finished (%v) without ever asking for a location: %+v", evt["type"], events)
		}
	}
	if requestThreadID == "" {
		t.Errorf("location_request thread_id is empty, want the turn's resolved thread id")
	}

	const wantLocation = "47.6062, -122.3321"
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "location_response", "user_location": wantLocation,
	}); err != nil {
		t.Fatalf("WriteJSON (location_response): %v", err)
	}

	events = append(events, readEventsUntilDone(t, conn, 5*time.Second)...)

	var sawToolResult bool
	for _, e := range events {
		if e["type"] == "tool_result" && e["tool"] == "test_location_probe" {
			sawToolResult = true
			if e["result"] != wantLocation {
				t.Errorf("tool_result.result = %v, want %q (the round-tripped location)", e["result"], wantLocation)
			}
		}
	}
	if !sawToolResult {
		t.Fatalf("never saw test_location_probe's tool_result: %+v", events)
	}

	if last := events[len(events)-1]; last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done", last)
	}
}

// TestWebSocket_LocationRequestOnlyOncePerTurn covers turn.go's sync.Once
// wrapping: dispatchToolCallsConcurrently runs a turn's tool calls in
// parallel (see agent/driver.go), so a turn calling both nearby_search and
// weather without an explicit location could otherwise interrupt the
// browser for two separate GPS round trips in the same breath. Both calls
// here reach ResolveLocation at effectively the same instant; only one
// "location_request" should ever go out, and both calls should end up
// with the same answer.
func TestWebSocket_LocationRequestOnlyOncePerTurn(t *testing.T) {
	srv := twoLocationProbesLLMServer(t)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "where am I", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var events []map[string]interface{}
	sawRequest := false
	for !sawRequest {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading events before location_request: %v (events so far: %+v)", err, events)
		}
		events = append(events, evt)
		switch evt["type"] {
		case "location_request":
			sawRequest = true
		case "done", "error":
			t.Fatalf("turn finished (%v) without ever asking for a location: %+v", evt["type"], events)
		}
	}

	const wantLocation = "47.6062, -122.3321"
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "location_response", "user_location": wantLocation,
	}); err != nil {
		t.Fatalf("WriteJSON (location_response): %v", err)
	}

	events = append(events, readEventsUntilDone(t, conn, 5*time.Second)...)

	var requestCount, toolResultCount int
	for _, e := range events {
		if e["type"] == "location_request" {
			requestCount++
		}
		if e["type"] == "tool_result" && e["tool"] == "test_location_probe" {
			toolResultCount++
			if e["result"] != wantLocation {
				t.Errorf("tool_result[%d].result = %v, want %q", toolResultCount, e["result"], wantLocation)
			}
		}
	}
	if requestCount != 1 {
		t.Errorf("saw %d location_request events for 2 concurrent tool calls, want exactly 1", requestCount)
	}
	if toolResultCount != 2 {
		t.Fatalf("saw %d test_location_probe tool_results, want 2 (one per call): %+v", toolResultCount, events)
	}
}

// TestWebSocket_LocationRequestTimesOutIfClientNeverReplies covers the
// other side: a client that never answers (backgrounded tab, denied
// permission that never even sends an empty reply, dropped connection)
// must not hang the turn forever — it should fall back to
// DefaultLocation (empty here, since the test config sets none) once
// locationRequestTimeout passes, and the turn still completes normally.
func TestWebSocket_LocationRequestTimesOutIfClientNeverReplies(t *testing.T) {
	orig := locationRequestTimeout
	locationRequestTimeout = 200 * time.Millisecond
	defer func() { locationRequestTimeout = orig }()

	srv := locationProbeLLMServer(t)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "where am I", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	events := readEventsUntilDone(t, conn, 5*time.Second)

	var sawRequest, sawToolResult bool
	for _, e := range events {
		if e["type"] == "location_request" {
			sawRequest = true
		}
		if e["type"] == "tool_result" && e["tool"] == "test_location_probe" {
			sawToolResult = true
			// omitempty drops Result from the wire entirely when it's "",
			// which decodes back as a missing key (nil), not "" — both mean
			// "no location" here.
			if v, _ := e["result"].(string); v != "" {
				t.Errorf("tool_result.result = %v, want \"\" (no live fix, no configured default)", e["result"])
			}
		}
	}
	if !sawRequest {
		t.Fatalf("never saw a location_request event: %+v", events)
	}
	if !sawToolResult {
		t.Fatalf("never saw test_location_probe's tool_result: %+v", events)
	}
	if last := events[len(events)-1]; last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done — the turn should still finish after the location request times out", last)
	}
}
