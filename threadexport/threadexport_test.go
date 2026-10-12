package threadexport

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

// fixedNow freezes ExportedAt for golden comparisons.
var fixedNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// fixtureThread is a small thread covering the shapes Phase 1 builds:
// an answered turn with citations and a ```ui fence, a turn with an
// attachment, and a trailing unanswered turn.
func fixtureThread() Input {
	created := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	return Input{
		AppVersion: "r200.test",
		Thread: Thread{
			ID:        "th_abc",
			Title:     "Question about tea",
			CreatedAt: created,
			UpdatedAt: created.Add(2 * time.Hour),
			Model:     "anthropic/claude-sonnet-4.5",
			Source:    "web",
		},
		Messages: []Message{
			{ID: 1, Role: "user", TurnID: "turn-1", CreatedAt: created, Content: "What is oolong?"},
			{
				ID: 2, Role: "assistant", TurnID: "turn-1", CreatedAt: created.Add(5 * time.Second),
				Content:   "Oolong is a semi-oxidized tea.\n\n```ui\n{\"c\":\"callout\",\"tone\":\"answer\",\"text\":\"Semi-oxidized\",\"asof\":\"2026-10\"}\n```\n",
				Citations: `[{"title":"Oolong - Wikipedia","url":"https://en.wikipedia.org/wiki/Oolong","site_name":"Wikipedia"}]`,
				CostUSD:   0.0123, DurationMs: 4200, AppliedModel: "anthropic/claude-sonnet-4.5",
				Transcript: `[
				  {"role":"user","content":"What is oolong?"},
				  {"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"oolong\"}"}}]},
				  {"role":"tool","tool_call_id":"c1","content":"1. Oolong — Wikipedia: semi-oxidized tea."},
				  {"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"memory","arguments":"{\"action\":\"write\",\"name\":\"tea\"}"}}]},
				  {"role":"tool","tool_call_id":"c2","content":"Saved memory: tea"}
				]`,
			},
			{
				ID: 3, Role: "user", TurnID: "turn-2", CreatedAt: created.Add(time.Minute),
				Content:     "Here are my tasting notes.",
				Attachments: `[{"filename":"notes.txt","content_type":"text/plain","workspace_file_id":"a1b2.txt"}]`,
			},
			{
				ID: 4, Role: "assistant", TurnID: "turn-2", CreatedAt: created.Add(2 * time.Minute),
				Content: "Thanks — noted.", CostUSD: 0.001, DurationMs: 1200,
			},
			{ID: 5, Role: "user", TurnID: "turn-3", CreatedAt: created.Add(3 * time.Minute), Content: "And green tea?"},
		},
		Events: []Event{
			{TurnID: "turn-1", Source: "turn", Message: "reasoning", Data: `{"content":"I should search for oolong.","duration_ms":1500}`},
			{TurnID: "turn-1", Source: "turn", Message: "reasoning", Data: `{"content":"Checking a second angle.","duration_ms":800,"agent_id":"researcher-1"}`},
			// turn-2 predates transcripts, so its trail comes from events.
			{TurnID: "turn-2", Source: "tool.web_read", Message: "tool call started", Data: `{"args":{"url":"https://example.com/notes"},"call_id":"r1"}`},
			{TurnID: "turn-2", Source: "tool.web_read", Message: "tool call finished", Data: `{"result":"Some page text.","call_id":"r1"}`},
		},
	}
}

func fixtureOptions() Options {
	return Options{
		Format:      FormatMarkdown,
		Scope:       ScopeFull,
		Sources:     true,
		Attachments: true,
		Meta:        true,
	}
}

func TestBuild_PairsTurnsInOrderAndCarriesTurnID(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)

	if len(doc.Turns) != 3 {
		t.Fatalf("got %d turns, want 3", len(doc.Turns))
	}
	for i, want := range []string{"turn-1", "turn-2", "turn-3"} {
		if doc.Turns[i].TurnID != want {
			t.Errorf("turns[%d].TurnID = %q, want %q", i, doc.Turns[i].TurnID, want)
		}
	}
	if doc.Turns[0].User == nil || doc.Turns[0].Assistant == nil {
		t.Fatalf("turn 1 should have both sides, got user=%v assistant=%v", doc.Turns[0].User, doc.Turns[0].Assistant)
	}
	if doc.Turns[2].Assistant != nil {
		t.Errorf("trailing turn should have a nil assistant, got %+v", doc.Turns[2].Assistant)
	}
}

