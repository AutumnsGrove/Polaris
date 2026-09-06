package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRunDailyPipeline_FullFirstDayRun exercises Stage A through D in one
// shot against a fake OpenRouter double — no real model credits, no
// network egress — the gap the handoff notes: existing tests cover each
// stage's helper function (dailyDiffJudge, dailyElectTopStory, ...) in
// isolation, but nothing previously drove runDailyPipeline itself end to
// end and checked what actually lands in the persisted edition row.
//
// Blocks are deliberately picked to dodge agent.Run's tool-calling loop
// (no SearXNG/Brave double needed): "quote" and "on_this_day" are plain
// no-tools pick calls, "headlines" and "tech_science" are
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
		plainSSEBody("On this day, a landmark treaty was signed that reshaped the region's borders."),      // Stage A: on_this_day (pick)
		plainSSEBody("Markets were quiet; one notable product launch dominated headlines today."),          // Stage A: headlines (research)
		plainSSEBody("A new open-weight model release was the big tech story today."),                      // Stage A: tech_science (research)
		toolCallSSEBody(`{"id":"call_1","type":"function","function":{"name":"elect_top_story","arguments":"{\"winner_key\":\"tech_science\"}"}}`), // Stage B
		plainSSEBody("Deeper dive: the open-weight release includes benchmarks showing strong reasoning gains, plus a pulled quote from the release notes."), // Stage C
	}
	srv := sequencedSSEServer(t, bodies)
	defer srv.Close()

	h := newTestHarness(t, srv.URL)

	resp := putDailyConfig(t, h, map[string]interface{}{
		"enabled_blocks":  []string{"quote", "on_this_day", "headlines", "tech_science"},
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
		t.Fatalf("got %d blocks, want 4 (quote, on_this_day, headlines, tech_science all survive on a first-ever day)", len(edition.Blocks))
	}
	if !edition.Blocks[0].IsTopStory || edition.Blocks[0].Key != "tech_science" {
		t.Errorf("edition.Blocks[0] = %+v, want tech_science elected as Top Story and sorted first", edition.Blocks[0])
	}
	if edition.Blocks[0].Content == "" || edition.Blocks[0].Content == "A new open-weight model release was the big tech story today." {
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
