package store

import "testing"

func TestDailyConfig_DefaultsThenUpdate(t *testing.T) {
	s := openTestStore(t)

	c, err := s.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig: %v", err)
	}
	if len(c.EnabledBlocks) != 9 {
		t.Fatalf("default enabled_blocks has %d entries, want 9: %+v", len(c.EnabledBlocks), c.EnabledBlocks)
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
	if len(c.CustomBlocks) != 0 {
		t.Errorf("CustomBlocks should default to empty, got %+v", c.CustomBlocks)
	}

	customInstructions := map[string]string{"headlines": "Focus on AI and climate policy"}
	customBlocks := []PulsarDailyCustomBlock{
		{Key: "custom_abc123", Title: "Stock Watchlist", Instructions: "Check NVDA and AAPL closing prices"},
	}
	if err := s.UpdateDailyConfig([]string{"weather", "sports"}, "Warriors, 49ers", customInstructions, customBlocks, "Seattle, WA", "deepseek-pro", "deepseek", "06:30"); err != nil {
		t.Fatalf("UpdateDailyConfig: %v", err)
	}
	c, err = s.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig (after update): %v", err)
	}
	if len(c.EnabledBlocks) != 2 || c.SportsTeams != "Warriors, 49ers" || c.TimeOfDay != "06:30" {
		t.Errorf("GetDailyConfig after update = %+v, want the values just written", c)
	}
	if c.WeatherLocation != "Seattle, WA" {
		t.Errorf("WeatherLocation = %q, want the value just written", c.WeatherLocation)
	}
	if c.CustomInstructions["headlines"] != "Focus on AI and climate policy" {
		t.Errorf("CustomInstructions = %+v, want the value just written", c.CustomInstructions)
	}
	if len(c.CustomBlocks) != 1 || c.CustomBlocks[0].Key != "custom_abc123" || c.CustomBlocks[0].Title != "Stock Watchlist" {
		t.Errorf("CustomBlocks = %+v, want the one block just written", c.CustomBlocks)
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

func TestAllDailyBlockContents(t *testing.T) {
	s := openTestStore(t)

	// No history yet — a block that's never fired should return an empty
	// slice, not an error (the normal case for a brand-new install).
	history, err := s.AllDailyBlockContents("word_of_day", 500)
	if err != nil {
		t.Fatalf("AllDailyBlockContents (empty): %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history = %+v, want empty before any trace rows exist", history)
	}

	dates := []string{"2026-09-05", "2026-09-06", "2026-09-07"}
	words := []string{"Petrichor", "Numinous", "Numinous"}
	for i, date := range dates {
		if err := s.UpsertDailyBlockTrace(PulsarDailyBlockTrace{
			EditionDate: date, BlockKey: "word_of_day", Title: "Word of the Day", StageAContent: words[i],
		}); err != nil {
			t.Fatalf("UpsertDailyBlockTrace(%s): %v", date, err)
		}
	}
	// A different block key's history must never bleed into another's.
	if err := s.UpsertDailyBlockTrace(PulsarDailyBlockTrace{
		EditionDate: "2026-09-07", BlockKey: "quote", Title: "Quote of the Day", StageAContent: "Stay hungry, stay foolish.",
	}); err != nil {
		t.Fatalf("UpsertDailyBlockTrace(quote): %v", err)
	}

	history, err = s.AllDailyBlockContents("word_of_day", 500)
	if err != nil {
		t.Fatalf("AllDailyBlockContents: %v", err)
	}
	want := []string{"Numinous", "Numinous", "Petrichor"} // most recent first
	if len(history) != len(want) {
		t.Fatalf("history = %+v, want %+v", history, want)
	}
	for i := range want {
		if history[i] != want[i] {
			t.Errorf("history[%d] = %q, want %q (most-recent-first order)", i, history[i], want[i])
		}
	}

	// limit caps how far back it reaches, not which block it reaches into.
	capped, err := s.AllDailyBlockContents("word_of_day", 2)
	if err != nil {
		t.Fatalf("AllDailyBlockContents (capped): %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("capped history = %+v, want exactly 2 entries", capped)
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
