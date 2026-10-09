// Package uiblocks is the server-side twin of web/src/lib/uiBlocks (split.ts,
// parse.ts, flatten.ts): it understands the ```ui fence an answer may carry
// (docs/plans/intelligent-ui.md) well enough to turn it into readable text.
//
// Every consumer of raw message text that is NOT the chat UI needs this —
// claim extraction, search_chats indexing, Weaver, read-aloud — because the
// stored answer is the model's verbatim output, JSON lines and all.
//
// The two implementations must agree exactly. testdata/ui_flatten.json is
// read by both this package's tests and the TS ones, so a drift in either
// fails a test rather than showing up as a subtly different transcript.
package uiblocks

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Caps mirror parse.ts exactly (docs/plans/intelligent-ui.md "Grammar").
const (
	maxLinesPerFence = 40
	maxTextChars     = 400
	maxCompareCols   = 6
	maxCompareRows   = 12
	maxSteps         = 15
	maxRawChars      = 200

	maxTimelineEvents = 15
	maxChecklistItems = 20
	maxProConPerSide  = 8
	maxChooseRules    = 8
	maxFactRows       = 12
	maxFlowNodes      = 10
	maxFlowEdges      = 20
	maxTabs           = 6
	maxDiscloseParas  = 8
	maxClaimEvidence  = 6

	// Mirror parse.ts's MAX_TAB_TEXT_CHARS / MAX_DISCLOSE_PARA_CHARS.
	maxTabTextChars      = 2000
	maxDiscloseParaChars = 1200
)

type block struct {
	kind string // callout | stat | compare | steps | timeline | checklist | procon | choose | facts | raw

	// callout / stat
	tone, text, asof, label, value, note string
	// compare
	cols []string
	pick int // -1 when unset
	rows []compareRow
	// steps
	title string
	steps []step
	// callout / stat sources
	src []string
	// timeline
	events []event
	// checklist
	items []string
	// procon
	proHead, conHead string
	pros, cons       []string
	// choose
	rules []rule
	// facts (title is shared with steps)
	sub      string
	factRows []factRow
	// flow: nodes and edges in arrival order (an edge may name a node that
	// arrives later, so ends are only resolved at flatten time)
	nodes []flowNode
	edges []flowEdge
	// tabs
	tabs []tab
	// disclose (title is shared with steps)
	hint  string
	paras []string
	// quote (text/src are shared with callout)
	by string
	// claim (text is shared): the model's own verdict, never a verified result
	verdict            string
	supports, disputes []evidence
}

type evidence struct {
	text string
	src  []string
}

type flowNode struct {
	n, t, d string
	src     []string
}

type flowEdge struct{ from, to, l string }

type tab struct{ name, text string }

type event struct {
	when, i string
	src     []string
}

type rule struct {
	cond, then string
	src        []string
}

type factRow struct {
	k, v string
	src  []string
}

type compareRow struct {
	row string
	v   []string
	src []string
}

type step struct{ i, d, t string }

// utf16Len/clip count in UTF-16 code units, matching JavaScript's String
// length, so a 400-"character" cap cuts at the same place on both sides.
func clip(s string, max int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= max {
		return s
	}
	return string(utf16.Decode(u[:max-1])) + "…"
}

// text mirrors parse.ts's text(): strings and finite numbers, trimmed,
// non-empty, clipped. ok is false for anything else.
func text(v any) (string, bool) { return textMax(v, maxTextChars) }

// textMax is text with a per-field clip, for the prose-body fields that need
// more than 400 characters (a tab's fenced command block, a disclosed paragraph).
func textMax(v any, max int) (string, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", false
		}
		v = strconv.FormatFloat(x, 'f', -1, 64)
	}
	s, isStr := v.(string)
	if !isStr {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return clip(s, max), true
}

func optText(v any) string { s, _ := text(v); return s }

