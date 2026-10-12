package threadexport

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown renders the document as a readable transcript — for a
// human, or for pasting into another model. It reads only Doc, so every
// option has already been applied by Build.
func RenderMarkdown(d Doc) string {
	var b strings.Builder

	title := strings.TrimSpace(d.Thread.Title)
	if title == "" {
		title = "Untitled thread"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)

	b.WriteString("_" + dateRange(d.Thread.CreatedAt, d.Thread.UpdatedAt) + "_\n")
	if d.Options.Meta {
		if model := strings.TrimSpace(d.Thread.Model); model != "" {
			fmt.Fprintf(&b, "Model: %s\n", model)
		}
	}
	b.WriteString("\n")

	if d.Summary != nil && strings.TrimSpace(d.Summary.Text) != "" {
		b.WriteString("## Summary of earlier conversation\n\n")
		writeBlock(&b, d.Summary.Text)
	}

	for _, t := range d.Turns {
		if t.User != nil {
			b.WriteString("## You\n\n")
			writeBlock(&b, t.User.Content)
			for _, a := range t.User.Attachments {
				b.WriteString(attachmentLine(a) + "\n")
			}
			if len(t.User.Attachments) > 0 {
				b.WriteString("\n")
			}
		}

		b.WriteString("## Assistant\n\n")
		if t.Assistant == nil {
			b.WriteString("_(no answer recorded)_\n\n")
			continue
		}
		if t.Meta != nil {
			b.WriteString(metaLine(*t.Meta) + "\n\n")
		}
		// Trail and thinking come before the answer they belong to, in
		// quoted blocks that read as clearly separate from the output so a
		// model pasting this in can tell reasoning from the final answer.
		if len(t.Trace) > 0 {
			writeTrail(&b, t.Trace)
		}
		if len(t.Thinking) > 0 {
			writeThinking(&b, t.Thinking)
		}
		writeBlock(&b, t.Assistant.Content)
		writeSources(&b, t.Assistant.Citations)
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeBlock writes one message body, normalizing trailing whitespace and
// guaranteeing a blank line after it.
func writeBlock(b *strings.Builder, s string) {
	b.WriteString(strings.TrimRight(s, "\n"))
	b.WriteString("\n\n")
}

// writeSources emits the numbered source list that the inline [n] markers
// in an answer refer to. Markers are kept as-is: renumbering would break
// the correspondence the model wrote.
func writeSources(b *strings.Builder, citations []Citation) {
	if len(citations) == 0 {
		return
	}
	b.WriteString("**Sources**\n\n")
	for i, c := range citations {
		label := strings.TrimSpace(c.Title)
		if label == "" {
			label = c.URL
		}
		if label == "" {
			label = "(untitled)"
		}
		fmt.Fprintf(b, "%d. [%s](%s)\n", i+1, label, c.URL)
	}
	b.WriteString("\n")
}

// writeTrail renders the research trail as a quoted, itemized list of tool
// calls with their results, so it is visibly not part of the answer text.
func writeTrail(b *strings.Builder, steps []TraceStep) {
	b.WriteString("> **Research trail**\n>\n")
	for _, s := range steps {
		args := strings.TrimSpace(s.Args)
		if args == "" || args == "{}" {
			fmt.Fprintf(b, "> - **%s**\n", s.Tool)
		} else {
			fmt.Fprintf(b, "> - **%s** `%s`\n", s.Tool, args)
		}
		if r := strings.TrimSpace(s.Result); r != "" {
			b.WriteString(">\n")
			for _, line := range strings.Split(r, "\n") {
				fmt.Fprintf(b, ">   %s\n", line)
			}
		}
	}
	b.WriteString("\n")
}

// writeThinking renders reasoning bursts as a quoted block, labeling each
// with its agent so a Deep Research turn's sub-agent thinking is
// distinguishable from the orchestrator's.
func writeThinking(b *strings.Builder, bursts []Thinking) {
	b.WriteString("> **Thinking**\n>\n")
	for _, th := range bursts {
		if th.Agent != "" || th.DurationMs > 0 {
			label := th.Agent
			if label == "" {
				label = "main"
			}
			if th.DurationMs > 0 {
				label += fmt.Sprintf(", %.1fs", float64(th.DurationMs)/1000)
			}
			fmt.Fprintf(b, "> _%s_\n>\n", label)
		}
		for _, line := range strings.Split(strings.TrimSpace(th.Content), "\n") {
			fmt.Fprintf(b, "> %s\n", line)
		}
	}
	b.WriteString("\n")
}

func attachmentLine(a Attachment) string {
	if a.ContentType == "" {
		return "📎 " + a.Filename
	}
	return fmt.Sprintf("📎 %s (%s)", a.Filename, a.ContentType)
}

func metaLine(m TurnMeta) string {
	parts := []string{}
	if m.Model != "" {
		parts = append(parts, m.Model)
	}
	if m.DurationMs > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(m.DurationMs)/1000))
	}
	parts = append(parts, fmt.Sprintf("$%.4f", m.CostUSD))
	return "_" + strings.Join(parts, " · ") + "_"
}

// dateRange renders a thread's span, collapsing a single-day thread to one
// date rather than repeating it.
func dateRange(created, updated time.Time) string {
	c := created.Format("2006-01-02")
	u := updated.Format("2006-01-02")
	if c == u || updated.IsZero() {
		return c
	}
	return c + " – " + u
}
