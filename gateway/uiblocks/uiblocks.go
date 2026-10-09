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
	maxCompareRows   = 12
	maxSteps         = 15
	maxRawChars      = 200
)

type block struct {
	kind string // callout | stat | compare | steps | raw

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
func text(v any) (string, bool) {
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
	return clip(s, maxTextChars), true
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
		// 2-4 columns, and no column silently dropped for being invalid.
		if len(cols) < 2 || len(cols) > 4 || len(cols) != len(raw) {
			return nil, false
		}
		pick := -1
		if p, ok := o["pick"].(float64); ok && p == math.Trunc(p) && p >= 0 && int(p) < len(cols) {
			pick = int(p)
		}
		return &block{kind: "compare", cols: cols, pick: pick}, true
	case "steps":
		return &block{kind: "steps", title: optText(o["title"])}, true
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
	}
	return false
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
		var obj any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
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
