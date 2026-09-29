// projects_routes.go is the REST API for Projects (docs/plans/projects.md,
// issue #119): CRUD, the shared-file pool's upload/list/delete, and moving a
// thread between projects. See projects.go for the per-turn prompt side.
//
// Every handler that touches the filesystem resolves the project through the
// database FIRST and joins only the row's own id into a path — the URL's id
// never reaches filepath.Join unverified, so a made-up id can't address an
// arbitrary directory under the workspace root (thread directories live
// beside project ones there).
package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"polaris/store"
	"polaris/tools"
)

const (
	maxProjectNameChars        = 100
	maxProjectDescriptionChars = 300
)

// projectColorPattern accepts an app.css --color-cat-* suffix ("technology",
// "nature-environment") without enumerating them here — the palette is a
// frontend/CSS concern, and an unknown value just renders untinted. The
// pattern only keeps junk (and anything that could ride into a CSS
// variable name) out of the column.
var projectColorPattern = regexp.MustCompile(`^[a-z][a-z-]{0,39}$`)

// projectFields is the editable surface shared by create and patch. Pointers
// so a patch can leave a field alone and still clear one to its empty value.
type projectFields struct {
	Name                  *string `json:"name"`
	Description           *string `json:"description"`
	CustomInstructions    *string `json:"custom_instructions"`
	Favorite              *bool   `json:"favorite"`
	DefaultFocusMode      *string `json:"default_focus_mode"`
	DefaultModel          *string `json:"default_model"`
	MemoryMode            *string `json:"memory_mode"`
	ConstellationVisible  *bool   `json:"constellation_visible"`
	ExcludeFromChatSearch *bool   `json:"exclude_from_chat_search"`
	Color                 *string `json:"color"`
}

// validate returns a user-facing message for the first bad field, or "".
func (f projectFields) validate(models map[string]bool) string {
	if f.Name != nil {
		name := strings.TrimSpace(*f.Name)
		if name == "" {
			return "name is required"
		}
		if len([]rune(name)) > maxProjectNameChars {
			return "name must be 100 characters or fewer"
		}
	}
	if f.Description != nil && len([]rune(*f.Description)) > maxProjectDescriptionChars {
		return "description must be 300 characters or fewer"
	}
	if f.CustomInstructions != nil && len(*f.CustomInstructions) > maxCustomInstructionsChars {
		return "custom_instructions is too long"
	}
	if f.DefaultFocusMode != nil {
		// "" inherits the global default; "off" is a real, distinct choice
		// (this project always starts with no focus mode), not "inherit".
		if m := *f.DefaultFocusMode; m != "" && m != "off" && !validFocusModes[m] {
			return "unknown default_focus_mode"
		}
	}
	if f.DefaultModel != nil && *f.DefaultModel != "" && !models[*f.DefaultModel] {
		return "unknown default_model"
	}
	if f.MemoryMode != nil && !store.ValidProjectMemoryMode(*f.MemoryMode) {
		return "memory_mode must be \"default\" or \"none\""
	}
	if f.Color != nil && *f.Color != "" && !projectColorPattern.MatchString(*f.Color) {
		return "invalid color"
	}
	return ""
}

