package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"polaris/store"
	"polaris/tz"
)

func TestCometPeriodMath(t *testing.T) {
	// cometPeriodOf is the most recent COMPLETE month (the one a run "now"
	// describes); cometMonthOf is the month CONTAINING the time.
	oct18 := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	if got := cometPeriodOf(oct18); got != "2026-09" {
		t.Errorf("cometPeriodOf(Oct 18) = %q, want 2026-09", got)
	}
	if got := cometMonthOf(oct18); got != "2026-10" {
		t.Errorf("cometMonthOf(Oct 18) = %q, want 2026-10", got)
	}
	jan5 := time.Date(2027, time.January, 5, 0, 0, 0, 0, time.UTC)
	if got := cometPeriodOf(jan5); got != "2026-12" {
		t.Errorf("cometPeriodOf(Jan 5) = %q, want 2026-12 (year wraps)", got)
	}

	if got, ok := cometAddMonths("2026-12", 1); !ok || got != "2027-01" {
		t.Errorf("cometAddMonths(2026-12, 1) = %q,%v want 2027-01,true", got, ok)
	}
	if got, ok := cometAddMonths("2026-01", -1); !ok || got != "2025-12" {
		t.Errorf("cometAddMonths(2026-01, -1) = %q,%v want 2025-12,true", got, ok)
	}
	if _, ok := cometAddMonths("nonsense", 1); ok {
		t.Error("cometAddMonths of a malformed period should fail")
	}

	start, end, ok := cometPeriodBounds("2026-09")
	if !ok {
		t.Fatal("cometPeriodBounds(2026-09) not ok")
	}
	if start.Month() != time.September || start.Year() != 2026 || start.Day() != 1 {
		t.Errorf("start = %v, want Sept 1 2026", start)
	}
	if end.Month() != time.October || end.Day() != 1 {
		t.Errorf("end = %v, want Oct 1 2026", end)
	}
	if start.Location() != tz.Location() {
		t.Errorf("bounds should be in the active zone, got %v", start.Location())
	}

	if got := cometPeriodLabel("2026-09"); got != "September 2026" {
		t.Errorf("cometPeriodLabel = %q", got)
	}
	if got, ok := cometBaselineStart("2026-09", 90); !ok || got[:7] != "2026-06" {
		t.Errorf("cometBaselineStart 90d = %q,%v want a June 2026 start", got, ok)
	}
}

func TestCometDue(t *testing.T) {
	base := func() *store.CometConfig {
		return &store.CometConfig{Enabled: true, TimeOfDay: "06:00", CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 18, h, m, 0, 0, time.UTC) }

	if cometDue(&store.CometConfig{Enabled: false, TimeOfDay: "06:00"}, at(12, 0)) {
		t.Error("disabled Comet should never be due")
	}

	c := base()
	// Never generated: due once the month's slot has passed.
	if !cometDue(c, at(12, 0)) {
		t.Error("never-generated Comet should be due past the 1st")
	}
	// Before this month's scheduled moment, the most recent slot is last
	// month's; CreatedAt is Sep-1-ish, so not due yet on Oct 1 at 03:00.
	c2 := base()
	c2.CreatedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if cometDue(c2, time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)) {
		t.Error("before the 1st's 06:00 slot, Comet should not be due")
	}

	// Generated for this month already.
	c3 := base()
	gen := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)
	c3.LastGeneratedAt = &gen
	if cometDue(c3, at(12, 0)) {
		t.Error("already-generated Comet should not be due again this month")
	}

	// Failed attempt minutes ago → retry backoff holds it off.
	c4 := base()
	attempt := at(11, 55)
	c4.LastAttemptAt = &attempt
	if cometDue(c4, at(12, 0)) {
		t.Error("a recent failed attempt should be held off by the retry backoff")
	}
	// ...but an attempt past the backoff is due again.
	old := at(6, 0)
	c4.LastAttemptAt = &old
	if !cometDue(c4, at(12, 0)) {
		t.Error("an attempt older than the backoff should be retried")
	}
}

