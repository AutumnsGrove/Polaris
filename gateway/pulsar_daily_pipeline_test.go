package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"polaris/store"
)

// TestRunDailyPipeline_FullFirstDayRun exercises Stage A through D in one
// shot against a fake OpenRouter double — no real model credits, no
// network egress — the gap the handoff notes: existing tests cover each
// stage's helper function (dailyDiffJudge, dailyElectTopStory, ...) in
// isolation, but nothing previously drove runDailyPipeline itself end to
// end and checked what actually lands in the persisted edition row.
//
// Blocks are deliberately picked to dodge agent.Run's tool-calling loop
// (no SearXNG/Brave double needed): "quote" is a plain no-tools pick
// call; "on_this_day", "headlines", and "trending" are all
// dailyBlockResearch but a model that replies in plain prose with no
// tool call makes agent.Run terminate after one round anyway, same as a
// real "the model didn't need a tool" turn. Four blocks, not fewer —
// dailyMinBlockCount's floor of 4 would otherwise collapse this run into
// the degraded-notice path being tested separately below. Fresh DB means
// hasYesterday is false, so both Watch blocks skip the diff-judge
// entirely and default to "notable" (generateOneDailyBlock's "First-ever
// day" branch) — that's what guarantees exactly one Stage B election
// call and one Stage C elaboration call, not a variable number.
func TestRunDailyPipeline_FullFirstDayRun(t *testing.T) {
	bodies := []string{
		plainSSEBody("Quote: \"Stay hungry, stay foolish.\" Worth remembering because it still holds up."), // Stage A: quote (pick)
		plainSSEBody("On this day, a landmark treaty was signed that reshaped the region's borders."),      // Stage A: on_this_day (research)
		plainSSEBody("Markets were quiet; one notable product launch dominated headlines today."),          // Stage A: headlines (research)
		plainSSEBody("A new open-weight model release was the big story trending today."),                  // Stage A: trending (research)
		toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"elect_top_story","arguments":"{\"winner_key\":\"trending\",\"reasoning\":\"The open-weight release is a bigger development than a quiet headlines day\"}"}}`), // Stage B
		plainSSEBody("Deeper dive: the open-weight release includes benchmarks showing strong reasoning gains, plus a pulled quote from the release notes."), // Stage C
	}
	srv := sequencedSSEServer(t, bodies)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day", "headlines", "trending"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT daily config status = %d, want 200", resp.StatusCode)
	}

	h.srvObj.runDailyPipeline(context.Background())

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}

	if len(edition.Blocks) != 4 {
		t.Fatalf("got %d blocks, want 4 (quote, on_this_day, headlines, trending all survive on a first-ever day)", len(edition.Blocks))
	}
	if !edition.Blocks[0].IsTopStory || edition.Blocks[0].Key != "trending" {
		t.Errorf("edition.Blocks[0] = %+v, want trending elected as Top Story and sorted first", edition.Blocks[0])
	}
	if edition.Blocks[0].Content == "" || edition.Blocks[0].Content == "A new open-weight model release was the big story trending today." {
		t.Errorf("top story content = %q, want the Stage C elaborated version, not the Stage A quick version", edition.Blocks[0].Content)
	}
	found := map[string]bool{}
	for _, b := range edition.Blocks[1:] {
		found[b.Key] = true
		if b.IsTopStory {
			t.Errorf("non-elected block %q incorrectly marked IsTopStory", b.Key)
		}
	}
	for _, key := range []string{"quote", "on_this_day", "headlines"} {
		if !found[key] {
			t.Errorf("edition.Blocks = %+v, want %q present as a plain card", edition.Blocks, key)
		}
	}

	if edition.CostUSD <= 0 {
		t.Errorf("edition.CostUSD = %v, want a positive sum across every stage's LLM call", edition.CostUSD)
	}

	cfgRow, err := h.db.GetDailyConfig()
	if err != nil {
		t.Fatalf("GetDailyConfig: %v", err)
	}
	if cfgRow.LastGeneratedAt == nil {
		t.Error("LastGeneratedAt not recorded after a real pipeline run")
	}

	// The trace table is what makes "why did this happen" answerable
	// after the fact — see pulsar_daily_trace's schema comment. Every
	// enabled block should have a row, the elected Top Story's should
	// carry Stage B's reasoning and Stage C's elaborated content, and a
	// plain included block should be marked included without being
	// mistaken for the Top Story.
	trace, err := h.db.GetDailyTrace(today)
	if err != nil {
		t.Fatalf("GetDailyTrace: %v", err)
	}
	if len(trace) != 4 {
		t.Fatalf("GetDailyTrace = %d rows, want 4 (one per enabled block)", len(trace))
	}
	traceByKey := map[string]store.PulsarDailyBlockTrace{}
	for _, tr := range trace {
		traceByKey[tr.BlockKey] = tr
	}
	topStoryTrace, ok := traceByKey["trending"]
	if !ok || !topStoryTrace.IsTopStory || !topStoryTrace.Included {
		t.Errorf("trending trace = %+v, want IsTopStory and Included both true", topStoryTrace)
	}
	if topStoryTrace.StageCContent == "" || topStoryTrace.StageCContent == topStoryTrace.StageAContent {
		t.Errorf("trending trace.StageCContent = %q, want the Stage C elaborated version recorded, not empty or identical to Stage A", topStoryTrace.StageCContent)
	}
	if topStoryTrace.TopStoryReasoning == "" {
		t.Error("trending trace.TopStoryReasoning is empty, want Stage B's stated reasoning recorded")
	}
	quoteTrace, ok := traceByKey["quote"]
	if !ok || quoteTrace.IsTopStory || !quoteTrace.Included {
		t.Errorf("quote trace = %+v, want Included true and IsTopStory false", quoteTrace)
	}
}

// TestRunDailyPipeline_ItemizedTopStory covers the per-story expansion
// path (finalize_daily_items) end to end — a list-shaped block
// (headlines) that calls the tool instead of replying in prose, wins Top
// Story, and gets Stage C elaboration applied to its single most
// significant item rather than the whole list. Verifies both halves of
// the "front-page tease, section still has it" design: the synthetic Top
// Story entry is keyed "top_story" (not "headlines" — see
// runDailyPipeline's topStoryBlockKey comment) and carries the
// elaborated single-item content, while headlines' own regular card
// still carries all its original Items untouched.
//
// All 4 Stage A bodies are identical (the same finalize_daily_items call)
// rather than one-per-block, deliberately: Stage A's blocks fire as
// concurrent goroutines against sequencedSSEServer, which assigns bodies
// by arrival order, not by which logical block asked — the same reason
// TestRunDailyPipeline_FullFirstDayRun's assertions only check aggregate
// facts, never "block X definitely got body Y". Making every Stage A
// slot serve the same scripted response sidesteps that race instead of
// fighting it: quote (a plain no-tools pick call) just gets empty content
// from a tool-call-shaped body it doesn't know how to parse, harmless
// since quote's actual content is never asserted here.
func TestRunDailyPipeline_ItemizedTopStory(t *testing.T) {
	itemsArgs := `{"items":[` +
		`{"title":"Nvidia buys Hugging Face","summary":"Nvidia announced a $13B acquisition of the open-weight hosting platform.","source":"TechCrunch","url":"https://techcrunch.com/nvidia-hf"},` +
		`{"title":"New open-weight model ships","summary":"A rival lab shipped a new open-weight model with strong benchmark gains.","source":"The Verge","url":"https://theverge.com/new-model"}` +
		`]}`
	itemsBody := toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"finalize_daily_items","arguments":` + fmt.Sprintf("%q", itemsArgs) + `}}`)
	bodies := []string{
		itemsBody, // Stage A: quote (pick — tool call ignored, ends up with empty content, unasserted)
		itemsBody, // Stage A: on_this_day (research, itemized)
		itemsBody, // Stage A: headlines (research, itemized)
		itemsBody, // Stage A: trending (research, itemized)
		toolCallSSEBody(`{"id":"call_2","type":"function","function":{"name":"elect_top_story","arguments":"{\"winner_key\":\"headlines\",\"reasoning\":\"The Nvidia acquisition is the bigger development\"}"}}`), // Stage B
		plainSSEBody("Deeper dive: the Nvidia acquisition includes board seats and a multi-year compute commitment."), // Stage C, elaborating item[0] only
	}
	srv := sequencedSSEServer(t, bodies)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day", "headlines", "trending"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT daily config status = %d, want 200", resp.StatusCode)
	}

	h.srvObj.runDailyPipeline(context.Background())

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}

	// 5 blocks, not 4: headlines won Top Story but, being itemized, still
	// keeps its own regular card too (see runDailyPipeline's Stage C/D).
	if len(edition.Blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (quote, on_this_day, trending, headlines' own card, plus the synthetic top_story entry)", len(edition.Blocks))
	}

	top := edition.Blocks[0]
	if !top.IsTopStory || top.Key != "top_story" {
		t.Errorf("edition.Blocks[0] = %+v, want Key=\"top_story\" (not \"headlines\" — that key is reserved for headlines' own regular card)", top)
	}
	if top.Title != "Nvidia buys Hugging Face" {
		t.Errorf("top.Title = %q, want the elected item's own title, not the parent block's title", top.Title)
	}
	if top.Content == "" || top.Content == "Nvidia announced a $13B acquisition of the open-weight hosting platform." {
		t.Errorf("top.Content = %q, want the Stage C elaborated version, not the item's raw quick summary", top.Content)
	}

	var headlinesCard *store.PulsarDailyBlock
	for i := range edition.Blocks {
		if edition.Blocks[i].Key == "headlines" {
			headlinesCard = &edition.Blocks[i]
		}
	}
	if headlinesCard == nil {
		t.Fatal("no block with Key \"headlines\" found — the parent list block should still render its own card")
	}
	if headlinesCard.IsTopStory {
		t.Error("headlines' own card incorrectly marked IsTopStory — only the synthetic top_story entry should be")
	}
	if len(headlinesCard.Items) != 2 {
		t.Fatalf("headlines.Items = %+v, want both original items intact, including the one promoted to Top Story", headlinesCard.Items)
	}
	if headlinesCard.Items[0].Title != "Nvidia buys Hugging Face" || headlinesCard.Items[1].Title != "New open-weight model ships" {
		t.Errorf("headlines.Items = %+v, want both items in their original order", headlinesCard.Items)
	}
}

