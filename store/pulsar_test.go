package store

import "testing"

func TestPulsarRoutine_CreateGetListUpdate(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreatePulsarRoutine("Daily news", "Give me today's tech news", "deepseek", "researcher", false, "daily", "", "07:00")
	if err != nil {
		t.Fatalf("CreatePulsarRoutine: %v", err)
	}

	r, err := s.GetPulsarRoutine(id)
	if err != nil {
		t.Fatalf("GetPulsarRoutine: %v", err)
	}
	if r.Name != "Daily news" || r.ScheduleType != "daily" || r.TimeOfDay != "07:00" || r.DeepResearch {
		t.Errorf("GetPulsarRoutine returned %+v, want the values just written", r)
	}
	if r.LastRunAt != nil {
		t.Errorf("LastRunAt should be unset for a brand-new routine, got %+v", r.LastRunAt)
	}
	if r.ArchivedAt != nil {
		t.Errorf("ArchivedAt should be unset for a new routine, got %+v", r.ArchivedAt)
	}

	if _, err := s.CreatePulsarRoutine("GW3 weekly", "Guild Wars 3 news roundup", "deepseek", "", true, "weekly", "monday", "09:00"); err != nil {
		t.Fatalf("CreatePulsarRoutine (second): %v", err)
	}

	active, err := s.ListActivePulsarRoutines()
	if err != nil {
		t.Fatalf("ListActivePulsarRoutines: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("got %d active routines, want 2: %+v", len(active), active)
	}

	if err := s.UpdatePulsarRoutine(id, "Daily tech news", "Give me today's tech news, brief", "luna", "brief", false, "daily", "", "08:00"); err != nil {
		t.Fatalf("UpdatePulsarRoutine: %v", err)
	}
	r, err = s.GetPulsarRoutine(id)
	if err != nil {
		t.Fatalf("GetPulsarRoutine after update: %v", err)
	}
	if r.Name != "Daily tech news" || r.Model != "luna" || r.FocusMode != "brief" || r.TimeOfDay != "08:00" {
		t.Errorf("GetPulsarRoutine after update returned %+v, want the edited values", r)
	}

	if _, err := s.GetPulsarRoutine(999999); err != ErrPulsarRoutineNotFound {
		t.Errorf("GetPulsarRoutine(missing id) = %v, want ErrPulsarRoutineNotFound", err)
	}
}

func TestPulsarRoutine_ArchiveUnarchive(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreatePulsarRoutine("openclaw repo", "Any news on the openclaw repo?", "deepseek", "", false, "daily", "", "07:00")
	if err != nil {
		t.Fatalf("CreatePulsarRoutine: %v", err)
	}

	if err := s.ArchivePulsarRoutine(id); err != nil {
		t.Fatalf("ArchivePulsarRoutine: %v", err)
	}

	active, err := s.ListActivePulsarRoutines()
	if err != nil {
		t.Fatalf("ListActivePulsarRoutines: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("got %d active routines after archiving the only one, want 0: %+v", len(active), active)
	}

	archived, err := s.ListArchivedPulsarRoutines()
	if err != nil {
		t.Fatalf("ListArchivedPulsarRoutines: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != id {
		t.Fatalf("got archived %+v, want exactly the routine just archived", archived)
	}
	if archived[0].ArchivedAt == nil {
		t.Errorf("archived routine's ArchivedAt should be set, got %+v", archived[0].ArchivedAt)
	}

	// Archiving again is a no-op, not an error — see ArchivePulsarRoutine's
	// doc comment.
	if err := s.ArchivePulsarRoutine(id); err != nil {
		t.Errorf("archiving an already-archived routine should not error, got %v", err)
	}

	if err := s.UnarchivePulsarRoutine(id); err != nil {
		t.Fatalf("UnarchivePulsarRoutine: %v", err)
	}
	active, err = s.ListActivePulsarRoutines()
	if err != nil {
		t.Fatalf("ListActivePulsarRoutines after unarchive: %v", err)
	}
	if len(active) != 1 || active[0].ID != id {
		t.Fatalf("got active %+v after unarchive, want exactly the routine back", active)
	}

	if err := s.ArchivePulsarRoutine(999999); err != ErrPulsarRoutineNotFound {
		t.Errorf("ArchivePulsarRoutine(missing id) = %v, want ErrPulsarRoutineNotFound", err)
	}
}

func TestPulsarRoutine_LastRun(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreatePulsarRoutine("Daily news", "news", "deepseek", "", false, "daily", "", "07:00")
	if err != nil {
		t.Fatalf("CreatePulsarRoutine: %v", err)
	}

	if err := s.SetPulsarRoutineLastRun(id, "2026-09-03 07:00:00"); err != nil {
		t.Fatalf("SetPulsarRoutineLastRun: %v", err)
	}
	r, err := s.GetPulsarRoutine(id)
	if err != nil {
		t.Fatalf("GetPulsarRoutine: %v", err)
	}
	if r.LastRunAt == nil {
		t.Fatalf("LastRunAt should be set after SetPulsarRoutineLastRun, got %+v", r.LastRunAt)
	}

	if err := s.SetPulsarRoutineLastRun(999999, "2026-09-03 07:00:00"); err != ErrPulsarRoutineNotFound {
		t.Errorf("SetPulsarRoutineLastRun(missing id) = %v, want ErrPulsarRoutineNotFound", err)
	}
}

func TestPulsarPulses_ListAndUnreadCounts(t *testing.T) {
	s := openTestStore(t)

	routineID, err := s.CreatePulsarRoutine("Daily news", "news", "deepseek", "", false, "daily", "", "07:00")
	if err != nil {
		t.Fatalf("CreatePulsarRoutine: %v", err)
	}

	if err := s.CreateThread("pulse-1", "Daily news — Sept 3", "deepseek", "pulsar"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE threads SET pulsar_routine_id = ? WHERE id = ?`, routineID, "pulse-1"); err != nil {
		t.Fatalf("linking pulse-1 to routine: %v", err)
	}
	if err := s.CreateThread("pulse-2", "Daily news — Sept 4", "deepseek", "pulsar"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE threads SET pulsar_routine_id = ? WHERE id = ?`, routineID, "pulse-2"); err != nil {
		t.Fatalf("linking pulse-2 to routine: %v", err)
	}

	pulses, err := s.ListPulsarPulses(routineID)
	if err != nil {
		t.Fatalf("ListPulsarPulses: %v", err)
	}
	if len(pulses) != 2 {
		t.Fatalf("got %d pulses, want 2: %+v", len(pulses), pulses)
	}
	for _, p := range pulses {
		if p.Seen {
			t.Errorf("pulse %+v should start unseen", p)
		}
	}

	counts, err := s.UnreadPulseCounts()
	if err != nil {
		t.Fatalf("UnreadPulseCounts: %v", err)
	}
	if counts[routineID] != 2 {
		t.Errorf("UnreadPulseCounts[%d] = %d, want 2: %+v", routineID, counts[routineID], counts)
	}

	if err := s.MarkPulseSeen("pulse-1"); err != nil {
		t.Fatalf("MarkPulseSeen: %v", err)
	}
	counts, err = s.UnreadPulseCounts()
	if err != nil {
		t.Fatalf("UnreadPulseCounts after marking one seen: %v", err)
	}
	if counts[routineID] != 1 {
		t.Errorf("UnreadPulseCounts[%d] after marking one seen = %d, want 1: %+v", routineID, counts[routineID], counts)
	}
}
