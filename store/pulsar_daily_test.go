package store

import "testing"

func TestDailyConfig_DefaultsThenUpdate(t *testing.T) {
	s := openTestStore(t)

	c, err := s.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig: %v", err)
	}
	if len(c.EnabledBlocks) != 10 {
		t.Fatalf("default enabled_blocks has %d entries, want 10: %+v", len(c.EnabledBlocks), c.EnabledBlocks)
	}
	if c.ArchitectModel != "deepseek-pro" || c.WriterModel != "deepseek" || c.TimeOfDay != "07:00" {
		t.Errorf("GetDailyConfig defaults = %+v, want the column defaults", c)
	}
	if c.LastGeneratedAt != nil {
		t.Errorf("LastGeneratedAt should be unset before any generation, got %+v", c.LastGeneratedAt)
	}
	if len(c.CustomInstructions) != 0 {
		t.Errorf("CustomInstructions should default to empty, got %+v", c.CustomInstructions)
	}

	customInstructions := map[string]string{"headlines": "Focus on AI and climate policy"}
	if err := s.UpdateDailyConfig([]string{"weather", "sports"}, "Warriors, 49ers", customInstructions, "deepseek-pro", "deepseek", "06:30"); err != nil {
		t.Fatalf("UpdateDailyConfig: %v", err)
	}
	c, err = s.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig (after update): %v", err)
	}
	if len(c.EnabledBlocks) != 2 || c.SportsTeams != "Warriors, 49ers" || c.TimeOfDay != "06:30" {
		t.Errorf("GetDailyConfig after update = %+v, want the values just written", c)
	}
	if c.CustomInstructions["headlines"] != "Focus on AI and climate policy" {
		t.Errorf("CustomInstructions = %+v, want the value just written", c.CustomInstructions)
	}

	if err := s.SetDailyLastGenerated("2026-09-05 07:00:00"); err != nil {
		t.Fatalf("SetDailyLastGenerated: %v", err)
	}
	c, err = s.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig (after SetDailyLastGenerated): %v", err)
	}
	if c.LastGeneratedAt == nil {
		t.Error("LastGeneratedAt should be set after SetDailyLastGenerated")
	}
}

func TestDailyEdition_UpsertGetLatest(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.GetDailyEdition("2026-09-04"); err != ErrDailyEditionNotFound {
		t.Fatalf("GetDailyEdition on empty table: got %v, want ErrDailyEditionNotFound", err)
	}
	if _, err := s.LatestDailyEdition("2026-09-05"); err != ErrDailyEditionNotFound {
		t.Fatalf("LatestDailyEdition on empty table: got %v, want ErrDailyEditionNotFound", err)
	}

	yesterday := []PulsarDailyBlock{
		{Key: "weather", Title: "Weather", Content: "Sunny, 72F", Gist: "Sunny"},
		{Key: "top_story", Title: "AI datacenter buildout", Content: "...", Gist: "Buildout continues", IsTopStory: true},
	}
	if err := s.UpsertDailyEdition("2026-09-03", yesterday, 0.0123); err != nil {
		t.Fatalf("UpsertDailyEdition (2026-09-03): %v", err)
	}

	got, err := s.GetDailyEdition("2026-09-03")
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}
	if len(got.Blocks) != 2 || got.Blocks[1].Key != "top_story" || !got.Blocks[1].IsTopStory {
		t.Errorf("GetDailyEdition = %+v, want the blocks just written", got.Blocks)
	}
	if got.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", got.CostUSD)
	}

	// A missed day: nothing generated for 2026-09-04, so 2026-09-05's
	// diff-judge should still find 2026-09-03 as "the latest edition
	// before today" rather than treating every block as first-ever-day.
	latest, err := s.LatestDailyEdition("2026-09-05")
	if err != nil {
		t.Fatalf("LatestDailyEdition: %v", err)
	}
	if latest.Date != "2026-09-03" {
		t.Errorf("LatestDailyEdition(2026-09-05) = %q, want 2026-09-03", latest.Date)
	}

	// Re-running Stage D for the same date overwrites, it doesn't duplicate.
	updated := []PulsarDailyBlock{{Key: "weather", Title: "Weather", Content: "Rain, 60F", Gist: "Rain"}}
	if err := s.UpsertDailyEdition("2026-09-03", updated, 0.045); err != nil {
		t.Fatalf("UpsertDailyEdition (overwrite): %v", err)
	}
	got, err = s.GetDailyEdition("2026-09-03")
	if err != nil {
		t.Fatalf("GetDailyEdition (after overwrite): %v", err)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Content != "Rain, 60F" {
		t.Errorf("GetDailyEdition after overwrite = %+v, want the single updated block", got.Blocks)
	}
	if got.CostUSD != 0.045 {
		t.Errorf("CostUSD after overwrite = %v, want 0.045 (the overwritten value, not the original)", got.CostUSD)
	}
}