func TestBuild_TrailingUserMessageIsRecordedAsOmitted(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)

	if len(doc.Omitted) != 1 {
		t.Fatalf("got %d omissions, want 1: %+v", len(doc.Omitted), doc.Omitted)
	}
	o := doc.Omitted[0]
	if o.What != "answer" || o.Reason != "no answer recorded" {
		t.Errorf("omission = %+v, want an unanswered-answer note", o)
	}
	if len(o.Turns) != 1 || o.Turns[0] != "turn-3" {
		t.Errorf("omission.Turns = %v, want [turn-3]", o.Turns)
	}
}

func TestBuild_ConsecutiveUserMessagesEachBecomeUnansweredTurns(t *testing.T) {
	// A streaming/failed turn leaves its user message behind, then the user
	// sends another — each is its own turn, not merged by a shared turn_id.
	in := Input{Thread: Thread{ID: "t"}, Messages: []Message{
		{ID: 1, Role: "user", TurnID: "a", Content: "first"},
		{ID: 2, Role: "user", TurnID: "b", Content: "second"},
		{ID: 3, Role: "assistant", TurnID: "b", Content: "answer to second"},
	}}
	doc := build(in, Options{Scope: ScopeFull}, fixedNow)

	if len(doc.Turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(doc.Turns))
	}
	if doc.Turns[0].TurnID != "a" || doc.Turns[0].Assistant != nil {
		t.Errorf("first turn = %+v, want unanswered turn a", doc.Turns[0])
	}
	if doc.Turns[1].TurnID != "b" || doc.Turns[1].Assistant == nil {
		t.Errorf("second turn = %+v, want answered turn b", doc.Turns[1])
	}
	if len(doc.Omitted) != 1 || doc.Omitted[0].Turns[0] != "a" {
		t.Errorf("omitted = %+v, want only turn a", doc.Omitted)
	}
}

func TestBuild_SourcesToggle(t *testing.T) {
	on := build(fixtureThread(), Options{Sources: true}, fixedNow)
	if len(on.Turns[0].Assistant.Citations) != 1 {
		t.Fatalf("sources on: got %d citations, want 1", len(on.Turns[0].Assistant.Citations))
	}
	if on.Turns[0].Assistant.Citations[0].URL != "https://en.wikipedia.org/wiki/Oolong" {
		t.Errorf("citation URL = %q", on.Turns[0].Assistant.Citations[0].URL)
	}

	off := build(fixtureThread(), Options{Sources: false}, fixedNow)
	if len(off.Turns[0].Assistant.Citations) != 0 {
		t.Errorf("sources off: got %d citations, want 0", len(off.Turns[0].Assistant.Citations))
	}
}

func TestBuild_AttachmentsToggle(t *testing.T) {
	on := build(fixtureThread(), Options{Attachments: true}, fixedNow)
	a := on.Turns[1].User.Attachments
	if len(a) != 1 || a[0].Filename != "notes.txt" || a[0].ContentType != "text/plain" {
		t.Fatalf("attachments on: got %+v, want notes.txt (text/plain)", a)
	}

	off := build(fixtureThread(), Options{Attachments: false}, fixedNow)
	if len(off.Turns[1].User.Attachments) != 0 {
		t.Errorf("attachments off: got %+v, want none", off.Turns[1].User.Attachments)
	}
}

func TestBuild_MetaToggleFallsBackToThreadModel(t *testing.T) {
	on := build(fixtureThread(), Options{Meta: true}, fixedNow)
	if on.Turns[0].Meta == nil || on.Turns[0].Meta.Model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("turn 1 meta = %+v, want the turn's applied model", on.Turns[0].Meta)
	}
	if on.Turns[1].Meta == nil || on.Turns[1].Meta.Model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("turn 2 meta = %+v, want the thread model fallback", on.Turns[1].Meta)
	}
	if on.Turns[2].Meta != nil {
		t.Errorf("unanswered turn should carry no meta, got %+v", on.Turns[2].Meta)
	}

	off := build(fixtureThread(), Options{Meta: false}, fixedNow)
	if off.Turns[0].Meta != nil {
		t.Errorf("meta off: got %+v, want nil", off.Turns[0].Meta)
	}
}

func TestBuild_FlattensUIFencesToOneLine(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)
	content := doc.Turns[0].Assistant.Content
	if strings.Contains(content, "```ui") || strings.Contains(content, `"c":"callout"`) {
		t.Errorf("ui fence should be flattened out of the content, got:\n%s", content)
	}
	if !strings.Contains(content, "Semi-oxidized (as of Oct 2026)") {
		t.Errorf("flattened content missing the callout text, got:\n%s", content)
	}
}

