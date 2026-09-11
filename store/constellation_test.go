package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestConstellationConfig_DefaultsThenUpdate(t *testing.T) {
	s := openTestStore(t)

	c, err := s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if c.Enabled {
		t.Error("Enabled should default to false")
	}
	if c.PollIntervalMinutes != 60 {
		t.Errorf("PollIntervalMinutes = %d, want 60", c.PollIntervalMinutes)
	}
	if c.Model != "" {
		t.Errorf("Model = %q, want empty (inherit default)", c.Model)
	}
	if c.LastCheckedAt != nil {
		t.Errorf("LastCheckedAt should be unset initially, got %+v", c.LastCheckedAt)
	}

	if err := s.UpdateConstellationConfig(true, 30, "deepseek-pro"); err != nil {
		t.Fatalf("UpdateConstellationConfig: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after update): %v", err)
	}
	if !c.Enabled || c.PollIntervalMinutes != 30 || c.Model != "deepseek-pro" {
		t.Errorf("GetConstellationConfig after update = %+v, want the values just written", c)
	}

	if err := s.SetConstellationLastChecked("2026-09-10 12:00:00"); err != nil {
		t.Fatalf("SetConstellationLastChecked: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after SetConstellationLastChecked): %v", err)
	}
	if c.LastCheckedAt == nil {
		t.Error("LastCheckedAt should be set after SetConstellationLastChecked")
	}
}

func TestStar_CreateGetUpdate(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{
		Title:    "Cloudflare Workers",
		Category: "technology",
		Summary:  "Edge compute platform",
		Body:     "## Cloudflare Workers\n\nRuns JS at the edge.",
		Tags:     []string{"cloudflare", "edge"},
		Status:   "auto",
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateStar returned id 0")
	}

	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Title != "Cloudflare Workers" || got.Category != "technology" || len(got.Tags) != 2 {
		t.Errorf("GetStar = %+v, want the values just written", got)
	}
	if got.Status != "auto" || got.IsPersonal {
		t.Errorf("GetStar defaults = %+v, want status=auto, is_personal=false", got)
	}

	if err := s.UpdateStar(id, "Updated summary", "Updated body", []string{"cloudflare", "workers", "edge"}, "fuzzy", false); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar (after update): %v", err)
	}
	if got.Summary != "Updated summary" || len(got.Tags) != 3 || got.Confidence != "fuzzy" {
		t.Errorf("GetStar after update = %+v, want the values just written", got)
	}

	if _, err := s.GetStar(999999); err != ErrStarNotFound {
		t.Fatalf("GetStar(missing) = %v, want ErrStarNotFound", err)
	}
}

