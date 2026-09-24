package gateway

import (
	"strings"
	"testing"
	"time"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/store"
)

// TestCompactThread_EndsOnAUserTurn is the regression test for the bug that
// made auto-compaction fail silently for its entire existence. The prompt it
// builds is [system] + the whole conversation, and the conversation always
// ends on an assistant message (a turn's own answer is the last thing
// persisted before that turn triggers compaction). Handed an array ending
// there, the model reads it as its own turn to continue rather than a thing
// to summarize and returns essentially nothing — measured live against
// deepseek-v4.1-flash: 1 character of content ending on an assistant turn,
// a real summary once a trailing user turn was appended. The empty response
// then tripped compactThread's empty-summary guard, so the thread never
// compacted and nothing was ever announced: the failure looked exactly like
// "it didn't fire". generateTitle hit the same wall and appends
// title_regenerate_task for the same reason.
func TestCompactThread_EndsOnAUserTurn(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := h.db.AddMessage("t1", "user", "what is the tallest mountain in Japan?", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	msgID, err := h.db.AddMessage("t1", "assistant", "Mount Fuji, at 3,776 m.", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "a summary of the exchange"}}},
	}
	s := &Server{db: h.db}
	if _, _, err := s.compactThread(mock, "t1", msgID); err != nil {
		t.Fatalf("compactThread: %v", err)
	}
	if len(mock.Calls) != 1 {
		t.Fatalf("got %d LLM calls, want 1", len(mock.Calls))
	}

	msgs := mock.Calls[0].Messages
	if len(msgs) < 2 {
		t.Fatalf("prompt has %d messages, want at least a system turn and the history", len(msgs))
	}
	// The hazard this guards is real, not hypothetical: the message right
	// before the task turn is the assistant's own answer. If that ever stops
	// being true, this test's premise has changed and the task turn below
	// may no longer be needed — better to fail here than to keep a
	// once-meaningful prompt fragment around on faith.
	if prev := msgs[len(msgs)-2]; prev.Role != "assistant" {
		t.Errorf("second-to-last message role = %q, want %q — the history is expected to end on the assistant's answer", prev.Role, "assistant")
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" {
		t.Errorf("last message role = %q, want %q — a prompt ending on the assistant turn makes the model continue it and return no usable summary", last.Role, "user")
	} else if strings.TrimSpace(last.Content) == "" {
		t.Error("trailing user task turn is empty, so it cannot redirect the model")
	}
}

// TestTryBeginCompaction_SerializesPerThread covers the race that
// detaching compaction introduces (F5 in docs/plans/auto-compaction.md):
// two turns arriving close together on a thread that has just crossed the
// threshold each decide, independently, that it needs compacting. Without
// a claim, both fire a summarization call, both write CompactThread, and
// the second summarizes a history the first has already begun replacing.
func TestTryBeginCompaction_SerializesPerThread(t *testing.T) {
	s := &Server{compactingThreads: make(map[string]bool)}

	if !s.tryBeginCompaction("t1") {
		t.Fatal("first tryBeginCompaction on t1 = false, want true")
	}
	if s.tryBeginCompaction("t1") {
		t.Error("second tryBeginCompaction on t1 = true, want false — a second summarization must not start")
	}

	// The claim is per-thread: one thread compacting must never suppress
	// an unrelated thread's compaction.
	if !s.tryBeginCompaction("t2") {
		t.Error("tryBeginCompaction on t2 = false while t1 is claimed, want true")
	}

	// Released after the call finishes, the slot is usable again — the
	// guard must cause a one-turn lag, not permanently disable compaction
	// for a thread that just ran one.
	s.endCompaction("t1")
	if !s.tryBeginCompaction("t1") {
		t.Error("tryBeginCompaction on t1 after endCompaction = false, want true")
	}
}

