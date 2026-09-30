package store

import "testing"

func TestFieldMemory_ScopedToItsField(t *testing.T) {
	s := openTestStore(t)
	a, _ := s.CreateField(Field{Name: "a"})
	b, _ := s.CreateField(Field{Name: "b"})

	// Same slug in both fields and in the global list: three independent rows.
	if err := s.CreateMemory("tz", "user", "global tz", "UTC", ""); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	if err := s.CreateFieldMemory(a.ID, "tz", "user", "a tz", "EST", ""); err != nil {
		t.Fatalf("CreateFieldMemory(a): %v", err)
	}
	if err := s.CreateFieldMemory(b.ID, "tz", "user", "b tz", "PST", ""); err != nil {
		t.Fatalf("CreateFieldMemory(b): %v", err)
	}
	if err := s.CreateFieldMemory(a.ID, "tz", "user", "dup", "x", ""); err != ErrMemoryExists {
		t.Errorf("duplicate within a field = %v, want ErrMemoryExists", err)
	}

	got, err := s.GetFieldMemory(a.ID, "tz")
	if err != nil || got.Content != "EST" {
		t.Errorf("GetFieldMemory(a) = %+v, %v; want EST", got, err)
	}
	if g, _ := s.GetMemory("tz"); g == nil || g.Content != "UTC" {
		t.Errorf("global memory changed by field writes: %+v", g)
	}

	// Neither field sees the other's entries, and the global list never
	// gains a field's.
	if list, _ := s.ListFieldMemories(a.ID); len(list) != 1 || list[0].Description != "a tz" {
		t.Errorf("ListFieldMemories(a) = %+v, want only a's entry", list)
	}
	if list, _ := s.ListMemories(); len(list) != 1 || list[0].Description != "global tz" {
		t.Errorf("ListMemories = %+v, want only the global entry", list)
	}
	if _, err := s.GetFieldMemory(a.ID, "nope"); err != ErrMemoryNotFound {
		t.Errorf("GetFieldMemory(missing) = %v, want ErrMemoryNotFound", err)
	}
}

func TestFieldMemory_EditForgetRevive(t *testing.T) {
	s := openTestStore(t)
	f, _ := s.CreateField(Field{Name: "f"})
	if err := s.CreateFieldMemory(f.ID, "n", "project", "desc", "body", "2026-09-30"); err != nil {
		t.Fatal(err)
	}

	// Partial edit: only description changes; everything else is kept.
	if err := s.UpdateFieldMemory(f.ID, "n", "", "new desc", "", ""); err != nil {
		t.Fatalf("UpdateFieldMemory: %v", err)
	}
	m, _ := s.GetFieldMemory(f.ID, "n")
	if m.Description != "new desc" || m.Content != "body" || m.Type != "project" || m.OccurredAt != "2026-09-30" {
		t.Errorf("after partial edit: %+v", m)
	}

	if err := s.DeleteFieldMemory(f.ID, "n"); err != nil {
		t.Fatalf("DeleteFieldMemory: %v", err)
	}
	if _, err := s.GetFieldMemory(f.ID, "n"); err != ErrMemoryNotFound {
		t.Errorf("forgotten memory still readable: %v", err)
	}
	if err := s.UpdateFieldMemory(f.ID, "n", "", "x", "", ""); err != ErrMemoryNotFound {
		t.Errorf("editing a forgotten memory = %v, want ErrMemoryNotFound", err)
	}
	if err := s.DeleteFieldMemory(f.ID, "n"); err != ErrMemoryNotFound {
		t.Errorf("double forget = %v, want ErrMemoryNotFound", err)
	}
	// A forgotten name is free again.
	if err := s.CreateFieldMemory(f.ID, "n", "user", "again", "back", ""); err != nil {
		t.Fatalf("reviving forgotten name: %v", err)
	}
	if m, _ := s.GetFieldMemory(f.ID, "n"); m.Content != "back" {
		t.Errorf("revived memory = %+v", m)
	}
}

func TestFieldMemory_DeletedWithItsField(t *testing.T) {
	s := openTestStore(t)
	f, _ := s.CreateField(Field{Name: "f"})
	other, _ := s.CreateField(Field{Name: "other"})
	_ = s.CreateFieldMemory(f.ID, "n", "user", "d", "c", "")
	_ = s.CreateFieldMemory(other.ID, "n", "user", "d", "c", "")

	if err := s.DeleteField(f.ID); err != nil {
		t.Fatalf("DeleteField: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM field_memories WHERE field_id = ?`, f.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("deleted field left %d memory rows (err %v)", n, err)
	}
	if list, _ := s.ListFieldMemories(other.ID); len(list) != 1 {
		t.Errorf("deleting one field touched another's memories: %+v", list)
	}
}

func TestValidFieldMemoryMode(t *testing.T) {
	for _, m := range []string{"default", "field_only", "both", "none"} {
		if !ValidFieldMemoryMode(m) {
			t.Errorf("%q should be valid", m)
		}
	}
	for _, m := range []string{"", "field_scoped", "project_scoped", "all"} {
		if ValidFieldMemoryMode(m) {
			t.Errorf("%q should be invalid", m)
		}
	}
}