// TestRunDailyPipeline_BelowFloorShowsDegradedNotice covers Stage D's
// "something's actually broken" floor: every enabled block generation
// failing (a model that always errors, here simulated by pointing at a
// server that isn't a valid SSE endpoint) must persist the single
// "notice" block instead of an edition with too few real blocks to be
// useful, and must not leave the config's last_generated_at unset (a
// crash-recovery concern separate from a normal "everything failed"
// run).
func TestRunDailyPipeline_BelowFloorShowsDegradedNotice(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1") // unroutable — every LLM call fails

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	resp.Body.Close()

	h.srvObj.runDailyPipeline(context.Background())

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}
	if len(edition.Blocks) != 1 || edition.Blocks[0].Key != "notice" {
		t.Errorf("edition.Blocks = %+v, want the single degraded-notice block below dailyMinBlockCount", edition.Blocks)
	}

	// Every block that failed should still leave a real trace row with
	// its actual error recorded — this is exactly the case that used to
	// be unanswerable after the fact (a degraded edition with no way to
	// know which blocks tried and failed vs. were never enabled at all).
	trace, err := h.db.GetDailyTrace(today)
	if err != nil {
		t.Fatalf("GetDailyTrace: %v", err)
	}
	if len(trace) != 2 {
		t.Fatalf("GetDailyTrace = %d rows, want 2 (one per enabled block, even though both failed)", len(trace))
	}
	for _, tr := range trace {
		if tr.Error == "" {
			t.Errorf("trace for %q has no Error recorded, want the connection-refused failure captured", tr.BlockKey)
		}
		if tr.Included {
			t.Errorf("trace for %q has Included=true, want false — it never made the degraded edition", tr.BlockKey)
		}
	}
}