func sources(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func rawBlock(line string) block {
	return block{kind: "raw", text: clip(strings.TrimSpace(line), maxRawChars)}
}

var asofRe = regexp.MustCompile(`^\d{4}-\d{2}$`)

func openContainer(o map[string]any) (*block, bool) {
	c, _ := o["c"].(string)
	switch c {
	case "callout":
		body, ok := text(o["text"])
		if !ok {
			return nil, false
		}
		tone := "note"
		if t, _ := o["tone"].(string); t == "warn" || t == "ok" || t == "answer" || t == "note" {
			tone = t
		}
		b := &block{kind: "callout", tone: tone, text: body, src: sources(o["src"])}
		if a, _ := o["asof"].(string); tone == "answer" && asofRe.MatchString(a) {
			b.asof = a
		}
		return b, true
	case "stat":
		value, ok := text(o["value"])
		if !ok {
			return nil, false
		}
		return &block{kind: "stat", label: optText(o["label"]), value: value, note: optText(o["note"]), src: sources(o["src"])}, true
	case "compare":
		raw, ok := o["cols"].([]any)
		if !ok {
			return nil, false
		}
		var cols []string
		for _, e := range raw {
			if s, ok := text(e); ok {
				cols = append(cols, s)
			}
		}
		// One column is not a comparison; the renderer handles the rest, and no
		// column is silently dropped for being invalid.
		if len(cols) < 2 || len(cols) > maxCompareCols || len(cols) != len(raw) {
			return nil, false
		}
		pick := -1
		if p, ok := o["pick"].(float64); ok && p == math.Trunc(p) && p >= 0 && int(p) < len(cols) {
			pick = int(p)
		}
		return &block{kind: "compare", cols: cols, pick: pick}, true
	case "steps":
		return &block{kind: "steps", title: optText(o["title"])}, true
	// These open with no required field: their data is all in child lines.
	case "timeline":
		return &block{kind: "timeline"}, true
	case "checklist":
		return &block{kind: "checklist", title: optText(o["title"])}, true
	case "procon":
		return &block{kind: "procon", proHead: optText(o["pro_h"]), conHead: optText(o["con_h"])}, true
	case "choose":
		return &block{kind: "choose", title: optText(o["title"])}, true
	case "facts":
		return &block{kind: "facts", title: optText(o["title"]), sub: optText(o["sub"])}, true
	case "flow":
		return &block{kind: "flow"}, true
	case "tabs":
		return &block{kind: "tabs"}, true
	case "disclose":
		return &block{kind: "disclose", title: optText(o["title"]), hint: optText(o["hint"])}, true
	case "quote":
		body, ok := text(o["text"])
		if !ok {
			return nil, false
		}
		return &block{kind: "quote", text: body, by: optText(o["by"]), src: sources(o["src"])}, true
	case "claim":
		body, ok := text(o["text"])
		if !ok {
			return nil, false
		}
		// An unknown verdict is "unverified", the neutral reading.
		verdict := "unverified"
		if v, _ := o["verdict"].(string); v == "true" || v == "mixed" || v == "misleading" || v == "false" {
			verdict = v
		}
		return &block{kind: "claim", text: body, verdict: verdict}, true
	}
	return nil, false
}

func addChild(b *block, o map[string]any) bool {
	switch b.kind {
	case "compare":
		label, ok := text(o["row"])
		cells, isArr := o["v"].([]any)
		if !ok || !isArr || len(b.rows) >= maxCompareRows {
			return false
		}
		v := make([]string, len(b.cols))
		for i := range b.cols {
			v[i] = "—"
			if i < len(cells) {
				if s, ok := text(cells[i]); ok {
					v[i] = s
				}
			}
		}
		b.rows = append(b.rows, compareRow{row: label, v: v, src: sources(o["src"])})
		return true
	case "steps":
		i, ok := text(o["i"])
		if !ok || len(b.steps) >= maxSteps {
			return false
		}
		b.steps = append(b.steps, step{i: i, d: optText(o["d"]), t: optText(o["t"])})
		return true
	case "timeline":
		when, ok1 := text(o["when"])
		i, ok2 := text(o["i"])
		if !ok1 || !ok2 || len(b.events) >= maxTimelineEvents {
			return false
		}
		b.events = append(b.events, event{when: when, i: i, src: sources(o["src"])})
		return true
	case "checklist":
		i, ok := text(o["i"])
		if !ok || len(b.items) >= maxChecklistItems {
			return false
		}
		b.items = append(b.items, i)
		return true
	case "procon":
		// A line carrying both "+" and "-" is ambiguous: it fits neither.
		pro, okPro := text(o["+"])
		con, okCon := text(o["-"])
		if okPro && !okCon && len(b.pros) < maxProConPerSide {
			b.pros = append(b.pros, pro)
			return true
		}
		if okCon && !okPro && len(b.cons) < maxProConPerSide {
			b.cons = append(b.cons, con)
			return true
		}
		return false
	case "choose":
		cond, ok1 := text(o["if"])
		then, ok2 := text(o["then"])
		if !ok1 || !ok2 || len(b.rules) >= maxChooseRules {
			return false
		}
		b.rules = append(b.rules, rule{cond: cond, then: then, src: sources(o["src"])})
		return true
	case "facts":
		k, ok1 := text(o["k"])
		v, ok2 := text(o["v"])
		if !ok1 || !ok2 || len(b.factRows) >= maxFactRows {
			return false
		}
		b.factRows = append(b.factRows, factRow{k: k, v: v, src: sources(o["src"])})
		return true
	case "flow":
		if e, isEdge := o["e"].([]any); isEdge {
			// Ids are not checked against the nodes here: the target may arrive later.
			if len(e) != 2 || len(b.edges) >= maxFlowEdges {
				return false
			}
			from, ok1 := text(e[0])
			to, ok2 := text(e[1])
			if !ok1 || !ok2 || from == to {
				return false
			}
			b.edges = append(b.edges, flowEdge{from: from, to: to, l: optText(o["l"])})
			return true
		}
		n, ok1 := text(o["n"])
		t, ok2 := text(o["t"])
		if !ok1 || !ok2 || len(b.nodes) >= maxFlowNodes {
			return false
		}
		for _, x := range b.nodes {
			if x.n == n {
				return false
			}
		}
		b.nodes = append(b.nodes, flowNode{n: n, t: t, d: optText(o["d"]), src: sources(o["src"])})
		return true
	case "tabs":
		name, ok1 := text(o["tab"])
		body, ok2 := textMax(o["text"], maxTabTextChars)
		if !ok1 || !ok2 || len(b.tabs) >= maxTabs {
			return false
		}
		b.tabs = append(b.tabs, tab{name: name, text: body})
		return true
	case "disclose":
		p, ok := textMax(o["p"], maxDiscloseParaChars)
		if !ok || len(b.paras) >= maxDiscloseParas {
			return false
		}
		b.paras = append(b.paras, p)
		return true
	case "claim":
		// Same "+" / "-" rule as procon: a line carrying both is ambiguous.
		pro, okPro := text(o["+"])
		con, okCon := text(o["-"])
		if okPro && !okCon && len(b.supports) < maxClaimEvidence {
			b.supports = append(b.supports, evidence{text: pro, src: sources(o["src"])})
			return true
		}
		if okCon && !okPro && len(b.disputes) < maxClaimEvidence {
			b.disputes = append(b.disputes, evidence{text: con, src: sources(o["src"])})
			return true
		}
		return false
	}
	return false
}

// parseLine mirrors parse.ts's parseLine: the JSON for one line, repairing a
// dropped closing bracket or brace when the structure is otherwise sound.
func parseLine(line string) (any, bool) {
	var obj any
	if err := json.Unmarshal([]byte(line), &obj); err == nil {
		return obj, true
	}
	fixed, ok := closeBrackets(line)
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal([]byte(fixed), &obj); err == nil {
		return obj, true
	}
	return nil, false
}

// closeBrackets mirrors parse.ts's closeBrackets: it returns s with any missing
// ]/} inserted, or ok=false when the structure is too broken to guess at.
//
// A model occasionally drops a closing bracket (`"v":["a","b"}`), which used
// to dump the whole row as visible JSON. Only brackets outside strings count, a
// mismatched closer inserts the one it displaced (`]` before a `}` that would
// close the enclosing object), an unterminated string or an unmatched `]`/`}`
// gives up, and the caller re-parses whatever comes back — so a wrong guess
// still falls through to a raw row.
func closeBrackets(s string) (string, bool) {
	out := make([]byte, 0, len(s)+4)
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
			out = append(out, ch)
		case '[', '{':
			stack = append(stack, ch)
			out = append(out, ch)
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return "", false
			}
			stack = stack[:len(stack)-1]
			out = append(out, ch)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
				out = append(out, ch)
				continue
			}
			// A `}` where a `]` was expected: the dropped `]` goes first.
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return "", false
			}
			stack = stack[:len(stack)-1]
			out = append(out, ']')
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return "", false
			}
			stack = stack[:len(stack)-1]
			out = append(out, '}')
		default:
			out = append(out, ch)
		}
	}
	if inString {
		return "", false
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '[' {
			out = append(out, ']')
		} else {
			out = append(out, '}')
		}
	}
	return string(out), true
}

