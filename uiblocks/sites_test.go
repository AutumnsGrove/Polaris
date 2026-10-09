package uiblocks

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func all(string) bool { return true }

func locators(sites []Site) []string {
	var out []string
	for _, s := range sites {
		out = append(out, s.Locator+" "+s.URL)
	}
	return out
}

func TestSites_AddressesLinksByPosition(t *testing.T) {
	content := "Prose with [a](https://p.example/x) is not a block link.\n\n" +
		"```ui\n" +
		"{\"c\":\"callout\",\"text\":\"Hello\"}\n" + // block 0, no links
		"{\"c\":\"facts\",\"title\":\"T\"}\n" + // block 1
		"{\"k\":\"Pop\",\"v\":\"1 [census](https://a.example/c)\",\"src\":[\"https://b.example/s\",\"https://c.example/s\"]}\n" +
		"{\"k\":\"Age\",\"v\":\"old\",\"src\":[\"https://d.example/s\"]}\n" +
		"```\n"
	got := locators(Sites(content, all))
	want := []string{
		"0.1.0.v#0 https://a.example/c",
		"0.1.0.src#0 https://b.example/s",
		"0.1.0.src#1 https://c.example/s",
		"0.1.1.src#0 https://d.example/s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("locators\n got %v\nwant %v", got, want)
	}
}

