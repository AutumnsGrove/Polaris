package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestField_CreateGetListUpdate(t *testing.T) {
	s := openTestStore(t)

	p, err := s.CreateField(Field{Name: "  Budget rebuild  ", Description: "Q4 numbers", ConstellationVisible: true})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if p.ID == "" || p.Name != "Budget rebuild" {
		t.Errorf("got %+v, want a generated id and a trimmed name", p)
	}
	if p.MemoryMode != FieldMemoryDefault {
		t.Errorf("MemoryMode = %q, want the %q fallback", p.MemoryMode, FieldMemoryDefault)
	}

	// A partial update must leave every field it doesn't name untouched —
	// including ones set to a non-default value at create time.
	instr := "Always answer in metric."
	updated, err := s.UpdateField(p.ID, FieldUpdate{CustomInstructions: &instr})
	if err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	if updated.CustomInstructions != instr || updated.Description != "Q4 numbers" || !updated.ConstellationVisible {
		t.Errorf("partial update changed the wrong fields: %+v", updated)
	}

	// Clearing a field back to '' (inherit) must be expressible.
	model := "deepseek"
	if _, err := s.UpdateField(p.ID, FieldUpdate{DefaultModel: &model}); err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	empty := ""
	cleared, err := s.UpdateField(p.ID, FieldUpdate{DefaultModel: &empty})
	if err != nil || cleared.DefaultModel != "" {
		t.Errorf("clearing default_model: got %+v, err %v", cleared, err)
	}

	list, err := s.ListFields()
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Errorf("ListFields = %+v, err %v", list, err)
	}
}

func TestField_Validation(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateField(Field{Name: "   "}); err == nil {
		t.Error("CreateField accepted a blank name")
	}
	if _, err := s.CreateField(Field{Name: "x", MemoryMode: "field_scoped"}); err == nil {
		t.Error("CreateField accepted the reserved, not-yet-functional field_scoped memory mode")
	}

	p, _ := s.CreateField(Field{Name: "x"})
	bad := "bogus"
	if _, err := s.UpdateField(p.ID, FieldUpdate{MemoryMode: &bad}); err == nil {
		t.Error("UpdateField accepted an unknown memory mode")
	}
	name := "y"
	if _, err := s.UpdateField("no-such-id", FieldUpdate{Name: &name}); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("UpdateField on a missing id: err = %v, want ErrFieldNotFound", err)
	}
	if _, err := s.GetField("no-such-id"); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("GetField on a missing id: err = %v, want ErrFieldNotFound", err)
	}
}

func TestField_ThreadMembershipAndCount(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.CreateField(Field{Name: "p"})

	if err := s.CreateThread("t1", "one", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread("t2", "two", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadField("t1", &p.ID); err != nil {
		t.Fatalf("SetThreadField: %v", err)
	}

	th, err := s.GetThread("t1")
	if err != nil || th.FieldID == nil || *th.FieldID != p.ID {
		t.Fatalf("GetThread(t1).FieldID = %v, err %v, want %q", th.FieldID, err, p.ID)
	}
	if th2, _ := s.GetThread("t2"); th2.FieldID != nil {
		t.Errorf("t2 should be ungrouped, got field %q", *th2.FieldID)
	}

	threads, err := s.ListFieldThreads(p.ID)
	if err != nil || len(threads) != 1 || threads[0].ID != "t1" {
		t.Errorf("ListFieldThreads = %+v, err %v", threads, err)
	}
	if got, _ := s.GetField(p.ID); got.ThreadCount != 1 {
		t.Errorf("ThreadCount = %d, want 1", got.ThreadCount)
	}

	// Moving out clears membership.
	if err := s.SetThreadField("t1", nil); err != nil {
		t.Fatalf("SetThreadField(nil): %v", err)
	}
	if th, _ := s.GetThread("t1"); th.FieldID != nil {
		t.Errorf("t1 still in field after move-out: %q", *th.FieldID)
	}

	// A stale picker id gets a clean not-found, not a raw FK error.
	missing := "gone"
	if err := s.SetThreadField("t1", &missing); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("SetThreadField to a missing field: err = %v, want ErrFieldNotFound", err)
	}
}

// DeleteField is a real DELETE against a real FK — this pins that the
// orphan-then-delete sequence actually satisfies foreign_keys=on and leaves
// the thread intact rather than cascading it away.
func TestField_DeleteOrphansThreads(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.CreateField(Field{Name: "p"})
	if err := s.CreateThread("t1", "keep me", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadField("t1", &p.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteField(p.ID); err != nil {
		t.Fatalf("DeleteField: %v", err)
	}
	if _, err := s.GetField(p.ID); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("field survived delete: err = %v", err)
	}
	th, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("thread was lost with its field: %v", err)
	}
	if th.FieldID != nil || th.Title != "keep me" {
		t.Errorf("thread not cleanly orphaned: %+v", th)
	}
	if err := s.DeleteField(p.ID); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("second DeleteField: err = %v, want ErrFieldNotFound", err)
	}
}

