package threadexport

import (
	"bytes"
	"encoding/json"
)

// Render renders the document in the format named by its Options.Format.
// An unset or unknown format falls back to Markdown, the modal's default.
func Render(d Doc) (string, error) {
	if d.Options.Format == FormatJSON {
		b, err := RenderJSON(d)
		return string(b), err
	}
	return RenderMarkdown(d), nil
}

// RenderJSON renders the document as indented, versioned JSON for tooling
// and audits. Field names are stable across a schema version; additive
// fields keep Version unchanged so an older reader still accepts a newer
// file. It never includes a raw transcript blob — trail steps are
// normalized from it (phase 2).
func RenderJSON(d Doc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// HTML escaping turns ordinary prose punctuation ("<", ">", "&") into
	// \u003c etc. — correct for embedding in a web page, wrong for a file
	// meant to be read and diffed.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
