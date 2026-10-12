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
