package uiblocks

import (
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
	got := locators(Sites(content, all, 8))
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
	got := locators(Sites(content, tracked, 8))
	want := []string{"0.0.0.src#0 https://t.example/1", "0.0.0.src#1 https://t.example/2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSites_ClaimTextIsTheItemSentence(t *testing.T) {
	content := "```ui\n{\"c\":\"facts\"}\n{\"k\":\"Pop\",\"v\":\"545,000 [census](https://a.example/c)\"}\n```\n"
	s := Sites(content, all, 8)
	if len(s) != 1 || s[0].Text != "Pop: 545,000 census" {
		t.Errorf("claim text should be the item with link markup reduced, got %+v", s)
	}
}

func TestSites_CompareRowsAreRowMajor(t *testing.T) {
	content := "```ui\n{\"c\":\"compare\",\"cols\":[\"A\",\"B\"]}\n" +
		"{\"row\":\"Price\",\"v\":[\"$1\",\"$2 [x](https://x.example/p)\"],\"src\":[\"https://y.example/p\"]}\n```\n"
	got := locators(Sites(content, all, 8))
	want := []string{"0.0.0.v1#0 https://x.example/p", "0.0.0.src#0 https://y.example/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSites_QuoteSourcesAreCheckedAsQuotes(t *testing.T) {
	content := "```ui\n{\"c\":\"quote\",\"text\":\"The only way out is through.\",\"by\":\"Frost\",\"src\":[\"https://q.example/f\"]}\n```\n"
	s := Sites(content, all, 8)
	if len(s) != 1 || !s[0].Quote || s[0].Text != "The only way out is through." {
		t.Errorf("quote source should be a Quote site carrying the passage, got %+v", s)
	}
}

func TestSites_ClaimEvidenceLinesAreTheirOwnClaims(t *testing.T) {
	content := "```ui\n{\"c\":\"claim\",\"text\":\"Knuckle cracking causes arthritis\",\"verdict\":\"false\"}\n" +
		"{\"-\":\"No link found\",\"src\":[\"https://s.example/1\"]}\n" +
		"{\"+\":\"Some swelling reported\",\"src\":[\"https://s.example/2\"]}\n```\n"
	s := Sites(content, all, 8)
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

func TestSites_FenceOrdinalAndCap(t *testing.T) {
	one := "```ui\n{\"c\":\"callout\",\"text\":\"x [l](https://a.example/1)\"}\n```\n"
	content := strings.Repeat(one, 3) + "```mermaid\ngraph TD\n```\n" + one
	got := locators(Sites(content, all, 3))
	want := []string{"0.0.0.text#0 https://a.example/1", "1.0.0.text#0 https://a.example/1", "2.0.0.text#0 https://a.example/1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fences past the cap (and non-ui fences) must not count\n got %v\nwant %v", got, want)
	}
}

func TestSites_NoUIFenceNoSites(t *testing.T) {
	if s := Sites("Just [prose](https://a.example/1).", all, 8); len(s) != 0 {
		t.Errorf("prose links are not block sites, got %+v", s)
	}
}
