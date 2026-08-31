package store

import "testing"

func TestMemory_CreateGetListUpdateDelete(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateMemory("user-timezone", "user", "the user's timezone", "US/Pacific"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	if err := s.CreateMemory("feedback-terse", "feedback", "keep replies short", "the user prefers terse replies"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	m, err := s.GetMemory("user-timezone")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.Type != "user" || m.Description != "the user's timezone" || m.Content != "US/Pacific" {
		t.Errorf("GetMemory returned %+v, want the values just written", m)
	}

	entries, err := s.ListMemories()
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	// ListMemories orders by type then name — "feedback" sorts before "user".
	if entries[0].Name != "feedback-terse" || entries[1].Name != "user-timezone" {
		t.Errorf("entries in unexpected order: %+v", entries)
	}

	if err := s.UpdateMemory("user-timezone", "user", "the user's timezone", "US/Eastern"); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	m, err = s.GetMemory("user-timezone")
	if err != nil {
		t.Fatalf("GetMemory after update: %v", err)
	}
	if m.Content != "US/Eastern" {
		t.Errorf("Content = %q after update, want US/Eastern", m.Content)
	}

	if err := s.DeleteMemory("user-timezone"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if _, err := s.GetMemory("user-timezone"); err != ErrMemoryNotFound {
		t.Errorf("GetMemory after delete: err = %v, want ErrMemoryNotFound", err)
	}
}

func TestMemory_CreateDuplicateNameFails(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateMemory("dup", "project", "first", "content"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	if err := s.CreateMemory("dup", "project", "second", "other content"); err != ErrMemoryExists {
		t.Errorf("second CreateMemory: err = %v, want ErrMemoryExists", err)
	}
}

func TestMemory_UpdateOrDeleteMissingReturnsNotFound(t *testing.T) {
	s := openTestStore(t)

	if err := s.UpdateMemory("nope", "user", "d", "c"); err != ErrMemoryNotFound {
		t.Errorf("UpdateMemory on missing name: err = %v, want ErrMemoryNotFound", err)
	}
	if err := s.DeleteMemory("nope"); err != ErrMemoryNotFound {
		t.Errorf("DeleteMemory on missing name: err = %v, want ErrMemoryNotFound", err)
	}
}