// TestRunDailyPipeline_CustomBlock exercises a user-authored "general
// purpose" block (store.PulsarDailyConfig.CustomBlocks) end to end —
// same dailyBlockResearch execution path as a fixed registry block
// (agent.Run against the research toolset), just with no fixed key to
// look a task template up by: the entire task comes verbatim from the
// block's own Instructions field. First-ever day, so it defaults to
// "notable" and is the only Watch candidate — guaranteed to be elected
// Top Story.
func TestRunDailyPipeline_CustomBlock(t *testing.T) {
	bodies := []string{
		plainSSEBody("Quote: \"Stay hungry, stay foolish.\" Worth remembering because it still holds up."), // Stage A: quote (pick)
		plainSSEBody("On this day, a landmark treaty was signed that reshaped the region's borders."),      // Stage A: on_this_day (research)
		plainSSEBody("Otiose — serving no practical purpose. From Latin otium, \"leisure\"."),               // Stage A: word_of_day (pick)
		plainSSEBody("NVDA closed at $142.50, up 2%. AAPL closed at $228.10, roughly flat on the day."),     // Stage A: custom block (research)
		toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"elect_top_story","arguments":"{\"winner_key\":\"custom_stocks\",\"reasoning\":\"Only notable candidate today\"}"}}`), // Stage B
		plainSSEBody("Deeper dive on today's close: NVDA and AAPL both traded within their recent range."),  // Stage C
	}
	srv := sequencedSSEServer(t, bodies)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day", "word_of_day"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
		"custom_blocks": []map[string]string{
			{"key": "custom_stocks", "title": "Stock Watchlist", "instructions": "Check today's closing prices for NVDA and AAPL and report them."},
		},
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT daily config status = %d, want 200", resp.StatusCode)
	}

	h.srvObj.runDailyPipeline(context.Background())

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}
	if len(edition.Blocks) != 4 {
		t.Fatalf("got %d blocks, want 4 (quote, on_this_day, word_of_day, custom_stocks)", len(edition.Blocks))
	}
	if !edition.Blocks[0].IsTopStory || edition.Blocks[0].Key != "custom_stocks" {
		t.Errorf("edition.Blocks[0] = %+v, want custom_stocks elected Top Story (the only Watch candidate)", edition.Blocks[0])
	}
	if edition.Blocks[0].Title != "Stock Watchlist" {
		t.Errorf("edition.Blocks[0].Title = %q, want the user-authored title carried through", edition.Blocks[0].Title)
	}

	trace, err := h.db.GetDailyTrace(today)
	if err != nil {
		t.Fatalf("GetDailyTrace: %v", err)
	}
	found := false
	for _, tr := range trace {
		if tr.BlockKey == "custom_stocks" {
			found = true
			if tr.StageAContent == "" {
				t.Error("custom block trace.StageAContent is empty, want the generated content recorded")
			}
		}
	}
	if !found {
		t.Errorf("GetDailyTrace = %+v, want a row for the custom block", trace)
	}
}

// diffJudgeMatchServer serves toolCallBody only to the one request whose
// body identifies it as the diff-judge call for blockTitle (dailyDiffJudge's
// system message always contains "You are comparing yesterday's and
// today's content" plus the block's own title — see its doc comment),
// and genericBody to every other request. Matching on request content
// rather than arrival order, unlike sequencedSSEServer above, is what
// makes it possible to control a single specific call's outcome when
// Stage A's blocks fire as genuinely concurrent goroutines (see
// TestRunDailyPipeline_ItemizedTopStory's doc comment on why that FIFO
// queue can't be trusted to hand a scripted response to a specific
// logical call).
func diffJudgeMatchServer(t *testing.T, blockTitle, genericBody, toolCallBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if strings.Contains(string(body), "You are comparing yesterday's and today's content") &&
			strings.Contains(string(body), blockTitle) {
			fmt.Fprint(w, toolCallBody)
		} else {
			fmt.Fprint(w, genericBody)
		}
		flusher.Flush()
	}))
}

// TestRunDailyPipeline_UnchangedBlockDroppedFromEdition covers the
// diff-judge's signature behavior end to end: a Watch block whose verdict
// comes back "unchanged" against a real seeded "yesterday" edition must
// be excluded from the persisted edition entirely, not just have its
// verdict recorded somewhere. Every other runDailyPipeline test either
// has no "yesterday" at all — every Watch block then defaults to
// "notable", skipping the diff-judge call outright, see
// generateOneDailyBlock's "First-ever day" branch — or drives a hard
// failure; none seed real prior content and confirm the drop actually
// happens.
//
// headlines is the only enabled Watch block, so Stage B never fires (zero
// "notable" candidates) — nothing to script for Stage B/C. The other four
// enabled blocks are all non-Watch fresh picks, chosen so the edition
// lands at exactly dailyMinBlockCount after headlines drops out,
// distinguishing "one block correctly excluded" from
// TestRunDailyPipeline_BelowFloorShowsDegradedNotice's "everything
// failed" degraded-notice path.
func TestRunDailyPipeline_UnchangedBlockDroppedFromEdition(t *testing.T) {
	genericBody := plainSSEBody("A generic block reply — content doesn't matter for this test's assertions.")
	unchangedVerdict := toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"record_verdict","arguments":"{\"verdict\":\"unchanged\",\"gist\":\"Same as yesterday\",\"reasoning\":\"No new development since yesterday's report\"}"}}`)
	srv := diffJudgeMatchServer(t, "Top Headlines", genericBody, unchangedVerdict)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := h.db.UpsertDailyEdition(yesterday, []store.PulsarDailyBlock{
		{Key: "headlines", Title: "Top Headlines", Content: "Yesterday: markets closed flat, no major news."},
	}, 0); err != nil {
		t.Fatalf("seeding yesterday's edition: %v", err)
	}

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"headlines", "quote", "word_of_day", "on_this_day", "sports"},
		"sports_teams":    "Lakers",
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "07:00",
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT daily config status = %d, want 200", resp.StatusCode)
	}

	h.srvObj.runDailyPipeline(context.Background())

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}

	for _, b := range edition.Blocks {
		if b.Key == "headlines" {
			t.Errorf("edition.Blocks contains %q, want it dropped as unchanged", b.Key)
		}
	}
	if len(edition.Blocks) != dailyMinBlockCount {
		t.Fatalf("got %d blocks, want exactly %d (the 4 fresh-pick blocks; headlines dropped, not collapsed into a degraded notice)", len(edition.Blocks), dailyMinBlockCount)
	}

	trace, err := h.db.GetDailyTrace(today)
	if err != nil {
		t.Fatalf("GetDailyTrace: %v", err)
	}
	var headlinesTrace *store.PulsarDailyBlockTrace
	for i := range trace {
		if trace[i].BlockKey == "headlines" {
			headlinesTrace = &trace[i]
		}
	}
	if headlinesTrace == nil {
		t.Fatal("no trace row for headlines — a dropped block should still leave a real trace record")
	}
	if headlinesTrace.Verdict != "unchanged" {
		t.Errorf("headlines trace.Verdict = %q, want %q", headlinesTrace.Verdict, "unchanged")
	}
	if headlinesTrace.Included {
		t.Error("headlines trace.Included = true, want false — it never made the persisted edition")
	}
}