func TestStar_NilTagsEncodeAsEmptyArrayNotNull(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{Title: "No tags", Category: "technology", Status: "auto"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	var tagsJSON string
	if err := s.db.QueryRow(`SELECT tags FROM stars WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
		t.Fatalf("querying raw tags column: %v", err)
	}
	if tagsJSON != "[]" {
		t.Errorf("raw tags column = %q, want \"[]\" (nil Tags must not encode as JSON null)", tagsJSON)
	}

	if err := s.UpdateStar(id, "summary", "body", nil, "obvious", false); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	if err := s.db.QueryRow(`SELECT tags FROM stars WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
		t.Fatalf("querying raw tags column after update: %v", err)
	}
	if tagsJSON != "[]" {
		t.Errorf("raw tags column after UpdateStar(nil) = %q, want \"[]\"", tagsJSON)
	}
}

func TestStar_PersonalCreateAndUpdateForceProposed(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{
		Title: "Reads science fiction", Category: "identity", Summary: "Enjoys sci-fi",
		Status: "auto", IsPersonal: true,
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Status != "proposed" {
		t.Errorf("personal star Status = %q, want proposed regardless of requested status", got.Status)
	}

	// Confirm it, then update it — even a pure reinforcement must reset
	// status back to proposed for a personal star.
	if err := s.SetStarStatus(id, "confirmed"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	if err := s.UpdateStar(id, "Enjoys sci-fi, especially Le Guin", got.Body, got.Tags, got.Confidence, true); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar (after update): %v", err)
	}
	if got.Status != "proposed" {
		t.Errorf("updating a personal star Status = %q, want proposed again after update", got.Status)
	}
}

func TestListStars_GroupingAndFilters(t *testing.T) {
	s := openTestStore(t)

	auto, _ := s.CreateStar(Star{Title: "Go generics", Category: "technology", Status: "auto"})
	proposed, _ := s.CreateStar(Star{Title: "Vague inference", Category: "technology", Status: "proposed"})
	rejected, _ := s.CreateStar(Star{Title: "Wrong guess", Category: "technology", Status: "proposed"})
	if err := s.SetStarStatus(rejected, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	disabled, _ := s.CreateStar(Star{Title: "Unwanted", Category: "technology", Status: "confirmed"})
	if err := s.SetStarDisabled(disabled, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	stars, err := s.ListStars(StarFilter{Statuses: []string{"auto", "confirmed"}})
	if err != nil {
		t.Fatalf("ListStars: %v", err)
	}
	if len(stars) != 1 || stars[0].ID != auto {
		t.Errorf("ListStars(auto+confirmed) = %+v, want just the auto star (disabled/proposed/rejected excluded)", stars)
	}

	proposedStars, err := s.ListStars(StarFilter{Statuses: []string{"proposed"}})
	if err != nil {
		t.Fatalf("ListStars(proposed): %v", err)
	}
	if len(proposedStars) != 1 || proposedStars[0].ID != proposed {
		t.Errorf("ListStars(proposed) = %+v, want just the proposed star", proposedStars)
	}

	rejectedStars, err := s.ListStars(StarFilter{Statuses: []string{"rejected"}})
	if err != nil {
		t.Fatalf("ListStars(rejected): %v", err)
	}
	if len(rejectedStars) != 1 || rejectedStars[0].ID != rejected {
		t.Errorf("ListStars(rejected) = %+v, want just the rejected star", rejectedStars)
	}
}

func TestSearchStars_FTS(t *testing.T) {
	s := openTestStore(t)

	id1, _ := s.CreateStar(Star{Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute platform", Status: "auto"})
	_, _ = s.CreateStar(Star{Title: "Sourdough baking", Category: "cooking", Summary: "Fermentation and hydration ratios", Status: "auto"})

	results, err := s.SearchStars("Cloudflare", 10)
	if err != nil {
		t.Fatalf("SearchStars: %v", err)
	}
	if len(results) != 1 || results[0].ID != id1 {
		t.Errorf("SearchStars(Cloudflare) = %+v, want just the Cloudflare star", results)
	}

	// A rejected star must still be findable — search_stars is also
	// dedup-checking duty, and Weaver needs to see "this was already said no
	// to" rather than silently re-proposing it.
	id3, _ := s.CreateStar(Star{Title: "Rejected topic", Category: "technology", Summary: "Something declined", Status: "proposed"})
	if err := s.SetStarStatus(id3, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	results, err = s.SearchStars("declined", 10)
	if err != nil {
		t.Fatalf("SearchStars(declined): %v", err)
	}
	if len(results) != 1 || results[0].ID != id3 {
		t.Errorf("SearchStars(declined) = %+v, want the rejected star to still be findable", results)
	}
}

func TestSearchLibraryStars_ExcludesRejectedAndDisabled(t *testing.T) {
	s := openTestStore(t)

	visible, _ := s.CreateStar(Star{Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute platform", Status: "auto"})

	rejected, _ := s.CreateStar(Star{Title: "Cloudflare pages rejected", Category: "technology", Summary: "Something declined about Cloudflare", Status: "proposed"})
	if err := s.SetStarStatus(rejected, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}

	disabled, _ := s.CreateStar(Star{Title: "Cloudflare disabled topic", Category: "technology", Summary: "Cloudflare but disabled", Status: "confirmed"})
	if err := s.SetStarDisabled(disabled, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	results, err := s.SearchLibraryStars("Cloudflare", 10)
	if err != nil {
		t.Fatalf("SearchLibraryStars: %v", err)
	}
	if len(results) != 1 || results[0].ID != visible {
		t.Errorf("SearchLibraryStars(Cloudflare) = %+v, want just the one visible star (not the rejected or disabled ones)", results)
	}
	if results[0].Category != "technology" {
		t.Errorf("Category = %q, want technology", results[0].Category)
	}
	if results[0].Tags == nil {
		t.Error("Tags should never be nil, even for a star created with no tags")
	}
}

func TestStarSources_UpsertNotDuplicate(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	starID, _ := s.CreateStar(Star{Title: "Topic", Category: "technology", Status: "auto"})

	if err := s.LinkStarSource(starID, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(starID, threadID); err != nil {
		t.Fatalf("LinkStarSource (second time, same thread): %v", err)
	}

	sources, err := s.StarSources(starID)
	if err != nil {
		t.Fatalf("StarSources: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("StarSources = %+v, want exactly one row (upsert, not duplicate)", sources)
	}
}

func TestStarsByThread_ReturnsContributedStars(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	otherThreadID := seedThread(t, s)

	starA, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	starB, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "auto"})
	unrelated, _ := s.CreateStar(Star{Title: "C", Category: "technology", Status: "auto"})

	if err := s.LinkStarSource(starA, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(starB, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(unrelated, otherThreadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}

	stars, err := s.StarsByThread(threadID)
	if err != nil {
		t.Fatalf("StarsByThread: %v", err)
	}
	if len(stars) != 2 {
		t.Fatalf("StarsByThread = %+v, want exactly the 2 stars this thread contributed to", stars)
	}
	for _, star := range stars {
		if star.ID == unrelated {
			t.Errorf("StarsByThread included a star from a different thread: %+v", star)
		}
	}
}

func TestStarEdges_LinkIsIdempotentAndUndirected(t *testing.T) {
	s := openTestStore(t)
	a, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	b, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "auto"})

	if err := s.LinkStars(a, b, "both concern edge compute"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}
	// Idempotent no-op regardless of argument order.
	if err := s.LinkStars(b, a, "duplicate call"); err != nil {
		t.Fatalf("LinkStars (reverse order): %v", err)
	}

	edges, err := s.StarEdges(a)
	if err != nil {
		t.Fatalf("StarEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("StarEdges(a) = %+v, want exactly one edge (idempotent link)", edges)
	}

	edgesB, err := s.StarEdges(b)
	if err != nil {
		t.Fatalf("StarEdges(b): %v", err)
	}
	if len(edgesB) != 1 {
		t.Errorf("StarEdges(b) = %+v, want the same edge visible from either side", edgesB)
	}
}

func TestShootingStarRun_LifecycleAndRetry(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)

	runID, err := s.StartShootingStarRun(threadID, 5)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}

	last, err := s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if last.ID != runID || last.LastMessageIDSeen != 5 {
		t.Errorf("LastShootingStarRun = %+v, want the run just started", last)
	}
	if last.NeedsRetry {
		t.Error("a fresh run should not need retry before it's finished")
	}

	if err := s.FinishShootingStarRun(runID, "Noted a new interest in Cloudflare Workers.", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	last, err = s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun (after finish): %v", err)
	}
	if last.Summary == "" || last.NeedsRetry {
		t.Errorf("LastShootingStarRun after success = %+v, want summary set and needs_retry false", last)
	}

	// A failing run sets needs_retry and error.
	runID2, err := s.StartShootingStarRun(threadID, 9)
	if err != nil {
		t.Fatalf("StartShootingStarRun (2nd): %v", err)
	}
	if err := s.FinishShootingStarRun(runID2, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun (failure): %v", err)
	}
	last, err = s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun (after failure): %v", err)
	}
	if !last.NeedsRetry || last.Error != "max_turns_exceeded" {
		t.Errorf("LastShootingStarRun after failure = %+v, want needs_retry=true, error=max_turns_exceeded", last)
	}
}

func TestShootingStarCandidateAndEvent_Recorded(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	starID, _ := s.CreateStar(Star{Title: "Topic", Category: "technology", Status: "auto"})

	if err := s.RecordShootingStarCandidate(runID, "Cloudflare Workers", "obvious", "new_star", "clearly discussed at length", &starID); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "create_star", `{"title":"Cloudflare Workers"}`, "created star 1", 0.0012); err != nil {
		t.Fatalf("RecordShootingStarEvent: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "final_answer", "{}", "Noted a new interest.", 0.0003); err != nil {
		t.Fatalf("RecordShootingStarEvent (final_answer): %v", err)
	}

	if err := s.FinishShootingStarRun(runID, "Noted a new interest.", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	last, err := s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	// cost_usd is a cached rollup of shooting_star_events.cost_usd for this run.
	want := 0.0012 + 0.0003
	if diff := last.CostUSD - want; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("CostUSD = %v, want rollup %v of the two logged events", last.CostUSD, want)
	}
}

func TestStarReview_Recorded(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "Proposed thing", Category: "technology", Status: "proposed"})

	if err := s.RecordStarReview(starID, "approved", ""); err != nil {
		t.Fatalf("RecordStarReview: %v", err)
	}
	if err := s.SetStarStatus(starID, "confirmed"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	got, err := s.GetStar(starID)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed after approve", got.Status)
	}
}

// TestGetConstellationWeekFeed_IncludesStarID covers a real bug: none of
// the three feed queries (new/updated/linked) ever selected a star's id,
// only its title — the "This week" page's rows had no way to navigate
// anywhere at all when tapped, since ConstellationWeekItem carried nothing
// to link to.
func TestHasInFlightShootingStarRun(t *testing.T) {
	s := openTestStore(t)

	busy, err := s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if busy {
		t.Error("busy = true with no runs at all, want false")
	}

	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}

	busy, err = s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if !busy {
		t.Error("busy = false with a started, unfinished run, want true")
	}

	if err := s.FinishShootingStarRun(runID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	busy, err = s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if busy {
		t.Error("busy = true after the only run finished, want false")
	}
}

func TestGetConstellationWeekFeed_IncludesStarID(t *testing.T) {
	s := openTestStore(t)

	newID, _ := s.CreateStar(Star{Title: "Brand new", Category: "technology", Status: "auto"})

	updatedID, _ := s.CreateStar(Star{Title: "Will be updated", Category: "technology", Status: "auto"})
	if _, err := s.db.Exec(`UPDATE stars SET created_at = datetime('now', '-30 days') WHERE id = ?`, updatedID); err != nil {
		t.Fatalf("backdating created_at: %v", err)
	}
	if err := s.UpdateStar(updatedID, "new summary", "new body", nil, "", false); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}

	linkA, _ := s.CreateStar(Star{Title: "Link side A", Category: "technology", Status: "auto"})
	linkB, _ := s.CreateStar(Star{Title: "Link side B", Category: "technology", Status: "auto"})
	if err := s.LinkStars(linkA, linkB, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}

	items, err := s.GetConstellationWeekFeed()
	if err != nil {
		t.Fatalf("GetConstellationWeekFeed: %v", err)
	}

	byTitle := map[string]ConstellationWeekItem{}
	for _, it := range items {
		byTitle[it.Title] = it
	}

	if got := byTitle["Brand new"]; got.StarID != newID {
		t.Errorf("new item StarID = %d, want %d", got.StarID, newID)
	}
	if got := byTitle["Will be updated"]; got.StarID != updatedID {
		t.Errorf("updated item StarID = %d, want %d", got.StarID, updatedID)
	}
	if got := byTitle["Link side A"]; got.StarID != linkA {
		t.Errorf("linked item StarID = %d, want %d", got.StarID, linkA)
	}
}

func TestGetConstellationStats_Aggregates(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)

	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	proposedID, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "proposed"})
	other, _ := s.CreateStar(Star{Title: "C", Category: "technology", Status: "auto"})
	if err := s.LinkStars(starID, other, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}
	if err := s.RecordStarReview(proposedID, "approved", ""); err != nil {
		t.Fatalf("RecordStarReview: %v", err)
	}

	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "search_stars", "{}", "[]", 0.001); err != nil {
		t.Fatalf("RecordShootingStarEvent: %v", err)
	}
	if err := s.FinishShootingStarRun(runID, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if stats.TotalCostUSD < 0.001 {
		t.Errorf("TotalCostUSD = %v, want at least 0.001", stats.TotalCostUSD)
	}
	if stats.ShootingStarCount != 1 {
		t.Errorf("ShootingStarCount = %d, want 1", stats.ShootingStarCount)
	}
	if stats.StarCountsByStatus["auto"] != 2 || stats.StarCountsByStatus["proposed"] != 1 {
		t.Errorf("StarCountsByStatus = %+v, want auto=2 proposed=1", stats.StarCountsByStatus)
	}
	if stats.ToolCallCounts["search_stars"] != 1 {
		t.Errorf("ToolCallCounts = %+v, want search_stars=1", stats.ToolCallCounts)
	}
	if stats.ReviewActionCounts["approved"] != 1 {
		t.Errorf("ReviewActionCounts = %+v, want approved=1", stats.ReviewActionCounts)
	}
	if stats.LinksCreatedCount != 1 {
		t.Errorf("LinksCreatedCount = %d, want 1", stats.LinksCreatedCount)
	}
	if stats.MaxTurnsCount != 1 {
		t.Errorf("MaxTurnsCount = %d, want 1", stats.MaxTurnsCount)
	}
	if stats.NeedsRetryCount != 1 {
		t.Errorf("NeedsRetryCount = %d, want 1", stats.NeedsRetryCount)
	}
}

func TestRecordStarReconcileCost_RollsIntoStats(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "confirmed"})

	// A Refine/Edit correction has no shooting_star_runs row to hang a
	// RecordShootingStarEvent call off of — this is its own cost trail,
	// which GetConstellationStats must still fold into the same
	// Total/PeriodCostUSD figure Weaver's own runs feed (see
	// gateway/constellation_routes.go's reconcileAndSaveStar, which
	// previously discarded this cost entirely).
	if err := s.RecordStarReconcileCost(starID, 0.0042); err != nil {
		t.Fatalf("RecordStarReconcileCost: %v", err)
	}

	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if diff := stats.TotalCostUSD - 0.0042; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("TotalCostUSD = %v, want to include the 0.0042 reconcile cost", stats.TotalCostUSD)
	}
	if diff := stats.PeriodCostUSD - 0.0042; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("PeriodCostUSD = %v, want to include the 0.0042 reconcile cost", stats.PeriodCostUSD)
	}
}