// A fresh database gets threads.field_id from the schema constant, so the
// migration only ever really runs against an install that predates Fields.
// Simulate one: strip the column, rewind user_version to just before the
// migration, reopen, and confirm the ALTER ... REFERENCES actually applies.
func TestField_MigrationAddsColumnToExistingDB(t *testing.T) {
	path := t.TempDir() + "/polaris.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE threads DROP COLUMN field_id`); err != nil {
		t.Fatalf("simulating a pre-Fields database: %v", err)
	}
	// Located by content, not assumed to be the last entry: migrations are
	// append-only, so anything added after field_id would otherwise make a
	// hardcoded len(migrations)-1 rewind to the wrong slot. Later
	// migrations re-running against columns the fresh schema already has
	// just hit applyMigrations' tolerated "duplicate column" skip.
	fieldIDMigration := -1
	for i, m := range migrations {
		if strings.Contains(m, "threads ADD COLUMN field_id") {
			fieldIDMigration = i
		}
	}
	if fieldIDMigration < 0 {
		t.Fatal("threads.field_id migration not found in migrations")
	}
	if _, err := s.db.Exec(`PRAGMA user_version = ` + strconv.Itoa(fieldIDMigration)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a pre-Fields database: %v", err)
	}
	defer s.Close()
	p, _ := s.CreateField(Field{Name: "p"})
	if err := s.CreateThread("t1", "t", "m", "web"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadField("t1", &p.ID); err != nil {
		t.Fatalf("field_id column missing after migration: %v", err)
	}
}

func searchHits(t *testing.T, s *Store, q string) map[string]bool {
	t.Helper()
	res, err := s.SearchMessages(q, 20)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range res {
		ids[r.ThreadID] = true
	}
	return ids
}

