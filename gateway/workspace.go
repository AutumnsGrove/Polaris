// workspace.go serves a single thread's code_exec workspace file back
// over HTTP — the piece tools/show.go's tool_result URL points at.
// Nothing served an attachment/workspace file's raw bytes back to the
// browser before this (confirmed directly in the codebase while
// designing show — see docs/plans/show.md), so this is genuinely new
// plumbing, not a rewire of an existing route.
//
// This is a single-operator, no-user-auth deployment (same posture as
// every other /api/* route in this file), so there's no per-user
// ownership check to make beyond the path-traversal defense below —
// but that defense is real, not decorative: thread_id/filename are both
// taken directly from the URL, so a well-formed workspace path is
// enforced the same defensive way tools/view_image.go's
// resolveWorkspaceFilePath already enforces it for the Go tool handler
// side of the same contract.
package gateway

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxWorkspaceFileServeBytes bounds how much of a workspace file this
// route ever reads into memory — defense in depth against an
// unexpectedly huge file ending up in a thread's workspace, independent
// of whatever cap the tool that wrote it already enforced (fetch_url's
// own 20MB cap, code_exec's chart output). Generous relative to
// anything a chat-driven artifact should actually be.
const maxWorkspaceFileServeBytes = 20 << 20 // 20MB

func (s *Server) handleGetWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("thread_id")
	filename := r.PathValue("filename")
	if threadID == "" || filename == "" {
		http.NotFound(w, r)
		return
	}

	cfg := s.liveConfig()
	if cfg.CodeExec.WorkspaceDir == "" {
		http.NotFound(w, r)
		return
	}

	base := filepath.Join(cfg.CodeExec.WorkspaceDir, threadID)
	target := filepath.Join(base, filename)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxWorkspaceFileServeBytes+1))
	if err != nil || len(data) > maxWorkspaceFileServeBytes {
		http.Error(w, "file too large to serve", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Write(data)
}