func TestBuild_EmptyThreadHasNonNilSlices(t *testing.T) {
	doc := build(Input{Thread: Thread{ID: "t"}}, Options{}, fixedNow)
	if doc.Turns == nil || doc.Omitted == nil {
		t.Errorf("Turns/Omitted should be non-nil, got turns=%v omitted=%v", doc.Turns, doc.Omitted)
	}
}

func TestBuild_MalformedRichJSONIsSkippedNotFatal(t *testing.T) {
	in := Input{Thread: Thread{ID: "t"}, Messages: []Message{
		{ID: 1, Role: "user", Content: "hi"},
		{ID: 2, Role: "assistant", Content: "hello", Citations: "{not json", Attachments: "also not"},
	}}
	doc := build(in, Options{Sources: true, Attachments: true}, fixedNow)
	if got := doc.Turns[0].Assistant.Citations; got != nil {
		t.Errorf("malformed citations = %+v, want nil", got)
	}
	if got := doc.Turns[0].User.Attachments; got != nil {
		t.Errorf("malformed attachments = %+v, want nil", got)
	}
}

func TestRenderJSON_VersionAndOmittedManifest(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)
	b, err := RenderJSON(doc)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	// The version is a top-level field, so a reader can branch before
	// decoding the rest.
	if !strings.Contains(string(b), `"polaris_export": 1`) {
		t.Errorf("JSON missing top-level polaris_export version:\n%s", b)
	}

	var got struct {
		Version int                          `json:"polaris_export"`
		Turns   []map[string]json.RawMessage `json:"turns"`
		Omitted []Omission                   `json:"omitted"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decoding rendered JSON: %v", err)
	}
	if got.Version != Version {
		t.Errorf("version = %d, want %d", got.Version, Version)
	}
	if len(got.Turns) != 3 {
		t.Fatalf("got %d turns, want 3", len(got.Turns))
	}
	// The trailing unanswered turn must serialize its assistant as null, not
	// omit the key — a consumer needs to distinguish "no answer" from "field
	// not present in this version".
	if raw, ok := got.Turns[2]["assistant"]; !ok || string(raw) != "null" {
		t.Errorf("turn 3 assistant = %s (present=%v), want null", raw, ok)
	}
	if len(got.Omitted) != 1 || got.Omitted[0].What != "answer" {
		t.Errorf("omitted = %+v, want one answer omission", got.Omitted)
	}
}

func TestRender_DispatchesOnFormat(t *testing.T) {
	md, err := Render(build(fixtureThread(), Options{Format: FormatMarkdown}, fixedNow))
	if err != nil {
		t.Fatalf("Render(md): %v", err)
	}
	if !strings.HasPrefix(md, "# Question about tea") {
		t.Errorf("markdown render should start with the title, got:\n%s", md)
	}

	js, err := Render(build(fixtureThread(), Options{Format: FormatJSON}, fixedNow))
	if err != nil {
		t.Fatalf("Render(json): %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(js), "{") {
		t.Errorf("json render should start with an object, got:\n%s", js)
	}
}

func TestEstimateTokens_CountsTheRenderedText(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)
	got := EstimateTokens(doc)
	if got <= 0 {
		t.Fatalf("EstimateTokens = %d, want > 0", got)
	}
	// Every turn's content must be counted: a rough floor is the combined
	// content length / 4.
	var chars int
	for _, tn := range doc.Turns {
		for _, e := range [2]*Entry{tn.User, tn.Assistant} {
			if e != nil {
				chars += len(e.Content)
			}
		}
	}
	if got < chars/4 {
		t.Errorf("EstimateTokens = %d, want at least the content floor %d", got, chars/4)
	}
}

func TestRenderMarkdown_Golden(t *testing.T) {
	doc := build(fixtureThread(), fixtureOptions(), fixedNow)
	checkGolden(t, "full.md", RenderMarkdown(doc))
}

func TestRenderJSON_Golden(t *testing.T) {
	doc := build(fixtureThread(), Options{
		Format:      FormatJSON,
		Scope:       ScopeFull,
		Sources:     true,
		Attachments: true,
		Meta:        true,
	}, fixedNow)
	b, err := RenderJSON(doc)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	checkGolden(t, "full.json", string(b))
}

func TestBuild_TrailFromTranscript(t *testing.T) {
	doc := build(fixtureThread(), Options{Trail: true}, fixedNow)

	trace := doc.Turns[0].Trace
	if len(trace) != 2 {
		t.Fatalf("got %d trace steps, want 2: %+v", len(trace), trace)
	}
	if trace[0].Tool != "web_search" || !strings.Contains(trace[0].Args, "oolong") {
		t.Errorf("step 0 = %+v, want web_search with its args", trace[0])
	}
	if !strings.Contains(trace[0].Result, "Oolong — Wikipedia") {
		t.Errorf("step 0 result = %q, want the transcript's tool result", trace[0].Result)
	}
	if trace[1].Tool != "memory" {
		t.Errorf("step 1 = %+v, want the memory call", trace[1])
	}
}

func TestBuild_TrailFallsBackToEventsWithoutTranscript(t *testing.T) {
	doc := build(fixtureThread(), Options{Trail: true}, fixedNow)

	trace := doc.Turns[1].Trace
	if len(trace) != 1 || trace[0].Tool != "web_read" {
		t.Fatalf("got %+v, want one web_read step from the event log", trace)
	}
	if !strings.Contains(trace[0].Result, "Some page text") {
		t.Errorf("result = %q, want the event result", trace[0].Result)
	}
}

// TestBuild_PrivacyDropsMemoryToolCall is the regression test for the
// privacy toggle: with Private on the memory tool call and its result must
// be gone from the trail and recorded as an omission, and with it off the
// same call must be present. Reverting the filter makes the first half fail.
func TestBuild_PrivacyDropsMemoryToolCall(t *testing.T) {
	in := fixtureThread()
	in.MemoryTools = map[string]bool{"memory": true}

	private := build(in, Options{Trail: true, Private: true}, fixedNow)
	assertToolAbsent(t, private.Turns[0].Trace, "memory")
	assertToolPresent(t, private.Turns[0].Trace, "web_search")
	o := findOmission(private.Omitted, "tool_calls")
	if o == nil || o.Reason != "privacy: memories left out" || len(o.Tools) != 1 || o.Tools[0] != "memory" {
		t.Errorf("omission = %+v, want the dropped memory tool recorded", o)
	}

	// Toggle off: the same call is exported in full.
	open := build(in, Options{Trail: true}, fixedNow)
	assertToolPresent(t, open.Turns[0].Trace, "memory")
}

func TestBuild_PrivacyPersonalDropsPersonalTools(t *testing.T) {
	in := Input{
		Thread: Thread{ID: "t"},
		Messages: []Message{
			{ID: 1, Role: "user", TurnID: "a", Content: "what did we decide?"},
			{ID: 2, Role: "assistant", TurnID: "a", Content: "We decided X.",
				Transcript: `[{"role":"assistant","content":"","tool_calls":[{"id":"x","function":{"name":"search_chats","arguments":"{}"}}]},{"role":"tool","tool_call_id":"x","content":"past thread contents"}]`},
		},
		PersonalTools: map[string]bool{"search_chats": true},
	}

	doc := build(in, Options{Trail: true, PrivatePersonal: true}, fixedNow)
	assertToolAbsent(t, doc.Turns[0].Trace, "search_chats")
	o := findOmission(doc.Omitted, "tool_calls")
	if o == nil || o.Reason != "privacy: personalization left out" {
		t.Errorf("omission = %+v, want the personalization omission", o)
	}

	open := build(in, Options{Trail: true}, fixedNow)
	assertToolPresent(t, open.Turns[0].Trace, "search_chats")
}

func TestBuild_HandoffScopeUsesStoredSummaryAndTail(t *testing.T) {
	in := fixtureThread()
	in.CompactedSummary = "Earlier we established what oolong is."
	in.CompactedThroughID = 2 // the first turn, before the attachment turn

	doc := build(in, Options{Scope: ScopeHandoff}, fixedNow)
	if doc.Summary == nil || doc.Summary.ThroughMessageID != 2 {
		t.Fatalf("summary = %+v, want the stored one through message 2", doc.Summary)
	}
	if len(doc.Turns) != 2 || doc.Turns[0].TurnID != "turn-2" {
		t.Errorf("turns = %+v, want only the post-compaction tail (turn-2, turn-3)", doc.Turns)
	}
	if !doc.Availability.HandoffAvailable {
		t.Errorf("HandoffAvailable = false, want true when a summary is stored")
	}
}

func TestBuild_HandoffWithoutSummaryFallsBackToFullAndRecordsIt(t *testing.T) {
	doc := build(fixtureThread(), Options{Scope: ScopeHandoff}, fixedNow)
	if doc.Summary != nil {
		t.Errorf("summary = %+v, want nil when none is stored", doc.Summary)
	}
	if len(doc.Turns) != 3 {
		t.Errorf("turns = %d, want the full 3 after falling back", len(doc.Turns))
	}
	if doc.Availability.HandoffAvailable {
		t.Errorf("HandoffAvailable = true, want false with no stored summary")
	}
	if o := findOmission(doc.Omitted, "summary"); o == nil {
		t.Errorf("omitted = %+v, want a no-summary note", doc.Omitted)
	}
}

func TestBuild_ThinkingGroupsByAgent(t *testing.T) {
	doc := build(fixtureThread(), Options{Thinking: true}, fixedNow)

	thinking := doc.Turns[0].Thinking
	if len(thinking) != 2 {
		t.Fatalf("got %d bursts, want 2: %+v", len(thinking), thinking)
	}
	if thinking[0].Agent != "" || !strings.Contains(thinking[0].Content, "search for oolong") {
		t.Errorf("burst 0 = %+v, want the orchestrator's", thinking[0])
	}
	if thinking[1].Agent != "researcher-1" {
		t.Errorf("burst 1 agent = %q, want researcher-1", thinking[1].Agent)
	}
	if thinking[0].DurationMs != 1500 || thinking[1].DurationMs != 800 {
		t.Errorf("durations = %d/%d, want 1500/800", thinking[0].DurationMs, thinking[1].DurationMs)
	}
}

func TestBuild_AvailabilityCountsOnlyAnsweredTurns(t *testing.T) {
	doc := build(fixtureThread(), Options{Trail: true, Thinking: true}, fixedNow)

	if doc.Availability.Turns != 3 {
		t.Errorf("Turns = %d, want 3 (including the unanswered one)", doc.Availability.Turns)
	}
	if doc.Availability.TurnsWithTrail != 2 {
		t.Errorf("TurnsWithTrail = %d, want 2", doc.Availability.TurnsWithTrail)
	}
	if doc.Availability.TurnsWithThinking != 1 {
		t.Errorf("TurnsWithThinking = %d, want 1", doc.Availability.TurnsWithThinking)
	}
}

func TestBuild_ThinkingOmissionListsAnsweredTurnsWithoutReasoning(t *testing.T) {
	doc := build(fixtureThread(), Options{Thinking: true}, fixedNow)

	o := findOmission(doc.Omitted, "thinking")
	if o == nil {
		t.Fatalf("omitted = %+v, want a thinking note for the turn with none", doc.Omitted)
	}
	if len(o.Turns) != 1 || o.Turns[0] != "turn-2" {
		t.Errorf("omission turns = %v, want [turn-2] (the unanswered turn-3 is skipped)", o.Turns)
	}
	if o.Reason != "no thinking recorded" {
		t.Errorf("reason = %q", o.Reason)
	}
}

func TestRenderJSON_IncludesTraceThinkingAndAvailability(t *testing.T) {
	doc := build(fixtureThread(), Options{Trail: true, Thinking: true}, fixedNow)
	b, err := RenderJSON(doc)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	for _, want := range []string{`"trace"`, `"web_search"`, `"thinking"`, `"researcher-1"`, `"availability"`, `"turns_with_thinking": 1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s\n%s", want, b)
		}
	}
}

func TestRenderMarkdown_TrailAndThinkingGolden(t *testing.T) {
	doc := build(fixtureThread(), Options{
		Format: FormatMarkdown, Sources: true, Trail: true, Thinking: true,
	}, fixedNow)
	checkGolden(t, "full-trail.md", RenderMarkdown(doc))
}

func assertToolPresent(t *testing.T, steps []TraceStep, tool string) {
	t.Helper()
	for _, s := range steps {
		if s.Tool == tool {
			return
		}
	}
	t.Errorf("expected tool %q in trail %+v", tool, steps)
}

func assertToolAbsent(t *testing.T, steps []TraceStep, tool string) {
	t.Helper()
	for _, s := range steps {
		if s.Tool == tool {
			t.Errorf("tool %q should have been dropped, got %+v", tool, steps)
			return
		}
	}
}

func findOmission(omissions []Omission, what string) *Omission {
	for i := range omissions {
		if omissions[i].What == what {
			return &omissions[i]
		}
	}
	return nil
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (run `go test ./threadexport -update` to create it): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
