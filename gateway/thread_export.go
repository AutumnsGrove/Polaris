// thread_export.go is the HTTP surface for docs/plans/thread-export.md:
// GET /api/threads/{id}/export. One thread, one request — nothing here
// spends money (handoff reuses a stored summary), so a plain GET with query
// parameters is enough and needs no POST body or job handle.
package gateway

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"polaris/threadexport"
	"polaris/tools"
)

// handleThreadExport serves a single thread as Markdown or versioned JSON.
// A dry_run=1 request returns just the token estimate and availability the
// modal shows, built from the same Doc as a real export.
func (s *Server) handleThreadExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	opts := exportOptions(r)

	in, err := s.db.ThreadExportInput(id)
	if err != nil {
		// GetThread inside ThreadExportInput deliberately excludes ghost,
		// disabled and hidden-variant threads, so ErrNoRows here is the
		// "ghost threads are refused" rule from the plan, not just a missing
		// id. Anything else is transient and retryable, matching
		// handleGetThread's split.
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "thread not found", http.StatusNotFound)
			return
		}
		log.Warn("gathering thread export input failed", "thread", id, "err", err)
		http.Error(w, "temporarily unable to export thread — please retry", http.StatusServiceUnavailable)
		return
	}
	in.AppVersion = s.version
	// The privacy sets come from the tool catalog, so a tool that declares
	// export_privacy is covered without this endpoint naming it.
	in.MemoryTools, in.PersonalTools = tools.ExportPrivacyClass()

	doc := threadexport.Build(in, opts)

	if exportBool(r, "dry_run", false) {
		writeJSON(w, map[string]interface{}{
			"tokens":       threadexport.EstimateTokens(doc),
			"availability": doc.Availability,
		})
		return
	}

	filename := exportFilename(doc.Thread.Title, opts.Format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	if opts.Format == threadexport.FormatJSON {
		b, err := threadexport.RenderJSON(doc)
		if err != nil {
			log.Warn("rendering thread export JSON failed", "thread", id, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(threadexport.RenderMarkdown(doc)))
}

// exportOptions reads the modal's choices off the query string. Defaults
// mirror the modal (sources and private on, everything else off), so a bare
// GET is a safe, private, cited Markdown export rather than accidentally
// exposing thinking traces or dropping the source list.
func exportOptions(r *http.Request) threadexport.Options {
	format := threadexport.FormatMarkdown
	if r.URL.Query().Get("format") == "json" {
		format = threadexport.FormatJSON
	}
	scope := threadexport.ScopeFull
	if r.URL.Query().Get("scope") == "handoff" {
		scope = threadexport.ScopeHandoff
	}
	return threadexport.Options{
		Format:          format,
		Scope:           scope,
		Sources:         exportBool(r, "sources", true),
		Attachments:     exportBool(r, "attachments", false),
		Meta:            exportBool(r, "meta", false),
		Trail:           exportBool(r, "trail", false),
		Thinking:        exportBool(r, "thinking", false),
		Private:         exportBool(r, "private", true),
		PrivatePersonal: exportBool(r, "private_personal", false),
	}
}

// exportBool reads a 1/0/true/false-style query parameter, returning def
// when the key is absent or unrecognized (so a typo can't silently flip a
// privacy toggle off).
func exportBool(r *http.Request, key string, def bool) bool {
	switch strings.ToLower(r.URL.Query().Get(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// exportFilename builds the download name from the thread title: a
// lowercased, hyphenated slug plus the date, so two exports of the same
// thread on different days don't collide and the file reads as what it is.
func exportFilename(title string, format threadexport.Format) string {
	slug := slugify(title)
	if slug == "" {
		slug = "thread"
	}
	ext := "md"
	if format == threadexport.FormatJSON {
		ext = "json"
	}
	return fmt.Sprintf("polaris-%s-%s.%s", slug, time.Now().Format("2006-01-02"), ext)
}

// slugify lowercases ASCII letters/digits and collapses everything else to
// single hyphens. A title that is all non-ASCII collapses to "" and the
// caller falls back to "thread" rather than shipping an empty filename.
func slugify(s string) string {
	const maxLen = 60
	var b strings.Builder
	lastHyphen := true // leading separators don't produce a leading hyphen
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	return out
}