func TestSites_OnlyTrackedLinksAreNumbered(t *testing.T) {
	// An untracked URL must not consume an n: the client never chips it, so
	// counting it would shift every later link's address.
	tracked := func(u string) bool { return u != "https://untracked.example/u" }
	content := "```ui\n{\"c\":\"facts\"}\n" +
		"{\"k\":\"A\",\"v\":\"x\",\"src\":[\"https://untracked.example/u\",\"https://t.example/1\",\"https://t.example/2\"]}\n```\n"
	got := locators(Sites(content, tracked))
	want := []string{"0.0.0.src#0 https://t.example/1", "0.0.0.src#1 https://t.example/2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSites_ClaimTextIsTheItemSentence(t *testing.T) {
	content := "```ui\n{\"c\":\"facts\"}\n{\"k\":\"Pop\",\"v\":\"545,000 [census](https://a.example/c)\"}\n```\n"
	s := Sites(content, all)
	if len(s) != 1 || s[0].Text != "Pop: 545,000 census" {
		t.Errorf("claim text should be the item with link markup reduced, got %+v", s)
	}
}

func TestSites_CompareRowsAreRowMajor(t *testing.T) {
	content := "```ui\n{\"c\":\"compare\",\"cols\":[\"A\",\"B\"]}\n" +
		"{\"row\":\"Price\",\"v\":[\"$1\",\"$2 [x](https://x.example/p)\"],\"src\":[\"https://y.example/p\"]}\n```\n"
	got := locators(Sites(content, all))
	want := []string{"0.0.0.v1#0 https://x.example/p", "0.0.0.src#0 https://y.example/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSites_QuoteSourcesAreCheckedAsQuotes(t *testing.T) {
	content := "```ui\n{\"c\":\"quote\",\"text\":\"The only way out is through.\",\"by\":\"Frost\",\"src\":[\"https://q.example/f\"]}\n```\n"
	s := Sites(content, all)
	if len(s) != 1 || !s[0].Quote || s[0].Text != "The only way out is through." {
		t.Errorf("quote source should be a Quote site carrying the passage, got %+v", s)
	}
}

func TestSites_ClaimEvidenceLinesAreTheirOwnClaims(t *testing.T) {
	content := "```ui\n{\"c\":\"claim\",\"text\":\"Knuckle cracking causes arthritis\",\"verdict\":\"false\"}\n" +
		"{\"-\":\"No link found\",\"src\":[\"https://s.example/1\"]}\n" +
		"{\"+\":\"Some swelling reported\",\"src\":[\"https://s.example/2\"]}\n```\n"
	s := Sites(content, all)
	got := locators(s)
	// Supports are walked before disputes. Order has no meaning for locators.
	want := []string{"0.0.0.plus.src#0 https://s.example/2", "0.0.0.minus.src#0 https://s.example/1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if s[0].Text != "Some swelling reported" || s[1].Text != "No link found" {
		t.Errorf("each evidence source should be checked against its own line, got %+v", s)
	}
}

func TestSites_FenceOrdinal(t *testing.T) {
	one := "```ui\n{\"c\":\"callout\",\"text\":\"x [l](https://a.example/1)\"}\n```\n"
	content := strings.Repeat(one, 3) + "```mermaid\ngraph TD\n```\n" + one
	got := locators(Sites(content, all))
	want := []string{
		"0.0.0.text#0 https://a.example/1",
		"1.0.0.text#0 https://a.example/1",
		"2.0.0.text#0 https://a.example/1",
		"3.0.0.text#0 https://a.example/1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("every ui fence must count, and non-ui fences must not\n got %v\nwant %v", got, want)
	}
}

// TestSites_FieldNamesMatchTheComponents is the contract test between the two
// halves of the locator scheme: for every block kind it feeds a link into every
// field and checks Sites emits exactly the expected field names, then checks the
// matching Svelte component builds the "<item>.<field>" suffix with the same
// item variable the server numbers by (so `{loc}.{i}.plus`, not a hardcoded
// `.0.plus`). A typo on either side would otherwise silently lose that field's
// ticks.
//
// What it does NOT prove: it is a substring check on the component source, not a
// structural one. The server's item indices are pinned by the locator tests
// above (row-major compare, per-line claim evidence, flow arrival order); the
// client's item variable is only checked to exist in the right place. Both are
// deliberate: reading Svelte for a stronger check would need each component to
// export its field table.
func TestSites_FieldNamesMatchTheComponents(t *testing.T) {
	const l = "[x](https://t.example/x)"
	const u = `"https://t.example/u"`
	cases := []struct {
		kind, component, body string
		fields                []string // as they appear in a locator: <field>
		locAttrs              []string // the exact loc="..." attribute each field needs in the component
	}{
		{"callout", "UiCallout", `{"c":"callout","text":"a ` + l + `","src":[` + u + `]}`, []string{"text", "src"}, []string{`loc="{loc}.0.text"`, `loc="{loc}.0.src"`}},
		{"stat", "UiStat", `{"c":"stat","value":"1","note":"n ` + l + `","src":[` + u + `]}`, []string{"note", "src"}, []string{`loc="{loc}.0.note"`, `loc="{loc}.0.src"`}},
		{"compare", "UiCompare", `{"c":"compare","cols":["A","B"]}` + "\n" + `{"row":"r","v":["a ` + l + `","b"],"src":[` + u + `]}`, []string{"v0", "src"}, []string{`loc="{loc}.{ri}.v{ci}"`, `loc="{loc}.{ri}.src"`}},
		{"steps", "UiSteps", `{"c":"steps"}` + "\n" + `{"i":"a ` + l + `","d":"b ` + l + `"}`, []string{"i", "d"}, []string{`loc="{loc}.{i}.i"`, `loc="{loc}.{i}.d"`}},
		{"timeline", "UiTimeline", `{"c":"timeline"}` + "\n" + `{"when":"w","i":"a ` + l + `","src":[` + u + `]}`, []string{"i", "src"}, []string{`loc="{loc}.{i}.i"`, `loc="{loc}.{i}.src"`}},
		{"checklist", "UiChecklist", `{"c":"checklist"}` + "\n" + `{"i":"a ` + l + `"}`, []string{"i"}, []string{`loc="{loc}.{i}.i"`}},
		{"procon", "UiProCon", `{"c":"procon"}` + "\n" + `{"+":"a ` + l + `"}` + "\n" + `{"-":"b ` + l + `"}`, []string{"pro", "con"}, []string{`loc="{loc}.{i}.pro"`, `loc="{loc}.{i}.con"`}},
		{"choose", "UiChoose", `{"c":"choose"}` + "\n" + `{"if":"a ` + l + `","then":"b ` + l + `","src":[` + u + `]}`, []string{"if", "then", "src"}, []string{`loc="{loc}.{i}.if"`, `loc="{loc}.{i}.then"`, `loc="{loc}.{i}.src"`}},
		{"facts", "UiFacts", `{"c":"facts"}` + "\n" + `{"k":"k","v":"a ` + l + `","src":[` + u + `]}`, []string{"v", "src"}, []string{`loc="{loc}.{i}.v"`, `loc="{loc}.{i}.src"`}},
		// A flow node renders in the layer or the waiting list; both build the
		// same `at(...)` address, so either occurrence satisfies the needle.
		{"flow", "UiFlow", `{"c":"flow"}` + "\n" + `{"n":"a","t":"a ` + l + `","d":"b ` + l + `","src":[` + u + `]}`, []string{"t", "d", "src"}, []string{`loc="{at(cell.node.n)}.t"`, `loc="{at(cell.node.n)}.d"`, `loc="{at(cell.node.n)}.src"`}},
		{"tabs", "UiTabs", `{"c":"tabs"}` + "\n" + `{"tab":"t","text":"a ` + l + `"}`, []string{"text"}, []string{`loc="{loc}.{active}.text"`}},
		{"disclose", "UiDisclose", `{"c":"disclose"}` + "\n" + `{"p":"a ` + l + `"}`, []string{"p"}, []string{`loc="{loc}.{i}.p"`}},
		{"quote", "UiQuote", `{"c":"quote","text":"q ` + l + `","by":"b ` + l + `","src":[` + u + `]}`, []string{"src"}, []string{`loc="{loc}.0.src"`}},
		{"claim", "UiClaim", `{"c":"claim","text":"a ` + l + `"}` + "\n" + `{"+":"p ` + l + `","src":[` + u + `]}` + "\n" + `{"-":"m ` + l + `","src":[` + u + `]}`,
			[]string{"text", "plus", "plus.src", "minus", "minus.src"}, []string{`loc="{loc}.0.text"`, `loc="{loc}.{i}.plus"`, `loc="{loc}.{i}.plus.src"`, `loc="{loc}.{i}.minus"`, `loc="{loc}.{i}.minus.src"`}},
	}
	for _, tc := range cases {
		sites := Sites("```ui\n"+tc.body+"\n```\n", all)
		got := map[string]bool{}
		for _, s := range sites {
			// "<fence>.<block>.<item>.<field>#<n>" -> <field>
			rest := strings.SplitN(s.Locator, ".", 4)[3]
			got[strings.SplitN(rest, "#", 2)[0]] = true
		}
		for _, f := range tc.fields {
			if !got[f] {
				t.Errorf("%s: Sites emitted no %q site (got %v)", tc.kind, f, got)
			}
		}
		if len(got) != len(tc.fields) {
			t.Errorf("%s: Sites emitted fields %v, the contract lists %v", tc.kind, got, tc.fields)
		}

		src, err := os.ReadFile("../web/src/lib/components/ui/" + tc.component + ".svelte")
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		for _, want := range tc.locAttrs {
			if !strings.Contains(string(src), want) {
				t.Errorf("%s: %s.svelte has no %s, so its ticks would silently never show", tc.kind, tc.component, want)
			}
		}
	}
}

func TestSites_NoUIFenceNoSites(t *testing.T) {
	if s := Sites("Just [prose](https://a.example/1).", all); len(s) != 0 {
		t.Errorf("prose links are not block sites, got %+v", s)
	}
}