func TestSetStarStatusAndRecordReview_WritesBothAtomically(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "proposed"})

	if err := s.SetStarStatusAndRecordReview(starID, "confirmed", "approved", ""); err != nil {
		t.Fatalf("SetStarStatusAndRecordReview: %v", err)
	}

	star, err := s.GetStar(starID)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed", star.Status)
	}
	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if stats.ReviewActionCounts["approved"] != 1 {
		t.Errorf("ReviewActionCounts = %+v, want approved=1 to have been written alongside the status change", stats.ReviewActionCounts)
	}
}

func TestLatestCandidateReasoningBulk_ReturnsMostRecentPerStar(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	starA, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "proposed"})
	starB, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "proposed"})

	if err := s.RecordShootingStarCandidate(runID, "A", "unsure", "new_star", "stale reason", &starA); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarCandidate(runID, "A", "unsure", "new_star", "fresh reason", &starA); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarCandidate(runID, "B", "unsure", "new_star", "reason for B", &starB); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}

	got, err := s.LatestCandidateReasoningBulk([]int64{starA, starB})
	if err != nil {
		t.Fatalf("LatestCandidateReasoningBulk: %v", err)
	}
	if got[starA] != "fresh reason" {
		t.Errorf("reasoning[A] = %q, want the most recently recorded row", got[starA])
	}
	if got[starB] != "reason for B" {
		t.Errorf("reasoning[B] = %q, want %q", got[starB], "reason for B")
	}
}

