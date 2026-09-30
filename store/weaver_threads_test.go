package store

import (
	"testing"
)

func TestListWeaverThreads_ExcludesOtherSourcesAndFlagsAutomatic(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("manual-1", "Fix the laptop stars", "deepseek", "weaver"); err != nil {
		t.Fatalf("CreateThread(manual-1): %v", err)
	}
	if err := s.CreateThread("auto-1", "Shooting star — Kitchen renovation thread", "deepseek", "weaver"); err != nil {
		t.Fatalf("CreateThread(auto-1): %v", err)
	}
	// A non-weaver thread must never leak into this list — same exclusion
	// ListThreads/ListThreadsPage already apply in the other direction.
	if err := s.CreateThread("web-1", "An ordinary chat", "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread(web-1): %v", err)
	}

	got, err := s.ListWeaverThreads()
	if err != nil {
		t.Fatalf("ListWeaverThreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListWeaverThreads = %+v, want exactly 2 (the ordinary web thread must be excluded)", got)
	}

	byID := map[string]WeaverThreadSummary{}
	for _, wt := range got {
		byID[wt.ID] = wt
	}
	if manual, ok := byID["manual-1"]; !ok || manual.IsAutomatic {
		t.Errorf("manual-1 = %+v, want present with IsAutomatic = false", manual)
	}
	if auto, ok := byID["auto-1"]; !ok || !auto.IsAutomatic {
		t.Errorf("auto-1 = %+v, want present with IsAutomatic = true", auto)
	}
}