// parse mirrors parse.ts's parseUi. Only newline-terminated lines are read; a
// trailing partial line is dropped, like a cut-off turn's in the UI.
func parse(src string) []*block {
	lines := strings.Split(src, "\n")
	lines = lines[:len(lines)-1] // the partial (or empty) tail

	var blocks []*block
	var current *block
	used := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		used++
		if used > maxLinesPerFence {
			blocks = append(blocks, &block{kind: "raw", text: "… more lines than a block can hold"})
			break
		}
		obj, ok := parseLine(line)
		if !ok {
			r := rawBlock(line)
			blocks = append(blocks, &r)
			continue
		}
		o, isObj := obj.(map[string]any)
		if !isObj {
			r := rawBlock(line)
			blocks = append(blocks, &r)
			continue
		}
		if _, hasC := o["c"]; hasC {
			if opened, ok := openContainer(o); ok {
				blocks = append(blocks, opened)
				current = opened
			} else {
				r := rawBlock(line)
				blocks = append(blocks, &r)
				current = nil
			}
			continue
		}
		if current == nil || !addChild(current, o) {
			r := rawBlock(line)
			blocks = append(blocks, &r)
		}
	}
	return blocks
}

var months = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// asofLabel turns "2026-10" into "Oct 2026" with a fixed table, not locale
// formatting, so both implementations print the same thing everywhere.
func asofLabel(a string) string {
	m, err := strconv.Atoi(a[5:7])
	if err != nil || m < 1 || m > 12 {
		return a
	}
	return months[m-1] + " " + a[:4]
}

