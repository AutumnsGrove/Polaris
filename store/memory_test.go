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

func TestMemory_UpdatePartialFieldsLeavesOthersUnchanged(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateMemory("partial", "user", "original description", "original content"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	// Empty type/content: only description changes.
	if err := s.UpdateMemory("partial", "", "new description", ""); err != nil {
		t.Fatalf("UpdateMemory (description only): %v", err)
	}
	m, err := s.GetMemory("partial")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.Type != "user" || m.Description != "new description" || m.Content != "original content" {
		t.Errorf("GetMemory returned %+v, want type/content untouched and description updated", m)
	}

	// Empty type/description: only content changes — the shape a second,
	// independent edit call (e.g. from a concurrent tool dispatch batch)
	// would use.
	if err := s.UpdateMemory("partial", "", "", "new content"); err != nil {
		t.Fatalf("UpdateMemory (content only): %v", err)
	}
	m, err = s.GetMemory("partial")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.Description != "new description" || m.Content != "new content" {
		t.Errorf("GetMemory returned %+v, want description untouched from the prior update and content updated", m)
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

// TestMemory_DeleteIsSoftAndExcludesEverywhere guards the actual soft-delete
// contract: the row survives (a real DELETE would make it un-revivable),
// but is unreachable through GetMemory/ListMemories/ListMemoriesFull, and a
// second forget of the same name reports not-found rather than a silent
// no-op success.
func TestMemory_DeleteIsSoftAndExcludesEverywhere(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateMemory("temp", "project", "d", "c"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	if err := s.DeleteMemory("temp"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	if _, err := s.GetMemory("temp"); err != ErrMemoryNotFound {
		t.Errorf("GetMemory after delete: err = %v, want ErrMemoryNotFound", err)
	}
	if entries, err := s.ListMemories(); err != nil || len(entries) != 0 {
		t.Errorf("ListMemories after delete = %+v, err %v, want empty", entries, err)
	}
	if full, err := s.ListMemoriesFull(); err != nil || len(full) != 0 {
		t.Errorf("ListMemoriesFull after delete = %+v, err %v, want empty", full, err)
	}
	if err := s.UpdateMemory("temp", "project", "new", "new"); err != ErrMemoryNotFound {
		t.Errorf("UpdateMemory on a disabled name: err = %v, want ErrMemoryNotFound", err)
	}

	// The row must still physically exist (soft delete, not a real
	// DELETE) — otherwise there'd be nothing for CreateMemory to revive.
	var disabled int
	if err := s.db.QueryRow(`SELECT disabled FROM memories WHERE name = 'temp'`).Scan(&disabled); err != nil {
		t.Fatalf("row is gone entirely, want a soft-deleted row to survive: %v", err)
	}
	if disabled != 1 {
		t.Errorf("disabled = %d, want 1", disabled)
	}

	// A second forget of an already-forgotten name is not-found, not a
	// silent success — there's nothing active left to forget.
	if err := s.DeleteMemory("temp"); err != ErrMemoryNotFound {
		t.Errorf("second DeleteMemory: err = %v, want ErrMemoryNotFound", err)
	}
}

// TestMemory_CreateRevivesForgottenName is the other half of the soft-delete
// contract: a name freed up by forgetting a memory must be reusable, not
// permanently blocked by a disabled row still occupying the primary key.
func TestMemory_CreateRevivesForgottenName(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateMemory("reused", "user", "original", "original content"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	if err := s.DeleteMemory("reused"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	if err := s.CreateMemory("reused", "project", "brand new", "brand new content"); err != nil {
		t.Fatalf("CreateMemory (revival): %v", err)
	}
	m, err := s.GetMemory("reused")
	if err != nil {
		t.Fatalf("GetMemory after revival: %v", err)
	}
	if m.Type != "project" || m.Description != "brand new" || m.Content != "brand new content" {
		t.Errorf("GetMemory after revival = %+v, want the freshly written values, not the forgotten ones", m)
	}
}
