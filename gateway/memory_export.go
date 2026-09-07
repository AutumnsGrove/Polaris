// memory_export.go backs the Memory settings page's "export all memories"
// button — a deterministic dump of every active memory, grouped by type,
// for backup/portability. The inverse direction of memory_import.go: that
// file asks a model to turn another assistant's prose into Polaris
// memories; this one just formats Polaris's own already-structured
// memories back out as plain text, so no model call (and no cost) is
// needed to produce it.
package gateway

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"polaris/store"
)

// memoryExportTypeOrder fixes the section order in the exported text — the
// same four types tools/memory.go's isValidMemoryType recognizes, ordered
// most to least likely to matter to someone skimming a backup: durable
// identity/preference facts first, standing guidance next, active work,
// then pointers last.
var memoryExportTypeOrder = []string{"user", "feedback", "project", "reference"}

var memoryExportTypeLabels = map[string]string{
	"user":      "User",
	"feedback":  "Feedback",
	"project":   "Project",
	"reference": "Reference",
}

func (s *Server) handleExportMemories(w http.ResponseWriter, r *http.Request) {
	memories, err := s.db.ListMemoriesFull()
	if err != nil {
		log.Warn("listing memories for export failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	byType := make(map[string][]store.Memory, len(memoryExportTypeOrder))
	for _, m := range memories {
		byType[m.Type] = append(byType[m.Type], m)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Polaris memory export — %s\n", time.Now().Format("2006-01-02"))
	for _, t := range memoryExportTypeOrder {
		rows := byType[t]
		if len(rows) == 0 {
			continue
		}
		// Deterministic order, not insertion/query order — ListMemoriesFull
		// already sorts by type then name, but sorting again here doesn't
		// depend on that staying true and costs nothing at this size.
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		fmt.Fprintf(&sb, "\n## %s\n", memoryExportTypeLabels[t])
		for _, m := range rows {
			date := m.OccurredAt
			if date == "" {
				date = "unknown"
			}
			fmt.Fprintf(&sb, "\n[%s] %s: %s\n%s\n", date, m.Name, m.Description, m.Content)
		}
	}

	// Content-Disposition: attachment — the browser downloads it directly
	// from a plain <a href> click, no client-side JS (fetch + Blob + a
	// synthetic click) needed to trigger a "save file" prompt.
	filename := fmt.Sprintf("polaris-memories-%s.txt", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Write([]byte(sb.String()))
}