// liveModelIDs is the set of model ids in the live config — validation
// checks membership the same way handlePutSettings' default-model check does,
// rather than ModelByID, which silently falls back to the default model.
func (s *Server) liveModelIDs() map[string]bool {
	cfg := s.liveConfig()
	ids := make(map[string]bool, len(cfg.Models))
	for _, m := range cfg.Models {
		ids[m.ID] = true
	}
	return ids
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.db.ListProjects()
	if err != nil {
		log.Warn("listing projects failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []store.Project{} // [] not null, so the frontend never special-cases it
	}
	writeJSON(w, projects)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req projectFields
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == nil {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if msg := req.validate(s.liveModelIDs()); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	p := store.Project{
		Name:                 *req.Name,
		ConstellationVisible: true, // opt-out, matching how every thread behaves today
	}
	applyProjectFields(&p, req)
	created, err := s.db.CreateProject(p)
	if err != nil {
		log.Warn("creating project failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "projects", "project created", map[string]interface{}{"project_id": created.ID, "name": created.Name}, "")
	writeJSON(w, created)
}

// applyProjectFields copies the fields a request actually set onto p.
func applyProjectFields(p *store.Project, f projectFields) {
	if f.Description != nil {
		p.Description = *f.Description
	}
	if f.CustomInstructions != nil {
		p.CustomInstructions = *f.CustomInstructions
	}
	if f.Favorite != nil {
		p.Favorite = *f.Favorite
	}
	if f.DefaultFocusMode != nil {
		p.DefaultFocusMode = *f.DefaultFocusMode
	}
	if f.DefaultModel != nil {
		p.DefaultModel = *f.DefaultModel
	}
	if f.MemoryMode != nil {
		p.MemoryMode = *f.MemoryMode
	}
	if f.ConstellationVisible != nil {
		p.ConstellationVisible = *f.ConstellationVisible
	}
	if f.ExcludeFromChatSearch != nil {
		p.ExcludeFromChatSearch = *f.ExcludeFromChatSearch
	}
	if f.Color != nil {
		p.Color = *f.Color
	}
}

// ProjectFile is one entry of a project's shared pool, as listed on the
// detail view.
type ProjectFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// projectDetail is GET /api/projects/{id}'s body: everything the detail view
// renders in one round trip.
type projectDetail struct {
	Project *store.Project `json:"project"`
	Threads []store.Thread `json:"threads"`
	Files   []ProjectFile  `json:"files"`
}

// projectDir returns the project's shared directory, or "" when no workspace
// is configured. id must already have been verified against the database.
func (s *Server) projectDir(id string) string {
	root := s.liveConfig().CodeExec.WorkspaceDir
	if root == "" {
		return ""
	}
	return filepath.Join(root, id)
}

func listProjectFileInfo(dir string) []ProjectFile {
	files := []ProjectFile{}
	if dir == "" {
		return files
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, ProjectFile{Name: e.Name(), SizeBytes: info.Size()})
		}
	}
	return files
}

// loadProject answers 404/500 itself and returns nil when it did.
func (s *Server) loadProject(w http.ResponseWriter, id string) *store.Project {
	p, err := s.db.GetProject(id)
	if errors.Is(err, store.ErrProjectNotFound) {
		http.Error(w, "project not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		log.Warn("loading project failed", "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	return p
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p := s.loadProject(w, r.PathValue("id"))
	if p == nil {
		return
	}
	threads, err := s.db.ListProjectThreads(p.ID)
	if err != nil {
		log.Warn("listing project threads failed", "id", p.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if threads == nil {
		threads = []store.Thread{}
	}
	writeJSON(w, projectDetail{Project: p, Threads: threads, Files: listProjectFileInfo(s.projectDir(p.ID))})
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var req projectFields
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if msg := req.validate(s.liveModelIDs()); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	updated, err := s.db.UpdateProject(r.PathValue("id"), store.ProjectUpdate{
		Name: req.Name, Description: req.Description, CustomInstructions: req.CustomInstructions,
		Favorite: req.Favorite, DefaultFocusMode: req.DefaultFocusMode, DefaultModel: req.DefaultModel,
		MemoryMode: req.MemoryMode, ConstellationVisible: req.ConstellationVisible,
		ExcludeFromChatSearch: req.ExcludeFromChatSearch, Color: req.Color,
	})
	if errors.Is(err, store.ErrProjectNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Warn("updating project failed", "id", r.PathValue("id"), "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, updated)
}

// handleDeleteProject removes the project row (orphaning its threads back to
// ungrouped, in one transaction) and THEN its shared directory — that order,
// because a leftover directory is harmless while a deleted directory under a
// project that failed to delete is not. Threads keep every file of their own:
// their workspaces were never inside the project's directory.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p := s.loadProject(w, r.PathValue("id"))
	if p == nil {
		return
	}
	if err := s.db.DeleteProject(p.ID); err != nil {
		log.Warn("deleting project failed", "id", p.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if dir := s.projectDir(p.ID); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("removing project directory failed", "id", p.ID, "dir", dir, "err", err)
		}
	}
	s.db.LogEvent("", "info", "projects", "project deleted", map[string]interface{}{"project_id": p.ID, "name": p.Name}, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadProjectFile is the project detail view's "add to shared files"
// — an explicit, deliberate path straight into the pool, unlike a chat
// attachment (which lands in that thread's own workspace and is only shared
// via save_to_project). A name collision renames rather than overwrites, the
// same rule save_to_project follows.
func (s *Server) handleUploadProjectFile(w http.ResponseWriter, r *http.Request) {
	p := s.loadProject(w, r.PathValue("id"))
	if p == nil {
		return
	}
	dir := s.projectDir(p.ID)
	if dir == "" {
		http.Error(w, "this deployment has no workspace configured for shared project files", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "invalid or too-large upload (max 100MB): "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing \"file\" field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	if _, uerr := s.uploadContentType(header); uerr != nil {
		http.Error(w, uerr.Error(), uerr.status)
		return
	}
	// Base name only, and never a dotfile: the client's filename is display
	// input, and a leading dot would hide the file from the listing and the
	// prompt (both skip dotfiles, since code_exec drops its own scripts there).
	name := filepath.Base(filepath.ToSlash(header.Filename))
	if name == "." || name == ".." || name == "/" || strings.HasPrefix(name, ".") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	// 0o777 + Chmod: the host-side watcher bind-mounts this directory as a
	// different uid — same reason code_exec's own workspace dirs are 0o777.
	if err := os.MkdirAll(dir, 0o777); err != nil {
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}
	saved, err := tools.WriteUniqueFile(dir, name, io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		log.Warn("saving project file failed", "id", p.ID, "err", err)
		http.Error(w, "couldn't save the file (max 100MB)", http.StatusInternalServerError)
		return
	}
	info, err := os.Stat(filepath.Join(dir, saved))
	if err != nil {
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}
	if info.Size() > maxUploadBytes {
		os.Remove(filepath.Join(dir, saved))
		http.Error(w, "file too large (max 100MB)", http.StatusRequestEntityTooLarge)
		return
	}
	// Touch the project so an upload counts as activity in the hub's recency order.
	s.db.TouchProject(p.ID)
	writeJSON(w, ProjectFile{Name: saved, SizeBytes: info.Size()})
}

func (s *Server) handleDeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	p := s.loadProject(w, r.PathValue("id"))
	if p == nil {
		return
	}
	name := r.PathValue("filename")
	// {filename} is one URL segment, but ".." can still arrive percent-
	// encoded past net/http's own path cleaning — reject it and anything
	// that isn't a bare, non-hidden name.
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	dir := s.projectDir(p.ID)
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(dir, name)
	// Lstat + IsRegular: never follow a symlink out of the pool, and never
	// let a DELETE remove a directory.
	if info, err := os.Lstat(target); err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(target); err != nil {
		http.Error(w, "couldn't delete the file", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetThreadProject moves a thread into a project, or out of any project
// with {"project_id": null}. Deliberately a separate route from PATCH
// /api/threads/{id} (title etc.) and from the turn's project_id field — a
// re-home is a distinct, explicit action, and a stray field on an ordinary
// message must never silently move a conversation.
func (s *Server) handleSetThreadProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID *string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ProjectID != nil && *req.ProjectID == "" {
		req.ProjectID = nil // "" and null both mean "remove from project"
	}
	err := s.db.SetThreadProject(r.PathValue("id"), req.ProjectID)
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		http.Error(w, "project not found", http.StatusNotFound)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		log.Warn("moving thread to project failed", "thread_id", r.PathValue("id"), "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
