package threadexport

import "testing"

// Stored columns can be empty, truncated or from an older schema; an export
// must never fail or panic because one old row is unreadable.
func FuzzParseStoredColumns(f *testing.F) {
	f.Add(`[{"title":"t","url":"https://a.org"}]`)
	f.Add(`[{"filename":"a.png","content_type":"image/png"}]`)
	f.Add(`[]`)
	f.Add(`{"not":"a list"}`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, raw string) {
		_ = parseCitations(raw)
		_ = parseAttachments(raw)
	})
}

func FuzzBuildAndRenderMarkdown(f *testing.F) {
	f.Add("user q", "assistant a", `[{"title":"t","url":"https://a.org"}]`, `[{"filename":"a"}]`, "")
	f.Add("```", "# heading\n---", `not json`, ``, `[{"role":"user"`)
	f.Fuzz(func(t *testing.T, user, answer, cites, atts, transcript string) {
		in := Input{
			Messages: []Message{
				{ID: 1, Role: "user", Content: user, TurnID: "t1"},
				{ID: 2, Role: "assistant", Content: answer, Citations: cites, Attachments: atts, Transcript: transcript, TurnID: "t1"},
			},
		}
		doc := Build(in, Options{Format: "markdown", Scope: "full", Sources: true, Attachments: true, Meta: true, Trail: true, Thinking: true})
		_ = RenderMarkdown(doc)
		if _, err := RenderJSON(doc); err != nil {
			t.Errorf("RenderJSON: %v", err)
		}
	})
}
