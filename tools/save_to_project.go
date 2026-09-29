// save_to_project is the one deliberate way a file joins a Project's shared
// pool (docs/plans/projects.md, issue #119). A thread's own workspace is
// private and read-write; the project's directory is mounted read-only into
// every project thread's sandbox, so nothing lands there by accident — a
// file gets there only because the model (or user, via the model) chose to
// promote it. One primitive rather than a fetch_url-only flag, so a
// code_exec-generated file has the same path in as a downloaded one.
//
// Only offered on a project thread with a configured workspace — catalog.go's
// "project_workspace" case — so this handler's own checks are defense in
// depth, not the primary gate.
package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"polaris/llm"
)

var saveToProjectDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "save_to_project",
		// Description is populated at call time from
		// tools/descriptions/save_to_project.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"filename": map[string]interface{}{
					"type": "string",
					"description": "Path (relative to this conversation's own workspace) of the file to copy into the " +
						"project's shared files. Only the file's base name is kept in the project.",
				},
			},
			"required": []string{"filename"},
		},
	},
}

func init() { Register("save_to_project", handleSaveToProject) }

func handleSaveToProject(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "save_to_project", nil, "error: "+err.Error(), callID)
	}
	callArgs := map[string]interface{}{"filename": args.Filename}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "save_to_project", "args": callArgs, "call_id": callID})

	fail := func(msg string) string {
		result := "error: " + msg
		ctx.Emit("tool_result", map[string]interface{}{"tool": "save_to_project", "result": result, "call_id": callID})
		return result
	}

	if ctx.ProjectID == "" || ctx.CodeExecWorkspaceDir == "" || ctx.ThreadID == "" {
		return fail("this conversation isn't part of a project")
	}
	if args.Filename == "" {
		return fail("filename is required")
	}

	// The source is the thread's OWN directory only — never the project
	// fallback resolveWorkspaceFilePath allows for reads. Copying a project
	// file onto itself under a new name is never what anyone wants.
	ownDir := filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID)
	src := filepath.Join(ownDir, args.Filename)
	if rel, err := filepath.Rel(ownDir, src); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fail("path escapes the workspace directory")
	}
	// Lstat, not Stat: the sandbox can create a symlink in its own workspace
	// pointing at any path *inside the container* — which the host would
	// resolve against the host filesystem. Following it here would let a
	// script copy an arbitrary host file into the shared pool.
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(fmt.Sprintf("no file %q in this conversation's workspace", args.Filename))
		}
		return fail("couldn't read the file: " + err.Error())
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Sprintf("%q isn't a regular file", args.Filename))
	}

	projectDir := filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ProjectID)
	// 0o777 + Chmod for the same cross-UID reason code_exec documents on its
	// own workspace directory: the host-side watcher bind-mounts this and a
	// different uid must be able to traverse it.
	if err := os.MkdirAll(projectDir, 0o777); err != nil {
		return fail("couldn't prepare the project's shared directory: " + err.Error())
	}
	if err := os.Chmod(projectDir, 0o777); err != nil {
		return fail("couldn't set the project directory's permissions: " + err.Error())
	}

	savedAs, err := copyIntoDirNoClobber(src, projectDir, filepath.Base(args.Filename))
	if err != nil {
		return fail("couldn't copy the file: " + err.Error())
	}

	result := fmt.Sprintf("saved %q to the project's shared files", savedAs)
	if savedAs != filepath.Base(args.Filename) {
		result = fmt.Sprintf("the project already had a file named %q, so this was saved as %q instead — nothing was overwritten",
			filepath.Base(args.Filename), savedAs)
	}
	log.Info("save_to_project", "project_id", ctx.ProjectID, "thread_id", ctx.ThreadID, "saved_as", savedAs)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "save_to_project", "result": result, "call_id": callID})
	return result
}

// copyIntoDirNoClobber copies src into dir under name, or name-2/name-3/…
// (before the extension) when that name is taken, and returns the name it
// actually used. O_EXCL makes "is this name free" and "claim it" one atomic
// step, so two threads promoting the same filename at the same moment each
// get their own file instead of one silently overwriting the other — the
// entire point of the shared pool is that a later promote can never destroy
// an earlier thread's contribution.
func copyIntoDirNoClobber(src, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	candidate := name
	for n := 2; n < 10000; n++ {
		// 0o644: the sandbox's own (different) uid only ever reads these, via
		// the read-only mount.
		out, err := os.OpenFile(filepath.Join(dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, copyErr := io.Copy(out, in)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil {
				os.Remove(out.Name())
				if copyErr == nil {
					copyErr = closeErr
				}
				return "", copyErr
			}
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, n, ext)
	}
	return "", fmt.Errorf("too many files named %q already exist", name)
}
