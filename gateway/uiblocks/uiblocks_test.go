package uiblocks

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fixture struct {
	Cases []struct {
		Name  string `json:"name"`
		Input string `json:"input"`
		Want  string `json:"want"`
	} `json:"cases"`
}

// TestFlatten_SharedFixture runs the same cases the TypeScript flattener runs
// (web/src/lib/uiBlocks/flatten.test.ts). They are the contract between the
// two implementations.
func TestFlatten_SharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ui_flatten.json")
	if err != nil {
		t.Fatalf("reading shared fixture: %v", err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parsing shared fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Flatten(c.Input); got != c.Want {
				t.Errorf("Flatten mismatch\n--- input ---\n%q\n--- got ---\n%q\n--- want ---\n%q", c.Input, got, c.Want)
			}
		})
	}
}

// Flatten runs on every stored message, so it must never panic and must be
// stable whatever the model wrote — including every prefix of a real answer
// (a turn can be cut anywhere).
func TestFlatten_NeverPanicsOnAnyPrefix(t *testing.T) {
	src := "intro\n\n```ui\n{\"c\":\"compare\",\"cols\":[\"A\",\"B\"],\"pick\":0}\n{\"row\":\"x\",\"v\":[\"1\",\"2\"]}\n" +
		"{\"c\":\"steps\"}\n{\"i\":\"s\"}\n```\n\nend\n```mermaid\ngraph TD\n```\n"
	for n := 0; n <= len(src); n++ {
		_ = Flatten(src[:n])
	}
}

func TestFlatten_Idempotent(t *testing.T) {
	in := "a\n```ui\n{\"c\":\"stat\",\"value\":\"1\"}\n```\nb\n"
	once := Flatten(in)
	if twice := Flatten(once); twice != once {
		t.Errorf("flattening already-flat text changed it: %q -> %q", once, twice)
	}
}

func TestStrip(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no fence is unchanged", "Plain [a](https://a.org).\n", "Plain [a](https://a.org).\n"},
		{
			"removes the fence, keeps prose and prose links",
			"[x](https://a.org)\n\n```ui\n{\"c\":\"callout\",\"text\":\"[y](https://a.org)\"}\n```\n\n[z](https://a.org)\n",
			"[x](https://a.org)\n\n\n[z](https://a.org)\n",
		},
		{"cut-off fence is removed too", "a\n```ui\n{\"c\":\"stat\",\"value\":\"1\"}\n", "a\n"},
		{"other fences untouched", "```go\nui := 1\n```\n", "```go\nui := 1\n```\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Strip(c.in); got != c.want {
				t.Errorf("Strip(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A tab's body and a disclosed paragraph have their own, larger clips (a real
// model puts a fenced command block in each tab); parse.test.ts pins the same
// numbers, so the two sides cut at the same character.
func TestFlatten_ProseBodyFieldsKeepTheirLargerCaps(t *testing.T) {
	mid := strings.Repeat("x", maxTextChars+50)
	if got, want := Flatten("```ui\n{\"c\":\"tabs\"}\n{\"tab\":\"A\",\"text\":\""+mid+"\"}\n```\n"), "A: "+mid+"\n"; got != want {
		t.Errorf("tab text should pass %d chars untouched, got %d bytes", maxTextChars, len(got))
	}
	if got, want := Flatten("```ui\n{\"c\":\"disclose\"}\n{\"p\":\""+mid+"\"}\n```\n"), mid+"\n"; got != want {
		t.Errorf("disclose paragraph should pass %d chars untouched, got %d bytes", maxTextChars, len(got))
	}
	over := strings.Repeat("x", maxTabTextChars+10)
	got := Flatten("```ui\n{\"c\":\"tabs\"}\n{\"tab\":\"A\",\"text\":\"" + over + "\"}\n```\n")
	if want := "A: " + strings.Repeat("x", maxTabTextChars-1) + "…\n"; got != want {
		t.Errorf("tab text not clipped to %d chars", maxTabTextChars)
	}
}

func TestFlatten_ClipsLongTextLikeTheParser(t *testing.T) {
	long := strings.Repeat("x", maxTextChars+50)
	got := Flatten("```ui\n{\"c\":\"callout\",\"text\":\"" + long + "\"}\n```\n")
	if want := strings.Repeat("x", maxTextChars-1) + "…\n"; got != want {
		t.Errorf("long text not clipped to %d chars: got %d bytes", maxTextChars, len(got))
	}
}