// TestWebSocket_SurfacesPendingCompactionNotice is the end-to-end half of
// the detached-compaction design, and the assertion that actually pins
// live/replay agreement: a compaction that finished while nobody was
// watching is announced at the top of the thread's NEXT turn, tagged with
// that turn's own turn_id so the live event and the row a reload replays
// land on the same turn's timeline.
//
// The notice is armed directly through the store rather than by driving a
// real compaction, deliberately: forcing one would mean either a huge
// fake context count or a tiny context_window_tokens, and either way would
// entangle what's being tested (the delivery handoff) with how compaction
// decides to fire (already covered by TestCompactThread and friends). The
// state armed here is byte-for-byte what CompactThread leaves behind.
func TestWebSocket_SurfacesPendingCompactionNotice(t *testing.T) {
	srv := fakeLLMServer(t, "any", "an answer")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "first question", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	firstEvents := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := firstEvents[len(firstEvents)-1]["thread_id"].(string)
	if threadID == "" {
		t.Fatalf("no thread_id in the first turn's events: %+v", firstEvents)
	}

	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var lastMsgID int64
	for _, m := range msgs {
		if m.ID > lastMsgID {
			lastMsgID = m.ID
		}
	}
	if err := h.db.CompactThread(threadID, "summary of the earlier exchange", lastMsgID, 0.007, 50); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "a follow-up question", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON (second turn): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)

	// The live frame: what a client with the thread open sees.
	var noticed map[string]interface{}
	firstTokenAt := -1
	for i, e := range events {
		if e["type"] == "compacted" && noticed == nil {
			noticed = e
		}
		if e["type"] == "token" && firstTokenAt < 0 {
			firstTokenAt = i
		}
	}
	if noticed == nil {
		t.Fatalf("no 'compacted' event on the turn after a compaction; events: %+v", events)
	}
	if got, _ := noticed["content"].(string); got != "summary of the earlier exchange" {
		t.Errorf("compacted event content = %q, want the summary", got)
	}
	// The cost is the whole reason this event carries cost_usd at all: it
	// belongs to no "done" event, so without it the frontend's running
	// session total silently misses the summarization call's spend.
	if got, _ := noticed["cost_usd"].(float64); got != 0.007 {
		t.Errorf("compacted event cost_usd = %v, want 0.007 (the compaction's own cost)", got)
	}
	// Announced at the TOP of the turn, not appended once the answer was
	// ready — the history this turn was built from is already the compacted
	// one, and the note should read that way.
	if idx := indexOfEvent(events, noticed); firstTokenAt >= 0 && idx > firstTokenAt {
		t.Errorf("compacted event arrived at index %d, after the first token at %d — want it at the top of the turn", idx, firstTokenAt)
	}

	// The notice must have been consumed, not merely read — a notice left
	// armed would be announced again (and the cost charged again) on the
	// turn after this one.
	if _, _, ok, err := h.db.TakeCompactionNotice(threadID); err != nil {
		t.Fatalf("TakeCompactionNotice: %v", err)
	} else if ok {
		t.Error("notice still pending after the next turn ran, want it consumed")
	}

	// The replay half: buildTimelineFromEvents is fed only a turn's own
	// event slice, so this row must carry a turn_id or a reloaded thread
	// shows no compaction note at all.
	stored, err := h.db.ListEvents(threadID, 500)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var found *store.Event
	for i := range stored {
		if stored[i].Source == "compaction" && stored[i].Message == "compaction notice shown" {
			found = &stored[i]
		}
	}
	if found == nil {
		t.Fatal(`no "compaction notice shown" event logged — a reload would show no compaction note for this turn`)
	}
	if found.TurnID == "" {
		t.Error("notice event has an empty turn_id, so a reloaded thread would not render it")
	}
	if !strings.Contains(found.Data, "summary of the earlier exchange") {
		t.Errorf("event data = %q, want it to carry the summary", found.Data)
	}
	// Deliberately no count assertion on "thread auto-compacted" audit rows
	// here: whether turn 2 itself tripped the threshold depends on the fake
	// LLM's reported usage, which this test has no business pinning.
}

// indexOfEvent finds an event's position in a turn's event slice by
// identity, for the ordering assertion above.
func indexOfEvent(events []map[string]interface{}, target map[string]interface{}) int {
	for i, e := range events {
		if e["type"] == target["type"] && e["content"] == target["content"] {
			return i
		}
	}
	return -1
}
