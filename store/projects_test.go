package store

import (
	"errors"
	"strconv"
	"testing"
)

func TestProject_CreateGetListUpdate(t *testing.T) {
	s := openTestStore(t)

	p, err := s.CreateProject(Project{Name: "  Budget rebuild  ", Description: "Q4 numbers", ConstellationVisible: true})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID == "" || p.Name != "Budget rebuild" {
		t.Errorf("got %+v, want a generated id and a trimmed name", p)
	}
	if p.MemoryMode != ProjectMemoryDefault {
		t.Errorf("MemoryMode = %q, want the %q fallback", p.MemoryMode, ProjectMemoryDefault)
	}

	// A partial update must leave every field it doesn't name untouched —
	// including ones set to a non-default value at create time.
	instr := "Always answer in metric."
	updated, err := s.UpdateProject(p.ID, ProjectUpdate{CustomInstructions: &instr})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if updated.CustomInstructions != instr || updated.Description != "Q4 numbers" || !updated.ConstellationVisible {
		t.Errorf("partial update changed the wrong fields: %+v", updated)
	}

	// Clearing a field back to '' (inherit) must be expressible.
	model := "deepseek"
	if _, err := s.UpdateProject(p.ID, ProjectUpdate{DefaultModel: &model}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	empty := ""
	cleared, err := s.UpdateProject(p.ID, ProjectUpdate{DefaultModel: &empty})
	if err != nil || cleared.DefaultModel != "" {
		t.Errorf("clearing default_model: got %+v, err %v", cleared, err)
	}

	list, err := s.ListProjects()
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Errorf("ListProjects = %+v, err %v", list, err)
	}
}

func TestProject_Validation(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateProject(Project{Name: "   "}); err == nil {
		t.Error("CreateProject accepted a blank name")
	}
	if _, err := s.CreateProject(Project{Name: "x", MemoryMode: "project_scoped"}); err == nil {
		t.Error("CreateProject accepted the reserved, not-yet-functional project_scoped memory mode")
	}

	p, _ := s.CreateProject(Project{Name: "x"})
	bad := "bogus"
	if _, err := s.UpdateProject(p.ID, ProjectUpdate{MemoryMode: &bad}); err == nil {
		t.Error("UpdateProject accepted an unknown memory mode")
	}
	name := "y"
	if _, err := s.UpdateProject("no-such-id", ProjectUpdate{Name: &name}); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("UpdateProject on a missing id: err = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.GetProject("no-such-id"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetProject on a missing id: err = %v, want ErrProjectNotFound", err)
	}
}

func TestProject_ThreadMembershipAndCount(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.CreateProject(Project{Name: "p"})

	if err := s.CreateThread("t1", "one", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread("t2", "two", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadProject("t1", &p.ID); err != nil {
		t.Fatalf("SetThreadProject: %v", err)
	}

	th, err := s.GetThread("t1")
	if err != nil || th.ProjectID == nil || *th.ProjectID != p.ID {
		t.Fatalf("GetThread(t1).ProjectID = %v, err %v, want %q", th.ProjectID, err, p.ID)
	}
	if th2, _ := s.GetThread("t2"); th2.ProjectID != nil {
		t.Errorf("t2 should be ungrouped, got project %q", *th2.ProjectID)
	}

	threads, err := s.ListProjectThreads(p.ID)
	if err != nil || len(threads) != 1 || threads[0].ID != "t1" {
		t.Errorf("ListProjectThreads = %+v, err %v", threads, err)
	}
	if got, _ := s.GetProject(p.ID); got.ThreadCount != 1 {
		t.Errorf("ThreadCount = %d, want 1", got.ThreadCount)
	}

	// Moving out clears membership.
	if err := s.SetThreadProject("t1", nil); err != nil {
		t.Fatalf("SetThreadProject(nil): %v", err)
	}
	if th, _ := s.GetThread("t1"); th.ProjectID != nil {
		t.Errorf("t1 still in project after move-out: %q", *th.ProjectID)
	}

	// A stale picker id gets a clean not-found, not a raw FK error.
	missing := "gone"
	if err := s.SetThreadProject("t1", &missing); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("SetThreadProject to a missing project: err = %v, want ErrProjectNotFound", err)
	}
}

// DeleteProject is a real DELETE against a real FK — this pins that the
// orphan-then-delete sequence actually satisfies foreign_keys=on and leaves
// the thread intact rather than cascading it away.
func TestProject_DeleteOrphansThreads(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.CreateProject(Project{Name: "p"})
	if err := s.CreateThread("t1", "keep me", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadProject("t1", &p.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetProject(p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("project survived delete: err = %v", err)
	}
	th, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("thread was lost with its project: %v", err)
	}
	if th.ProjectID != nil || th.Title != "keep me" {
		t.Errorf("thread not cleanly orphaned: %+v", th)
	}
	if err := s.DeleteProject(p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("second DeleteProject: err = %v, want ErrProjectNotFound", err)
	}
}

// A fresh database gets threads.project_id from the schema constant, so the
// migration only ever really runs against an install that predates Projects.
// Simulate one: strip the column, rewind user_version to just before the
// migration, reopen, and confirm the ALTER ... REFERENCES actually applies.
func TestProject_MigrationAddsColumnToExistingDB(t *testing.T) {
	path := t.TempDir() + "/polaris.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE threads DROP COLUMN project_id`); err != nil {
		t.Fatalf("simulating a pre-Projects database: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = ` + strconv.Itoa(len(migrations)-1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a pre-Projects database: %v", err)
	}
	defer s.Close()
	p, _ := s.CreateProject(Project{Name: "p"})
	if err := s.CreateThread("t1", "t", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadProject("t1", &p.ID); err != nil {
		t.Fatalf("project_id column missing after migration: %v", err)
	}
}
