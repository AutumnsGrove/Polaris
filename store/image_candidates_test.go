package store

import "testing"

func TestImageCandidates_RoundTripKeepsNumbersAndGaps(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if err := s.SaveImageCandidates("t1", []string{`{"title":"a"}`, "", `{"title":"c"}`}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.LoadImageCandidates("t1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 || got[0] != `{"title":"a"}` || got[1] != "" || got[2] != `{"title":"c"}` {
		t.Fatalf("Load = %q, want numbers 1 and 3 kept in place with a gap at 2", got)
	}

	// A later turn appends; earlier numbers must be untouched.
	if err := s.SaveImageCandidates("t1", []string{`{"title":"a"}`, "", `{"title":"c"}`, `{"title":"d"}`}); err != nil {
		t.Fatalf("Save (append): %v", err)
	}
	got, _ = s.LoadImageCandidates("t1")
	if len(got) != 4 || got[3] != `{"title":"d"}` {
		t.Errorf("Load after append = %q, want 4 entries ending in d", got)
	}
}

func TestImageCandidates_ScopedToThread(t *testing.T) {
	s := openTestStore(t)
	for _, id := range []string{"t1", "t2"} {
		if err := s.CreateThread(id, "Thread", "test-model", "web"); err != nil {
			t.Fatalf("CreateThread: %v", err)
		}
	}
	if err := s.SaveImageCandidates("t1", []string{`{"title":"a"}`}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoadImageCandidates("t2"); len(got) != 0 {
		t.Errorf("t2 sees %q, want its own empty pool", got)
	}
}

func TestForkThread_CopiesImageCandidates(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage("root", "user", "q1", "[]", "[]", 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage("root", "assistant", "a1", "[]", "[]", 0.01, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveImageCandidates("root", []string{`{"title":"a"}`, `{"title":"b"}`}); err != nil {
		t.Fatal(err)
	}

	forkID, err := s.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	got, _ := s.LoadImageCandidates(forkID)
	if len(got) != 2 || got[1] != `{"title":"b"}` {
		t.Errorf("fork pool = %q, want the source's candidates carried over", got)
	}
}