func TestEligibleConstellationThreads_Gates(t *testing.T) {
	s := openTestStore(t)

	// A thread that's never been processed and is old enough to be idle
	// — eligible via the delta gate (no prior run at all).
	neverProcessed := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, neverProcessed); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	// A thread still "mid-thought" (a message from seconds ago) — must be
	// excluded by the idle-timing gate regardless of everything else.
	midThought := seedThread(t, s)

	// A thread already fully processed with nothing new since — must be
	// excluded (fails the delta gate, no retry needed).
	upToDate := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, upToDate); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	msgs, err := s.GetMessages(upToDate)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	runID, err := s.StartShootingStarRun(upToDate, msgs[len(msgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(runID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	// A thread that failed once but has since succeeded — the bug this
	// test exists to catch: an EXISTS-any-row check on needs_retry would
	// wrongly keep flagging this eligible forever using the stale failed
	// row, instead of only the most recent run's needs_retry value.
	failedThenSucceeded := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, failedThenSucceeded); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	fsMsgs, err := s.GetMessages(failedThenSucceeded)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	failedRunID, err := s.StartShootingStarRun(failedThenSucceeded, fsMsgs[len(fsMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(failedRunID, "", "some error", true); err != nil {
		t.Fatalf("FinishShootingStarRun (failure): %v", err)
	}
	retryRunID, err := s.StartShootingStarRun(failedThenSucceeded, fsMsgs[len(fsMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun (retry): %v", err)
	}
	if err := s.FinishShootingStarRun(retryRunID, "done on retry", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun (retry succeeded): %v", err)
	}

	// A thread that's currently needing retry — eligible unconditionally
	// via the retry gate, regardless of the delta gate.
	needsRetry := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, needsRetry); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	nrMsgs, err := s.GetMessages(needsRetry)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	nrRunID, err := s.StartShootingStarRun(needsRetry, nrMsgs[len(nrMsgs)-1].ID)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.FinishShootingStarRun(nrRunID, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun (needs retry): %v", err)
	}

	// A disabled thread, otherwise eligible by every other gate — excluded
	// outright.
	disabled := seedThread(t, s)
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, disabled); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	if err := s.DeleteThread(disabled); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	// A pulsar-sourced thread (a routine's pulse history), otherwise
	// eligible by every other gate — excluded outright, same as
	// ListThreads' own source != 'pulsar' filter. Weaver should never mine
	// Constellation's own scheduled-pulse output for new stars.
	pulsar := uuid.NewString()
	if err := s.CreateThread(pulsar, "Test pulse", "deepseek", "pulsar"); err != nil {
		t.Fatalf("CreateThread (pulsar): %v", err)
	}
	if _, err := s.AddMessage(pulsar, "user", "some pulse content", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage (pulsar): %v", err)
	}
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, pulsar); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	got, err := s.EligibleConstellationThreads(60)
	if err != nil {
		t.Fatalf("EligibleConstellationThreads: %v", err)
	}

	want := map[string]bool{neverProcessed: true, needsRetry: true}
	gotSet := map[string]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	for id := range want {
		if !gotSet[id] {
			t.Errorf("EligibleConstellationThreads missing expected thread %q; got %v", id, got)
		}
	}
	for _, excluded := range []string{midThought, upToDate, failedThenSucceeded, disabled, pulsar} {
		if gotSet[excluded] {
			t.Errorf("EligibleConstellationThreads wrongly included %q; got %v", excluded, got)
		}
	}
}

// TestEligibleConstellationThreads_ResolvesForkedThreadToRoot covers a real
// bug found by inspecting a production database: editing/regenerating a
// message forks a hidden variant thread (fork_root_id set, its own title
// always "" — see ForkThread's doc comment), and this function used to
// return that raw variant id directly. Weaver then linked stars to it via
// star_sources, and since a variant is never independently addressable
// (GetThread can't open one, nothing lists it), every such star's "source"
// permanently showed as a broken/untitled thread in the UI. This must
// return the stable root id instead — same join-through-root fix
// SearchMessages already uses for the identical class of problem.
func TestEligibleConstellationThreads_ResolvesForkedThreadToRoot(t *testing.T) {
	s := openTestStore(t)

	root := seedThread(t, s)
	forkID, err := s.ForkThread(root, root, 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := s.AddMessage(forkID, "assistant", "edited reply", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.SetActiveVariant(root, forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}
	// Idle-gate: back-date the variant's messages (its own row, not root's
	// — the whole point is root's own messages table is stale/irrelevant
	// once a variant becomes active).
	if _, err := s.db.Exec(`UPDATE messages SET created_at = datetime('now', '-2 hours') WHERE thread_id = ?`, forkID); err != nil {
		t.Fatalf("backdating message: %v", err)
	}

	got, err := s.EligibleConstellationThreads(60)
	if err != nil {
		t.Fatalf("EligibleConstellationThreads: %v", err)
	}

	if len(got) != 1 || got[0] != root {
		t.Errorf("EligibleConstellationThreads = %v, want exactly [%q] (the root, not the hidden fork %q)", got, root, forkID)
	}
}

// seedThread inserts a minimal thread row plus one message, so
// foreign-key-referencing tests (star_sources, shooting_star_runs) have a
// valid thread_id to point at, and eligibility-gate tests
// (EligibleConstellationThreads, keyed off messages' created_at/id) have
// something to gate on.
func seedThread(t *testing.T, s *Store) string {
	t.Helper()
	id := uuid.NewString()
	if err := s.CreateThread(id, "Test thread", "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage(id, "user", "hello", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	return id
}