func host(u string) string {
	// Lowercased because JavaScript's URL.hostname is, and the TS twin of
	// this function must print the same label.
	if p, err := url.Parse(u); err == nil && p.Hostname() != "" {
		return strings.ToLower(p.Hostname())
	}
	return u
}

// links renders source URLs as markdown links in the order given, which is
// the schema's canonical order — claim_index matching (verification) depends
// on both implementations emitting links in the same order.
func links(src []string) string {
	parts := make([]string, len(src))
	for i, u := range src {
		parts[i] = fmt.Sprintf("[%s](%s)", host(u), u)
	}
	return strings.Join(parts, " ")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func withSources(s string, src []string) string {
	if len(src) == 0 {
		return s
	}
	return s + " " + links(src)
}

// flattenBlocks renders blocks as plain readable lines. Raw rows are dropped:
// they are what the grammar could not use, which is noise to any reader.
func flattenBlocks(blocks []*block) []string {
	var out []string
	for _, b := range blocks {
		switch b.kind {
		case "callout":
			s := b.text
			if b.asof != "" {
				s += " (as of " + asofLabel(b.asof) + ")"
			}
			out = append(out, withSources(s, b.src))
		case "stat":
			s := b.value
			if b.label != "" {
				s = b.label + ": " + b.value
			}
			if b.note != "" {
				s += " (" + b.note + ")"
			}
			out = append(out, withSources(s, b.src))
		case "compare":
			for ci, col := range b.cols {
				cells := make([]string, len(b.rows))
				for ri, r := range b.rows {
					cells[ri] = r.row + " " + r.v[ci]
				}
				line := col + ": " + strings.Join(cells, "; ")
				if b.pick == ci {
					line += " (recommended)"
				}
				out = append(out, line)
			}
			var src []string
			for _, r := range b.rows {
				src = append(src, r.src...)
			}
			if len(src) > 0 {
				out = append(out, links(src))
			}
		case "steps":
			if b.title != "" {
				out = append(out, b.title+":")
			}
			for n, s := range b.steps {
				line := strconv.Itoa(n+1) + ". " + s.i
				if s.d != "" {
					line += " — " + s.d
				}
				if s.t != "" {
					line += " (" + s.t + ")"
				}
				out = append(out, line)
			}
		case "timeline":
			for _, ev := range b.events {
				out = append(out, withSources(ev.when+": "+ev.i, ev.src))
			}
		case "checklist":
			if b.title != "" {
				out = append(out, b.title+":")
			}
			for _, item := range b.items {
				out = append(out, "- "+item)
			}
		case "procon":
			if len(b.pros) > 0 {
				out = append(out, orDefault(b.proHead, "Pros")+": "+strings.Join(b.pros, "; "))
			}
			if len(b.cons) > 0 {
				out = append(out, orDefault(b.conHead, "Cons")+": "+strings.Join(b.cons, "; "))
			}
		case "choose":
			if b.title != "" {
				out = append(out, b.title+":")
			}
			for _, r := range b.rules {
				out = append(out, withSources("If "+r.cond+": "+r.then, r.src))
			}
		case "facts":
			head := b.title
			if b.title != "" && b.sub != "" {
				head = b.title + " — " + b.sub
			} else if b.title == "" {
				head = b.sub
			}
			if head != "" {
				out = append(out, head+":")
			}
			for _, r := range b.factRows {
				out = append(out, withSources(r.k+": "+r.v, r.src))
			}
		case "flow":
			// Nodes then edges in arrival order; layout is a display concern. An
			// edge whose ends never arrived is dropped.
			title := map[string]string{}
			for _, n := range b.nodes {
				title[n.n] = n.t
				s := n.t
				if n.d != "" {
					s += " — " + n.d
				}
				out = append(out, withSources(s, n.src))
			}
			for _, e := range b.edges {
				from, okFrom := title[e.from]
				to, okTo := title[e.to]
				if okFrom && okTo {
					line := from + " → " + to
					if e.l != "" {
						line += " (" + e.l + ")"
					}
					out = append(out, line)
				}
			}
		case "tabs":
			for _, t := range b.tabs {
				out = append(out, t.name+": "+t.text)
			}
		case "disclose":
			if b.title != "" {
				out = append(out, b.title+":")
			}
			out = append(out, b.paras...)
		case "quote":
			s := `"` + b.text + `"`
			if b.by != "" {
				s += " — " + b.by
			}
			out = append(out, withSources(s, b.src))
		case "claim":
			// The verdict is the model's own read, worded as one; "unverified"
			// is the neutral default and is omitted.
			s := "Claim: " + b.text
			if b.verdict != "unverified" {
				s += " (" + b.verdict + ")"
			}
			out = append(out, s)
			for _, e := range b.supports {
				out = append(out, withSources("Supports: "+e.text, e.src))
			}
			for _, e := range b.disputes {
				out = append(out, withSources("Disputes: "+e.text, e.src))
			}
		}
	}
	return out
}

var (
	fenceRe = regexp.MustCompile("^(`{3,}|~{3,})[ \t]*(.*?)[ \t]*$")
	closeRe = regexp.MustCompile("^(`{3,}|~{3,})[ \t]*$")
)

// Flatten returns content with every column-0 ```ui fence replaced by its
// readable text; everything else is passed through byte for byte. Content
// with no ui fence is returned unchanged (the common case, so it is cheap to
// call on every message).
//
// Mirrors split.ts (streaming=false, ui only): fences are recognised only at
// column 0, another kind of fence is opaque (a ```ui example inside a longer
// ````md fence stays literal), and a fence still open at the end of the text
// is final.
func Flatten(content string) string {
	return rewrite(content, func(body string) string {
		if flat := flattenBlocks(parse(body)); len(flat) > 0 {
			return strings.Join(flat, "\n") + "\n"
		}
		return ""
	})
}

// Strip returns content with every column-0 ```ui fence removed outright.
//
// Claim extraction (gateway/verification.go) uses this rather than Flatten:
// until verification is wired for blocks (docs/plans/intelligent-ui.md
// "Sourcing and verification"), the client's per-URL occurrence counter skips
// `ui` links, so the server must not count them either — a URL cited once in
// a block and once in prose would otherwise put its tick on the wrong chip.
// Both sides turn this on together or not at all.
func Strip(content string) string {
	return rewrite(content, func(string) string { return "" })
}

// rewrite walks content's fences exactly as split.ts does and substitutes
// render(body) for each ui fence; everything else passes through byte for byte.
func rewrite(content string, render func(body string) string) string {
	if !strings.Contains(content, "ui") {
		return content
	}
	lines := strings.Split(content, "\n")
	last := len(lines) - 1
	var out strings.Builder

	type fenceState struct {
		ui   bool
		char byte
		n    int
		body strings.Builder
	}
	var f *fenceState
	emit := func(f *fenceState) { out.WriteString(render(f.body.String())) }

	for i, line := range lines {
		nl := ""
		if i < last {
			nl = "\n"
		}
		if f == nil {
			if m := fenceRe.FindStringSubmatch(line); m != nil && !(m[1][0] == '`' && strings.Contains(m[2], "`")) {
				f = &fenceState{ui: m[2] == "ui", char: m[1][0], n: len(m[1])}
				if !f.ui {
					out.WriteString(line + nl)
				}
				continue
			}
			out.WriteString(line + nl)
			continue
		}
		c := closeRe.FindStringSubmatch(line)
		closes := c != nil && c[1][0] == f.char && len(c[1]) >= f.n
		switch {
		case !f.ui:
			out.WriteString(line + nl)
		case closes:
			emit(f)
		default:
			f.body.WriteString(line + nl)
		}
		if closes {
			f = nil
		}
	}
	if f != nil && f.ui {
		emit(f) // cut off before its closing fence: final, partial line dropped
	}
	return out.String()
}
