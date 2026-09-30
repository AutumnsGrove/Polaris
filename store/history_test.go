package store

import (
	"fmt"
	"strings"
	"testing"
)

// TestReadThread_CompactionSubstitutionMatchesEffectiveHistory pins
// ReadThread to the exact same compaction-substitution behavior loadHistory
// gets via EffectiveHistory — a compacted thread should show the same
// collapsed summary-plus-tail view here as a resumed live thread would, not
// a raw dump of every message the summary was supposed to replace.
func TestReadThread_CompactionSubstitutionMatchesEffectiveHistory(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "a thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	id1, err := s.AddMessage("t1", "user", "message that gets compacted away", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage(1): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "message that survives", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(2): %v", err)
	}
	if err := s.CompactThread("t1", "summary of the early part of the conversation", id1, 0, 0); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	result, err := s.ReadThread("t1")
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if strings.Contains(result.Content, "message that gets compacted away") {
		t.Errorf("Content = %q, want the compacted-away message replaced by the summary, not present verbatim", result.Content)
	}
	if !strings.Contains(result.Content, "summary of the early part of the conversation") {
		t.Errorf("Content = %q, want the compaction summary included", result.Content)
	}
	if !strings.Contains(result.Content, "message that survives") {
		t.Errorf("Content = %q, want the post-compaction message included", result.Content)
	}
}

// TestEffectiveHistory_IncludesPendingQuestionOptions guards against the bug
// filed as polaris#70: an ask_user_question call's suggested options are
// persisted in Message.PendingQuestion, a separate column from Content, but
// EffectiveHistory (and therefore both loadHistory's live turns and
// search_chats' ReadThread) used to build history from Content alone. A user
// answering "a combo of 1, 2, and 3" instead of retyping the options left
// the model with no record of what it had actually offered.
func TestEffectiveHistory_IncludesPendingQuestionOptions(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "a thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "help me pick a game", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	assistantID, err := s.AddMessage("t1", "assistant", "What kind of game are you in the mood for?", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}
	pendingJSON := `{"question":"What kind of game are you in the mood for?","options":["Puzzle","Strategy","Cozy"]}`
	if err := s.SetMessagePendingQuestion(assistantID, pendingJSON); err != nil {
		t.Fatalf("SetMessagePendingQuestion: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "a combo of 1 and 3", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(reply): %v", err)
	}

	thread, err := s.GetThreadRaw("t1")
	if err != nil {
		t.Fatalf("GetThreadRaw: %v", err)
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	entries := EffectiveHistory(thread, msgs, 0)

	var assistantEntry *HistoryEntry
	for i := range entries {
		if entries[i].Role == "assistant" {
			assistantEntry = &entries[i]
			break
		}
	}
	if assistantEntry == nil {
		t.Fatalf("no assistant entry in history: %+v", entries)
	}
	for _, want := range []string{"Puzzle", "Strategy", "Cozy"} {
		if !strings.Contains(assistantEntry.Content, want) {
			t.Errorf("assistant history Content = %q, want it to include offered option %q", assistantEntry.Content, want)
		}
	}
}

// TestEffectiveHistory_IncludesCitedSources: a follow-up turn used to see
// only the prior answer's prose, never the source list the user saw under
// it — the model then doubted its own cited answer and re-ran searches it
// had already done. Also checks the list is capped (citations include
// every search hit) and never attached to a user message.
func TestEffectiveHistory_IncludesCitedSources(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "a thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	var cits []string
	for i := 0; i < maxHistorySources+3; i++ {
		cits = append(cits, fmt.Sprintf(`{"title":"Source %d","url":"https://example.com/%d"}`, i, i))
	}
	if _, err := s.AddMessage("t1", "user", "q", `[{"title":"x","url":"https://user.example"}]`, "[]", 0, "turn1"); err != nil {
		t.Fatalf("AddMessage(user): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "answer", "["+strings.Join(cits, ",")+"]", "[]", 0, "turn1"); err != nil {
		t.Fatalf("AddMessage(assistant): %v", err)
	}
	thread, err := s.GetThreadRaw("t1")
	if err != nil {
		t.Fatalf("GetThreadRaw: %v", err)
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	entries := EffectiveHistory(thread, msgs, 0)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Content != "q" {
		t.Errorf("user entry = %q, want it untouched", entries[0].Content)
	}
	got := entries[1].Content
	if !strings.HasPrefix(got, "answer\n\n[Polaris note") {
		t.Errorf("assistant entry = %q, want the answer followed by the sources note", got)
	}
	if !strings.Contains(got, "Source 0 — https://example.com/0") || strings.Contains(got, fmt.Sprintf("example.com/%d\n", maxHistorySources)) {
		t.Errorf("assistant entry = %q, want the first %d sources only", got, maxHistorySources)
	}
	if !strings.Contains(got, "...and 3 more") {
		t.Errorf("assistant entry = %q, want the overflow counted", got)
	}
	if entries[1].TurnID != "turn1" {
		t.Errorf("TurnID = %q, want turn1", entries[1].TurnID)
	}
}

// TestStripFakeSourcesNote: seen live on a real thread with
// full_turn_history on — once the model had seen appendCitedSources'
// injected note a few times in its own replayed history, it imitated the
// exact format in its own answer despite prompt.md telling it not to.
// This is the mechanical backstop: strip a trailing block matching that
// format before the answer is ever stored or replayed again.
func TestStripFakeSourcesNote(t *testing.T) {
	clean := "This is a perfectly normal answer with [a real link](https://example.com) in it."
	if got := StripFakeSourcesNote(clean); got != clean {
		t.Errorf("StripFakeSourcesNote(%q) = %q, want it untouched", clean, got)
	}

	faked := clean + polarisNoteMarker + "- Some Source — https://example.com/fake\n]"
	if got := StripFakeSourcesNote(faked); got != clean {
		t.Errorf("StripFakeSourcesNote(%q) = %q, want %q", faked, got, clean)
	}

	// A real injected note (the one appendCitedSources itself produces)
	// must also come off cleanly — this is what a model-generated
	// imitation looks like once it's fed back in as this turn's own
	// history for the turn after it.
	withRealNote := appendCitedSources("The real answer.", `[{"title":"X","url":"https://example.com/x"}]`)
	if got := StripFakeSourcesNote(withRealNote); got != "The real answer." {
		t.Errorf("StripFakeSourcesNote(%q) = %q, want the note stripped back off", withRealNote, got)
	}
}
