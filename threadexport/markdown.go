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