// TestApproachRun_RecordsTrail drives runApproach end to end against a fake
// model that calls record_classification, and checks the trail row, the
// hidden thread's existence/visibility, and that the run's cost is captured.
func TestApproachRun_RecordsTrail(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBodyCost(`{"id":"call_1","type":"function","function":{"name":"record_classification","arguments":"{\"topic\":\"technology\",\"mood\":\"curious\",\"intent\":\"pick a laptop\",\"resolved\":\"yes\"}"}}`, 0.0042),
	})
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	threadID := "e1111111-1111-1111-1111-111111111111"
	if err := h.db.CreateThread(threadID, "Laptop shopping", "mimo-pro", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.AddMessage(threadID, "user", "Which laptop should I get for Go development?", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.AddMessage(threadID, "assistant", "The Framework 13 is a strong pick because of its repair-friendly design and Linux support.", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatal(err)
	}

	period := cometMonthOf(time.Now())
	if err := h.srvObj.runApproach(threadID, period); err != nil {
		t.Fatalf("runApproach: %v", err)
	}

	trail, err := h.db.GetTrail(threadID, period)
	if err != nil || trail == nil {
		t.Fatalf("GetTrail = %v, %v; want a finished trail", trail, err)
	}
	if trail.Topic != "technology" || trail.Mood != "curious" || trail.Intent != "pick a laptop" || trail.Resolved != "yes" {
		t.Errorf("trail = %+v, want the model's classification", trail)
	}
	if trail.CostUSD <= 0 {
		t.Errorf("trail cost = %v, want the run's spend recorded", trail.CostUSD)
	}

	// A hidden thread exists (source = comet_approach) but is excluded from
	// the sidebar listing.
	if n, err := h.db.CountThreadsBySource("comet_approach"); err != nil || n != 1 {
		t.Fatalf("comet_approach thread count = %d, %v; want 1", n, err)
	}
	if n, err := h.db.CountThreadsBySource("web"); err != nil || n != 1 {
		t.Fatalf("web thread count = %d, %v; want 1", n, err)
	}
	summaries, err := h.db.ListThreads(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Errorf("ListThreads returned %d threads, want only the real one (hidden approach excluded)", len(summaries))
	}
}

// TestRunCometGeneration_BuildsEdition drives the assembly path with a fake
// model forced to call record_trends, and checks the four blocks land.
func TestRunCometGeneration_BuildsEdition(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBodyCost(`{"id":"call_1","type":"function","function":{"name":"record_trends","arguments":"{\"spiked\":[{\"topic\":\"Docker\",\"count\":5}],\"faded\":[{\"topic\":\"Kubernetes\",\"count\":3,\"since\":\"quiet since Aug 1\"}],\"hook\":\"A Docker-heavy month.\",\"highlights\":[{\"index\":1,\"why\":\"the thread that framed the month\"}]}"}}`, 0.003),
	})
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	period := cometMonthOf(time.Now())

	// A trail so the category breakdown has something to group, a star for
	// the showcase, and a web_search event for the synthesis input.
	threadID := "f1111111-1111-1111-1111-111111111111"
	if err := h.db.CreateThread(threadID, "Docker notes", "mimo-pro", "web"); err != nil {
		t.Fatal(err)
	}
	// A user message makes the thread a highlight candidate and gives the
	// rhythm block a real timestamp.
	if _, err := h.db.AddMessage(threadID, "user", "how does docker compose networking work?", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatal(err)
	}
	tid, err := h.db.StartTrail(threadID, period)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.FinishTrail(tid, "software engineering", "curious", "figure out networking", "yes", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateStar(store.Star{Title: "Runs Docker Compose", Category: "software engineering", Status: "auto"}); err != nil {
		t.Fatal(err)
	}
	// A trail in an earlier period, so this period's topic reads as new
	// ground rather than "the first period, everything is new".
	priorThread := "a2222222-2222-2222-2222-222222222222"
	if err := h.db.CreateThread(priorThread, "prior", "mimo-pro", "web"); err != nil {
		t.Fatal(err)
	}
	pid, err := h.db.StartTrail(priorThread, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.FinishTrail(pid, "technology", "curious", "x", "yes", 0); err != nil {
		t.Fatal(err)
	}
	h.db.LogEvent(threadID, "info", "tool.web_search", "tool call started", map[string]interface{}{"args": map[string]interface{}{"query": "docker compose networking"}}, "turn-1")
	h.db.LogEvent(threadID, "info", "tool.web_search", "tool call finished", map[string]interface{}{}, "turn-1")
	h.db.LogEvent(threadID, "info", "tool.create_star", "tool call finished", map[string]interface{}{}, "turn-1")

	if err := h.srvObj.runCometGeneration(context.Background(), period); err != nil {
		t.Fatalf("runCometGeneration: %v", err)
	}

	edition, err := h.db.GetCometEdition(period)
	if err != nil {
		t.Fatalf("GetCometEdition: %v", err)
	}
	if edition.Hook != "A Docker-heavy month." {
		t.Errorf("hook = %q, want the synthesis hook", edition.Hook)
	}
	if len(edition.Data.Categories) == 0 || edition.Data.Categories[0].Name != "software engineering" {
		t.Errorf("categories = %+v, want software engineering present", edition.Data.Categories)
	}
	if len(edition.Data.Spiked) != 1 || edition.Data.Spiked[0].Topic != "Docker" {
		t.Errorf("spiked = %+v", edition.Data.Spiked)
	}
	if len(edition.Data.Faded) != 1 || edition.Data.Faded[0].Since != "quiet since Aug 1" {
		t.Errorf("faded = %+v", edition.Data.Faded)
	}
	if edition.Data.Stats.Searches < 1 {
		t.Errorf("stat row searches = %d, want the logged web_search counted", edition.Data.Stats.Searches)
	}
	if len(edition.Data.Highlights) != 1 || edition.Data.Highlights[0].ThreadID != threadID {
		t.Errorf("highlights = %+v, want the candidate thread resolved from index 1", edition.Data.Highlights)
	}
	if len(edition.Data.Highlights) == 1 && edition.Data.Highlights[0].Why != "the thread that framed the month" {
		t.Errorf("highlight why = %q", edition.Data.Highlights[0].Why)
	}
	if edition.Data.Rhythm.DaysInPeriod == 0 || edition.Data.Rhythm.ActiveDays == 0 {
		t.Errorf("rhythm = %+v, want a populated day grid", edition.Data.Rhythm)
	}
	if len(edition.Data.Tools) == 0 || edition.Data.Tools[0].Name != "web_search" {
		t.Errorf("tools = %+v, want web_search counted", edition.Data.Tools)
	}
	if len(edition.Data.Firsts) != 1 || edition.Data.Firsts[0].Name != "software engineering" {
		t.Errorf("firsts = %+v, want software engineering as new ground", edition.Data.Firsts)
	}
	if edition.CostUSD <= 0 {
		t.Errorf("edition cost = %v, want the synthesis spend recorded", edition.CostUSD)
	}
	if cfg, _ := h.db.GetCometConfig(); cfg.LastGeneratedAt == nil {
		t.Error("runCometGeneration should stamp last_generated_at on success")
	}
}

// TestComputeCometRhythm checks the day/hour bucketing, including that the
// peak and busiest day are picked from real counts.
func TestComputeCometRhythm(t *testing.T) {
	loc := tz.Location()
	times := []time.Time{
		time.Date(2026, 9, 1, 9, 0, 0, 0, loc),
		time.Date(2026, 9, 1, 22, 0, 0, 0, loc),
		time.Date(2026, 9, 3, 22, 0, 0, 0, loc),
	}
	r := computeCometRhythm("2026-09", times)
	if r.DaysInPeriod != 30 {
		t.Errorf("DaysInPeriod = %d, want 30", r.DaysInPeriod)
	}
	if r.ActiveDays != 2 {
		t.Errorf("ActiveDays = %d, want 2", r.ActiveDays)
	}
	if r.Hours[22] != 2 || r.Hours[9] != 1 {
		t.Errorf("Hours = %v, want 2 at 22 and 1 at 9", r.Hours)
	}
	if r.PeakHour != 22 {
		t.Errorf("PeakHour = %d, want 22", r.PeakHour)
	}
	if r.BusiestCount != 2 || r.BusiestDate != "2026-09-01" {
		t.Errorf("busiest = %d on %s, want 2 on 2026-09-01", r.BusiestCount, r.BusiestDate)
	}
	if len(r.Days) != 30 || r.Days[0].Date != "2026-09-01" {
		t.Errorf("Days grid = %d entries starting %q", len(r.Days), func() string {
			if len(r.Days) > 0 {
				return r.Days[0].Date
			}
			return ""
		}())
	}
}

func cometPost(t *testing.T, h *testHarness, path string) (int, string) {
	t.Helper()
	resp, err := http.Post(h.url(path), "application/json", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func cometBusy(t *testing.T, h *testHarness) bool {
	t.Helper()
	resp, err := http.Get(h.url("/api/busy"))
	if err != nil {
		t.Fatalf("GET /api/busy: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Busy bool `json:"busy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding /api/busy: %v", err)
	}
	return body.Busy
}

func TestHandleCometClassify_MalformedPeriod_Is400(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	if code, _ := cometPost(t, h, "/api/comet/classify?period=nonsense"); code != http.StatusBadRequest {
		t.Errorf("classify with a malformed period = %d, want 400 (not a 500 from deep inside the run)", code)
	}
}

func TestHandleCometBackfill_BadBounds_Is400(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	for _, q := range []string{"from=nonsense", "to=2026-13", "from=2026-09&to=2026-03"} {
		if code, _ := cometPost(t, h, "/api/comet/backfill?"+q); code != http.StatusBadRequest {
			t.Errorf("backfill?%s = %d, want 400", q, code)
		}
	}
}

func TestHandleCometClassify_WhileAnotherRunHoldsTheFlag_Is409(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	h.srvObj.cometGenerationRunning.Store(true)

	if code, _ := cometPost(t, h, "/api/comet/classify?period=2026-09"); code != http.StatusConflict {
		t.Errorf("classify during a running pipeline = %d, want 409", code)
	}
	if code, _ := cometPost(t, h, "/api/comet/backfill"); code != http.StatusConflict {
		t.Errorf("backfill during a running pipeline = %d, want 409", code)
	}
	if !cometBusy(t, h) {
		t.Error("/api/busy should report busy while a Comet run holds the flag")
	}
}

func TestHandleCometClassify_ReleasesTheFlagWhenDone(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	if code, body := cometPost(t, h, "/api/comet/classify?period=2026-09"); code != http.StatusOK {
		t.Fatalf("classify over an empty database = %d (%s), want 200", code, body)
	}
	if cometBusy(t, h) {
		t.Error("/api/busy still reports busy after the classify run finished — the flag leaked")
	}
}

// TestRunCometGeneration_DropsBogusHighlightsAndNeverStoresNullCollections
// covers two properties of the synthesis → storage path at once: a highlight
// whose index isn't in the candidate list is dropped (a model can't invent a
// thread id), and a month with no spikes/fades/stars/etc. stores `[]`, not
// `null` — the null that once froze the page's .length calls.
func TestRunCometGeneration_DropsBogusHighlightsAndNeverStoresNullCollections(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBodyCost(`{"id":"call_1","type":"function","function":{"name":"record_trends","arguments":"{\"spiked\":[],\"faded\":[],\"hook\":\"A quiet month.\",\"highlights\":[{\"index\":9,\"why\":\"out of range\"},{\"index\":0,\"why\":\"zero\"}]}"}}`, 0.001),
	})
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	period := cometMonthOf(time.Now())
	threadID := "b3333333-3333-3333-3333-333333333333"
	if err := h.db.CreateThread(threadID, "Only thread", "mimo-pro", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.AddMessage(threadID, "user", "hello", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatal(err)
	}

	if err := h.srvObj.runCometGeneration(context.Background(), period); err != nil {
		t.Fatalf("runCometGeneration: %v", err)
	}

	edition, err := h.db.GetCometEdition(period)
	if err != nil {
		t.Fatal(err)
	}
	if len(edition.Data.Highlights) != 0 {
		t.Errorf("highlights = %+v, want the out-of-range indexes dropped", edition.Data.Highlights)
	}
	// `[]` decodes to a non-nil empty slice, `null` to nil — so nil here means
	// the stored JSON held a null collection.
	d := edition.Data
	if d.Categories == nil || d.Spiked == nil || d.Faded == nil || d.Stars == nil || d.Highlights == nil || d.Tools == nil || d.Firsts == nil {
		t.Errorf("stored edition has a null collection: %+v", d)
	}
}

func TestHandleGenerateCometNow_EmptyPeriod_Is422WithAReadableReason(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	code, body := cometPost(t, h, "/api/comet/generate?period=2026-09")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("generate for a month with no messages = %d (%s), want 422", code, body)
	}
	if !strings.Contains(body, "No conversations in September 2026") {
		t.Errorf("body = %q, want a message naming the month", body)
	}
	if h.srvObj.cometGenerationRunning.Load() {
		t.Error("an empty-period refusal must not leave the single-flight flag held")
	}
}

func TestRunCometGeneration_EmptyPeriod_ReturnsErrCometNoDataAndStoresNothing(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid") // any LLM call would fail loudly
	err := h.srvObj.runCometGeneration(context.Background(), "2026-09")
	if !errors.Is(err, ErrCometNoData) {
		t.Fatalf("runCometGeneration over an empty month = %v, want ErrCometNoData", err)
	}
	if _, gerr := h.db.GetCometEdition("2026-09"); !errors.Is(gerr, store.ErrCometEditionNotFound) {
		t.Errorf("GetCometEdition = %v, want no edition stored for an empty month", gerr)
	}
}

func TestRunCometPipeline_EmptyPeriod_SkipsQuietlyAndIsNotDueAgain(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	if err := h.db.UpdateCometConfig(true, "06:00", ""); err != nil {
		t.Fatal(err)
	}
	h.srvObj.runCometPipeline(context.Background(), "2026-09")

	cfg, err := h.db.GetCometConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastGeneratedAt == nil {
		t.Error("an empty month should be stamped as handled, or cometDue re-offers it every hour until the 1st")
	}
	if _, gerr := h.db.GetCometEdition("2026-09"); !errors.Is(gerr, store.ErrCometEditionNotFound) {
		t.Errorf("GetCometEdition = %v, want no edition stored", gerr)
	}
}

func TestBackfillComet_SkipsMonthsWithNoMessages(t *testing.T) {
	h := newTestHarness(t, "http://unused.invalid")
	results, err := h.srvObj.BackfillComet(context.Background(), "2026-07", "2026-08")
	if err != nil {
		t.Fatalf("BackfillComet: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per month", results)
	}
	for _, r := range results {
		if !r.Skipped || r.Error != "" || r.Generated {
			t.Errorf("result %+v, want skipped with no error and nothing generated", r)
		}
	}
}