// TestHandleGenerateDailyNow drives the manual "Generate now" trigger
// through the real HTTP handler, not runDailyPipeline directly — the
// previous only way to force a real run for testing was the time_of_day
// scheduler-tick workaround documented in pulsar_daily.go's isDailyDue,
// which has no place in the actual product. Same four-cheap-blocks setup
// as TestRunDailyPipeline_FullFirstDayRun, just triggered over HTTP and
// waited out via WaitForActiveTurns (the same shutdown-drain mechanism
// runDailyPipelineRecovered registers with) instead of calling the
// pipeline function synchronously.
func TestHandleGenerateDailyNow(t *testing.T) {
	bodies := []string{
		plainSSEBody("Quote: \"Stay hungry, stay foolish.\" Worth remembering because it still holds up."),
		plainSSEBody("On this day, a landmark treaty was signed that reshaped the region's borders."),
		plainSSEBody("Markets were quiet; one notable product launch dominated headlines today."),
		plainSSEBody("A new open-weight model release was the big story trending today."),
		toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"elect_top_story","arguments":"{\"winner_key\":\"trending\",\"reasoning\":\"Bigger than a quiet headlines day\"}"}}`),
		plainSSEBody("Deeper dive on the open-weight release."),
	}
	srv := sequencedSSEServer(t, bodies)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day", "headlines", "trending"},
		"architect_model": "deepseek-pro",
		"writer_model":    "deepseek",
		"time_of_day":     "23:59", // far in the future — only the manual trigger should fire this run
	})
	resp.Body.Close()

	genResp, err := http.Post(h.url("/api/pulsar/daily/generate"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST generate: %v", err)
	}
	defer genResp.Body.Close()
	if genResp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 Accepted (fire-and-forget — the pipeline runs in the background)", genResp.StatusCode)
	}

	// A second click while the first run is still in flight must be
	// rejected, not queue a wasteful overlapping run.
	genResp2, err := http.Post(h.url("/api/pulsar/daily/generate"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST generate (second, concurrent): %v", err)
	}
	defer genResp2.Body.Close()
	if genResp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 Conflict for a generation already in flight", genResp2.StatusCode)
	}

	h.srvObj.BeginShutdown()
	if err := h.srvObj.WaitForActiveTurns(context.Background()); err != nil {
		t.Fatalf("waiting for the triggered generation to finish: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	edition, err := h.db.GetDailyEdition(today)
	if err != nil {
		t.Fatalf("GetDailyEdition: %v", err)
	}
	if len(edition.Blocks) != 4 {
		t.Errorf("edition.Blocks = %+v, want the 4-block run the manual trigger actually produced", edition.Blocks)
	}
}

// plainSSEBody is sseTextOnlyServer's per-response body, standalone so
// sequencedSSEServer (which serves one canned body per request rather
// than owning its own handler like sseTextOnlyServer does) can mix plain
// prose replies with toolCallSSEBody replies across one fake model's
// sequence of stage calls.
func plainSSEBody(content string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n", content) +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"cost\":0.0001}}\n" +
		"data: [DONE]\n"
}
