package store

import (
	"testing"
)

func TestAddMessage_AccumulatesThreadCost(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := s.AddMessage("t1", "user", "hello", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage (user): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "hi there", "[]", "[]", 0.0025, ""); err != nil {
		t.Fatalf("AddMessage (assistant): %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CostUSD != 0.0025 {
		t.Errorf("thread.CostUSD = %v, want 0.0025", thread.CostUSD)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("message order/roles wrong: %+v", msgs)
	}
}

func TestSetMessageAttachments_RoundTrips(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "user", "see attached", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	want := `[{"filename":"a.pdf","content_type":"application/pdf","workspace_file_id":"abc123.pdf"},{"filename":"b.csv","content_type":"text/csv","workspace_file_id":"def456.csv"}]`
	if err := s.SetMessageAttachments(msgID, want); err != nil {
		t.Fatalf("SetMessageAttachments: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Attachments != want {
		t.Errorf("Attachments = %q, want %q", msgs[0].Attachments, want)
	}
}

func TestGetMessages_FallsBackToLegacyAttachmentColumns(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	// Simulates a row written before the attachments column existed:
	// only the three singular legacy columns are populated, never the
	// new array column.
	msgID, err := s.AddMessage("t1", "user", "see attached", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.SetMessageAttachment(msgID, "report.pdf", "application/pdf"); err != nil {
		t.Fatalf("SetMessageAttachment: %v", err)
	}
	if err := s.SetMessageWorkspaceFileID(msgID, "abc123.pdf"); err != nil {
		t.Fatalf("SetMessageWorkspaceFileID: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	want := `[{"filename":"report.pdf","content_type":"application/pdf","workspace_file_id":"abc123.pdf"}]`
	if msgs[0].Attachments != want {
		t.Errorf("Attachments = %q, want %q (synthesized from legacy columns)", msgs[0].Attachments, want)
	}
}

func TestGetMessages_NoAttachmentIsEmptyArray(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "hello", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].Attachments != "[]" {
		t.Errorf("Attachments = %q, want \"[]\" for a message with no upload", msgs[0].Attachments)
	}
}

func TestSetMessageDuration_RecordsElapsedTime(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	assistantID, err := s.AddMessage("t1", "assistant", "answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].DurationMs != 0 {
		t.Errorf("DurationMs before SetMessageDuration = %d, want 0", msgs[0].DurationMs)
	}

	if err := s.SetMessageDuration(assistantID, 4200); err != nil {
		t.Fatalf("SetMessageDuration: %v", err)
	}

	msgs, err = s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].DurationMs != 4200 {
		t.Errorf("DurationMs = %d, want 4200", msgs[0].DurationMs)
	}
}

func TestSetMessageCallStats_RoundTrips(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	assistantID, err := s.AddMessage("t1", "assistant", "answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.SetMessageCallStats(assistantID, 16123, 3); err != nil {
		t.Fatalf("SetMessageCallStats: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].LastPromptTokens != 16123 || msgs[0].LLMCalls != 3 {
		t.Errorf("GetMessages: LastPromptTokens=%d LLMCalls=%d, want 16123/3", msgs[0].LastPromptTokens, msgs[0].LLMCalls)
	}
	got, err := s.GetMessageByID(assistantID)
	if err != nil {
		t.Fatalf("GetMessageByID: %v", err)
	}
	if got.LastPromptTokens != 16123 || got.LLMCalls != 3 {
		t.Errorf("GetMessageByID: LastPromptTokens=%d LLMCalls=%d, want 16123/3", got.LastPromptTokens, got.LLMCalls)
	}
}

// Issue #107's thread-level hit % is summed from per-message rows on read,
// so a fork has to carry its shared prefix's usage along with the messages
// themselves — otherwise an edited thread's hit % would silently reset to
// only what the fork itself generated.
func TestThreadCacheUsage_SumsMessagesAndSurvivesFork(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i, usage := range [][2]int{{1000, 0}, {3000, 1000}} {
		if _, err := s.AddMessage("root", "user", "q", "[]", "[]", 0, ""); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		id, err := s.AddMessage("root", "assistant", "a", "[]", "[]", 0, "")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if err := s.SetMessageCacheUsage(id, usage[0], usage[1]); err != nil {
			t.Fatalf("SetMessageCacheUsage %d: %v", i, err)
		}
	}

	prompt, cached, err := s.ThreadCacheUsage("root")
	if err != nil {
		t.Fatalf("ThreadCacheUsage: %v", err)
	}
	if prompt != 4000 || cached != 1000 {
		t.Fatalf("ThreadCacheUsage(root) = %d/%d, want 4000/1000", prompt, cached)
	}

	// Fork before the second answer: only the first turn's usage carries.
	forkID, err := s.ForkThread("root", "root", 3)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	prompt, cached, err = s.ThreadCacheUsage(forkID)
	if err != nil {
		t.Fatalf("ThreadCacheUsage(fork): %v", err)
	}
	if prompt != 1000 || cached != 0 {
		t.Fatalf("ThreadCacheUsage(fork) = %d/%d, want 1000/0", prompt, cached)
	}
}
