// fields.go is the gateway side of Fields (docs/plans/fields.md, issue
// #119): the per-turn prompt block a field thread gets, and (in
// fields_routes.go) the REST surface. The data lives in store/fields.go;
// the shared file pool lives on disk at <CodeExecWorkspaceDir>/<fieldID>/.
package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"polaris/store"
)

// maxFieldPromptFiles caps how many shared filenames the per-turn opener
// lists. Names only, deliberately — the model can ls /field in code_exec
// to go deeper, the same "let it find its own way in" call the workspace-
// unification doc made for single attachments — and a pool that accumulates
// files across many threads must not be able to balloon every turn's prompt.
const maxFieldPromptFiles = 40

// listFieldFiles returns the regular files directly inside a field's
// shared directory, sorted, or nil when the directory doesn't exist yet
// (a field nobody has promoted a file into) or the workspace isn't
// configured. Dotfiles are skipped: code_exec drops its own .code_exec_*.py
// scripts into a workspace directory, and none of that is field content.
func listFieldFiles(workspaceDir, fieldID string) []string {
	if workspaceDir == "" || fieldID == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(workspaceDir, fieldID))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// fieldPromptBlock is what a field thread's turn appends after the
// operator's global custom instructions inside {custom_instructions} —
// additive, never a replacement, so a field can't hide the operator's
// standing instructions. Returns "" for a nil field. The shared-file line
// spells out that the originals are read-only and how an edit actually
// persists, because the model needs to know that up front: without it, an
// attempted in-place edit of /field/x just fails with a bare
// "Read-only file system" and the model has no idea why.
func fieldPromptBlock(p *store.Field, sharedFiles []string) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Field: %s\n", p.Name)
	if instr := strings.TrimSpace(p.CustomInstructions); instr != "" {
		b.WriteString(instr)
		b.WriteString("\n")
	}
	if len(sharedFiles) > 0 {
		shown := sharedFiles
		more := 0
		if len(shown) > maxFieldPromptFiles {
			more = len(shown) - maxFieldPromptFiles
			shown = shown[:maxFieldPromptFiles]
		}
		b.WriteString("\nThis conversation belongs to that field. Its shared files are read-only originals, " +
			"under /field in code_exec (and readable by name with view_image/show): " + strings.Join(shown, ", "))
		if more > 0 {
			fmt.Fprintf(&b, ", and %d more (ls /field to see them all)", more)
		}
		b.WriteString(". To change one, write your edited copy into your own workspace — the original stays as it is. " +
			"save_to_field adds a file from your workspace to the shared set for every conversation in the field.\n")
	}
	return b.String()
}

// joinCustomInstructions appends the field block after the operator's
// global field with a blank line between them — or returns whichever one
// exists, so an install with no global instructions doesn't get a leading gap.
func joinCustomInstructions(global, fieldBlock string) string {
	switch {
	case fieldBlock == "":
		return global
	case strings.TrimSpace(global) == "":
		return fieldBlock
	default:
		return global + "\n\n" + fieldBlock
	}
}