// The exclusion is joined at query time, so it must follow a field's
// setting and a thread's membership live — and hold for an edited thread
// whose real content sits in a hidden fork (which carries no field_id).
func TestField_ExcludeFromChatSearch(t *testing.T) {
	s := openTestStore(t)
	private, _ := s.CreateField(Field{Name: "private", ExcludeFromChatSearch: true})
	open, _ := s.CreateField(Field{Name: "open"})

	mk := func(id string, field *string) {
		t.Helper()
		if err := s.CreateThread(id, id, "m", "web"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddMessage(id, "user", "quetzalcoatl migration notes", "[]", "[]", 0, ""); err != nil {
			t.Fatal(err)
		}
		if field != nil {
			if err := s.SetThreadField(id, field); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("in-private", &private.ID)
	mk("in-open", &open.ID)
	mk("ungrouped", nil)

	hits := searchHits(t, s, "quetzalcoatl")
	if hits["in-private"] || !hits["in-open"] || !hits["ungrouped"] {
		t.Errorf("hits = %v, want everything except the excluded field's thread", hits)
	}

	// Edited thread: content now lives in a fork variant with no field_id.
	fork, err := s.ForkThread("in-private", "in-private", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(fork, "user", "quetzalcoatl revised", "[]", "[]", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActiveVariant("in-private", fork); err != nil {
		t.Fatal(err)
	}
	if searchHits(t, s, "quetzalcoatl")["in-private"] {
		t.Error("an edited thread leaked out of an excluded field via its hidden fork")
	}

	// Live follow-through: flip the setting off, then move the thread out.
	off := false
	if _, err := s.UpdateField(private.ID, FieldUpdate{ExcludeFromChatSearch: &off}); err != nil {
		t.Fatal(err)
	}
	if !searchHits(t, s, "quetzalcoatl")["in-private"] {
		t.Error("turning the exclusion off didn't restore the thread to search")
	}
}

func TestField_ConstellationVisibility(t *testing.T) {
	s := openTestStore(t)
	hidden, _ := s.CreateField(Field{Name: "hidden", ConstellationVisible: false})
	shown, _ := s.CreateField(Field{Name: "shown", ConstellationVisible: true})

	idle := func(id string) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	inHidden, inShown, ungrouped := seedThread(t, s), seedThread(t, s), seedThread(t, s)
	for _, id := range []string{inHidden, inShown, ungrouped} {
		idle(id)
	}
	if err := s.SetThreadField(inHidden, &hidden.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadField(inShown, &shown.ID); err != nil {
		t.Fatal(err)
	}

	eligible := func() map[string]bool {
		t.Helper()
		ids, err := s.EligibleConstellationThreads(60)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	got := eligible()
	if got[inHidden] || !got[inShown] || !got[ungrouped] {
		t.Errorf("eligible = %v, want everything except the opted-out field's thread", got)
	}

	// Backfill goes through the same query, so it inherits the opt-out.
	bf, err := s.EligibleConstellationThreadsForBackfill(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range bf {
		if id == inHidden {
			t.Error("backfill included a thread from a Constellation-hidden field")
		}
	}

	// No stale mirror: moving the thread out of the field re-admits it.
	if err := s.SetThreadField(inHidden, nil); err != nil {
		t.Fatal(err)
	}
	if !eligible()[inHidden] {
		t.Error("a thread moved out of the hidden field stayed excluded")
	}
}

// The sidebar renders its field marker from ListThreads' rows, so that
// query must carry field_id — it didn't, because it was added to the
// single-thread reads first.
func TestListThreads_CarriesFieldID(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.CreateField(Field{Name: "p"})
	for _, id := range []string{"in", "out"} {
		if err := s.CreateThread(id, id, "m", "web"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetThreadField("in", &p.ID); err != nil {
		t.Fatal(err)
	}
	threads, err := s.ListThreads(10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	for _, th := range threads {
		got[th.ID] = th.FieldID
	}
	if got["in"] == nil || *got["in"] != p.ID || got["out"] != nil {
		t.Errorf("ListThreads field_ids = in:%v out:%v, want in:%q out:nil", got["in"], got["out"], p.ID)
	}
}

// A database created while this feature was still called "Projects" must
// come through Open with its groups, thread membership, and disabled-tool
// setting intact — not get a second empty fields table beside the orphaned
// projects one. The legacy shape is built by taking a current database and
// renaming the table/column back, which is exactly what the old schema was.
func TestOpen_RenamesLegacyProjectsToFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f, err := s.CreateField(Field{Name: "Budget rebuild", CustomInstructions: "metric only"})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if err := s.CreateThread("t1", "in a field", "m", "chat"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := s.SetThreadField("t1", &f.ID); err != nil {
		t.Fatalf("SetThreadField: %v", err)
	}
	if err := s.SetSetting("disabled_tools", `["code_exec","save_to_project"]`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE fields RENAME TO projects`,
		`ALTER TABLE threads RENAME COLUMN field_id TO project_id`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("building legacy shape %q: %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a legacy database: %v", err)
	}
	defer s.Close()

	got, err := s.GetField(f.ID)
	if err != nil || got.Name != "Budget rebuild" || got.CustomInstructions != "metric only" {
		t.Errorf("GetField after migration = %+v, %v; want the original row", got, err)
	}
	list, err := s.ListFields()
	if err != nil || len(list) != 1 {
		t.Errorf("ListFields = %d rows, %v; want exactly the migrated one", len(list), err)
	}
	th, err := s.GetThreadRaw("t1")
	if err != nil || th.FieldID == nil || *th.FieldID != f.ID {
		t.Errorf("thread membership after migration = %+v, %v; want field %s", th, err, f.ID)
	}
	if v, _ := s.GetSetting("disabled_tools"); v != `["code_exec","save_to_field"]` {
		t.Errorf("disabled_tools = %q, want the renamed tool", v)
	}
	// Deleting the field must still NULL out membership — proves the
	// threads FK was rewritten to point at fields, not the old name.
	if err := s.DeleteField(f.ID); err != nil {
		t.Fatalf("DeleteField after migration: %v", err)
	}
	if th, _ := s.GetThreadRaw("t1"); th == nil || th.FieldID != nil {
		t.Errorf("thread still in a deleted field: %+v", th)
	}
}
