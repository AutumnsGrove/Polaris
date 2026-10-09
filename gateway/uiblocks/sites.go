package uiblocks

import (
	"fmt"
	"regexp"
	"strings"
)

// Site is one citation link inside a ui block that verification may check:
// where it is (Locator), which source it cites, and the sentence the source
// should back (Text).
//
// Locators, not occurrence numbers. Prose links are matched to their chip by
// "the nth time this URL is cited in the answer"; for blocks that rule is a
// trap (a compare block renders each cell twice, flattens column by column but
// draws row by row), and a drift in the order would put a "found in source"
// tick on the wrong chip. A locator names the link by where it sits instead:
//
//	<fence>.<block>.<item>.<field>#<n>
//
// fence is the ordinal among the answer's ui fences, block the index in that
// fence's parsed output, item the row/step/event (0 for a block's own
// fields), field a name from fieldsOf below, and n the position among the
// field's tracked links. If the client and server ever disagree about an
// address the tick is simply missing; it can never land on another chip.
// The client side builds the same strings in components/ui/ (its `loc` prop).
type Site struct {
	Locator string
	URL     string
	// Text is the claim for Jev: the item's own sentence with link markup
	// reduced to the link text.
	Text string
	// Quote marks a `quote` block's source: a stronger promise than "supports
	// this claim", so it is checked as "contains this passage".
	Quote bool
}

// linkRe matches a markdown link, the same shape as gateway/verification.go's
// citationLinkRe (url stops at the first ")" or whitespace; optional title).
var linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// stripLinks reduces [text](url) to text, so a claim reads as a sentence.
func stripLinks(s string) string { return linkRe.ReplaceAllString(s, "$1") }

// Sites returns every verifiable link in content's ui fences, in document
// order. tracked says which URLs are real citations of this turn: only those
// are numbered (the client chips only tracked links, so counting an untracked
// one would shift every later n). Only the first maxFences fences count,
// matching the client (renderAnswer.ts renders later ones as code).
func Sites(content string, tracked func(url string) bool, maxFences int) []Site {
	var sites []Site
	fence := 0
	rewrite(content, func(body string) string {
		f := fence
		fence++
		if f >= maxFences {
			return ""
		}
		for bi, b := range parse(body) {
			collect(b, fmt.Sprintf("%d.%d", f, bi), tracked, &sites)
		}
		return ""
	})
	return sites
}

type collector struct {
	loc     string
	tracked func(string) bool
	out     *[]Site
}

// text emits a Site per tracked markdown link in s, numbered within the field.
func (c collector) text(item int, field, s, claim string, quote bool) {
	n := 0
	for _, m := range linkRe.FindAllStringSubmatch(s, -1) {
		if !c.tracked(m[2]) {
			continue
		}
		*c.out = append(*c.out, Site{Locator: fmt.Sprintf("%s.%d.%s#%d", c.loc, item, field, n), URL: m[2], Text: claim, Quote: quote})
		n++
	}
}

// src emits a Site per tracked URL in a "src" array, numbered within the field.
func (c collector) src(item int, field string, urls []string, claim string, quote bool) {
	n := 0
	for _, u := range urls {
		if !c.tracked(u) {
			continue
		}
		*c.out = append(*c.out, Site{Locator: fmt.Sprintf("%s.%d.%s#%d", c.loc, item, field, n), URL: u, Text: claim, Quote: quote})
		n++
	}
}

func join(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// collect walks one block. Field names here are the contract with the Svelte
// components' `loc` props: change one and the other together.
func collect(b *block, loc string, tracked func(string) bool, out *[]Site) {
	c := collector{loc: loc, tracked: tracked, out: out}
	switch b.kind {
	case "callout":
		claim := stripLinks(b.text)
		c.text(0, "text", b.text, claim, false)
		c.src(0, "src", b.src, claim, false)
	case "stat":
		label := ""
		if b.label != "" {
			label = b.label + ":"
		}
		claim := stripLinks(join(label, b.value, bracket(b.note)))
		c.text(0, "value", b.value, claim, false)
		c.text(0, "note", b.note, claim, false)
		c.src(0, "src", b.src, claim, false)
	case "compare":
		for ri, r := range b.rows {
			var cells []string
			for ci, col := range b.cols {
				cells = append(cells, col+": "+stripLinks(r.v[ci]))
			}
			claim := r.row + " — " + strings.Join(cells, "; ")
			for ci := range b.cols {
				c.text(ri, fmt.Sprintf("v%d", ci), r.v[ci], claim, false)
			}
			c.src(ri, "src", r.src, claim, false)
		}
	case "steps":
		for i, s := range b.steps {
			claim := stripLinks(join(s.i, dash(s.d)))
			c.text(i, "i", s.i, claim, false)
			c.text(i, "d", s.d, claim, false)
		}
	case "timeline":
		for i, e := range b.events {
			claim := stripLinks(e.when + ": " + e.i)
			c.text(i, "i", e.i, claim, false)
			c.src(i, "src", e.src, claim, false)
		}
	case "checklist":
		for i, it := range b.items {
			c.text(i, "i", it, stripLinks(it), false)
		}
	case "procon":
		for i, p := range b.pros {
			c.text(i, "pro", p, stripLinks(p), false)
		}
		for i, p := range b.cons {
			c.text(i, "con", p, stripLinks(p), false)
		}
	case "choose":
		for i, r := range b.rules {
			claim := stripLinks("If " + r.cond + ": " + r.then)
			c.text(i, "if", r.cond, claim, false)
			c.text(i, "then", r.then, claim, false)
			c.src(i, "src", r.src, claim, false)
		}
	case "facts":
		for i, r := range b.factRows {
			claim := stripLinks(r.k + ": " + r.v)
			c.text(i, "v", r.v, claim, false)
			c.src(i, "src", r.src, claim, false)
		}
	case "flow":
		for i, n := range b.nodes {
			claim := stripLinks(join(n.t, dash(n.d)))
			c.text(i, "t", n.t, claim, false)
			c.text(i, "d", n.d, claim, false)
			c.src(i, "src", n.src, claim, false)
		}
	case "tabs":
		for i, t := range b.tabs {
			c.text(i, "text", t.text, stripLinks(t.name+": "+t.text), false)
		}
	case "disclose":
		for i, p := range b.paras {
			c.text(i, "p", p, stripLinks(p), false)
		}
	case "quote":
		// A quote is a stronger promise than a claim: its sources are checked
		// for the passage itself. Links in its attribution are ordinary prose.
		c.src(0, "src", b.src, b.text, true)
	case "claim":
		claim := stripLinks(b.text)
		c.text(0, "text", b.text, claim, false)
		// Evidence lines are claims in their own right, so each line's sources
		// are checked against that line, not against the headline claim.
		for i, e := range b.supports {
			c.text(i, "plus", e.text, stripLinks(e.text), false)
			c.src(i, "plus.src", e.src, stripLinks(e.text), false)
		}
		for i, e := range b.disputes {
			c.text(i, "minus", e.text, stripLinks(e.text), false)
			c.src(i, "minus.src", e.src, stripLinks(e.text), false)
		}
	}
}

func bracket(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

func dash(s string) string {
	if s == "" {
		return ""
	}
	return "— " + s
}
