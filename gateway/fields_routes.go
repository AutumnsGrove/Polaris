// fields_routes.go is the REST API for Fields (docs/plans/fields.md,
// issue #119): CRUD, the shared-file pool's upload/list/delete, and moving a
// thread between fields. See fields.go for the per-turn prompt side.
//
// Every handler that touches the filesystem resolves the field through the
// database FIRST and joins only the row's own id into a path — the URL's id
// never reaches filepath.Join unverified, so a made-up id can't address an
// arbitrary directory under the workspace root (thread directories live
// beside field ones there).
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
	maxFieldNameChars        = 100
	maxFieldDescriptionChars = 300
)

// fieldColorPattern accepts an app.css --color-cat-* suffix ("technology",
// "nature-environment") without enumerating them here — the palette is a
// frontend/CSS concern, and an unknown value just renders untinted. The
// pattern only keeps junk (and anything that could ride into a CSS
// variable name) out of the column.
var fieldColorPattern = regexp.MustCompile(`^[a-z][a-z-]{0,39}$`)

// fieldInput is the editable surface shared by create and patch. Pointers
// so a patch can leave a field alone and still clear one to its empty value.
type fieldInput struct {
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
func (f fieldInput) validate(models map[string]bool) string {
	if f.Name != nil {
		name := strings.TrimSpace(*f.Name)
		if name == "" {
			return "name is required"
		}
		if len([]rune(name)) > maxFieldNameChars {
			return "name must be 100 characters or fewer"
		}
	}
	if f.Description != nil && len([]rune(*f.Description)) > maxFieldDescriptionChars {
		return "description must be 300 characters or fewer"
	}
	if f.CustomInstructions != nil && len(*f.CustomInstructions) > maxCustomInstructionsChars {
		return "custom_instructions is too long"
	}
	if f.DefaultFocusMode != nil {
		// "" inherits the global default; "off" is a real, distinct choice
		// (this field always starts with no focus mode), not "inherit".
		if m := *f.DefaultFocusMode; m != "" && m != "off" && !validFocusModes[m] {
			return "unknown default_focus_mode"
		}
	}
	if f.DefaultModel != nil && *f.DefaultModel != "" && !models[*f.DefaultModel] {
		return "unknown default_model"
	}
	if f.MemoryMode != nil && !store.ValidFieldMemoryMode(*f.MemoryMode) {
		return "memory_mode must be \"default\", \"field_only\", \"both\" or \"none\""
	}
	if f.Color != nil && *f.Color != "" && !fieldColorPattern.MatchString(*f.Color) {
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

func (s *Server) handleListFields(w http.ResponseWriter, r *http.Request) {
	fields, err := s.db.ListFields()
	if err != nil {
		log.Warn("listing fields failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if fields == nil {
		fields = []store.Field{} // [] not null, so the frontend never special-cases it
	}
	writeJSON(w, fields)
}

func (s *Server) handleCreateField(w http.ResponseWriter, r *http.Request) {
	var req fieldInput
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
	p := store.Field{
		Name:                 *req.Name,
		ConstellationVisible: true, // opt-out, matching how every thread behaves today
	}
	applyFieldInput(&p, req)
	created, err := s.db.CreateField(p)
	if err != nil {
		log.Warn("creating field failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "fields", "field created", map[string]interface{}{"field_id": created.ID, "name": created.Name}, "")
	writeJSON(w, created)
}

// applyFieldInput copies the fields a request actually set onto p.
func applyFieldInput(p *store.Field, f fieldInput) {
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

// FieldFile is one entry of a field's shared pool, as listed on the
// detail view.
type FieldFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// fieldDetail is GET /api/fields/{id}'s body: everything the detail view
// renders in one round trip.
type fieldDetail struct {
	Field   *store.Field   `json:"field"`
	Threads []store.Thread `json:"threads"`
	Files   []FieldFile    `json:"files"`
}

// fieldDir returns the field's shared directory, or "" when no workspace
// is configured. id must already have been verified against the database.
func (s *Server) fieldDir(id string) string {
	root := s.liveConfig().CodeExec.WorkspaceDir
	if root == "" {
		return ""
	}
	return filepath.Join(root, id)
}

func listFieldFileInfo(dir string) []FieldFile {
	files := []FieldFile{}
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
			files = append(files, FieldFile{Name: e.Name(), SizeBytes: info.Size()})
		}
	}
	return files
}

// loadField answers 404/500 itself and returns nil when it did.
func (s *Server) loadField(w http.ResponseWriter, id string) *store.Field {
	p, err := s.db.GetField(id)
	if errors.Is(err, store.ErrFieldNotFound) {
		http.Error(w, "field not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		log.Warn("loading field failed", "id", id, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	return p
}

func (s *Server) handleGetField(w http.ResponseWriter, r *http.Request) {
	p := s.loadField(w, r.PathValue("id"))
	if p == nil {
		return
	}
	threads, err := s.db.ListFieldThreads(p.ID)
	if err != nil {
		log.Warn("listing field threads failed", "id", p.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if threads == nil {
		threads = []store.Thread{}
	}
	writeJSON(w, fieldDetail{Field: p, Threads: threads, Files: listFieldFileInfo(s.fieldDir(p.ID))})
}

func (s *Server) handleUpdateField(w http.ResponseWriter, r *http.Request) {
	var req fieldInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if msg := req.validate(s.liveModelIDs()); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	updated, err := s.db.UpdateField(r.PathValue("id"), store.FieldUpdate{
		Name: req.Name, Description: req.Description, CustomInstructions: req.CustomInstructions,
		Favorite: req.Favorite, DefaultFocusMode: req.DefaultFocusMode, DefaultModel: req.DefaultModel,
		MemoryMode: req.MemoryMode, ConstellationVisible: req.ConstellationVisible,
		ExcludeFromChatSearch: req.ExcludeFromChatSearch, Color: req.Color,
	})
	if errors.Is(err, store.ErrFieldNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Warn("updating field failed", "id", r.PathValue("id"), "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, updated)
}

// handleDeleteField removes the field row (orphaning its threads back to
// ungrouped, in one transaction) and THEN its shared directory — that order,
// because a leftover directory is harmless while a deleted directory under a
// field that failed to delete is not. Threads keep every file of their own:
// their workspaces were never inside the field's directory.
func (s *Server) handleDeleteField(w http.ResponseWriter, r *http.Request) {
	p := s.loadField(w, r.PathValue("id"))
	if p == nil {
		return
	}
	if err := s.db.DeleteField(p.ID); err != nil {
		log.Warn("deleting field failed", "id", p.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if dir := s.fieldDir(p.ID); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("removing field directory failed", "id", p.ID, "dir", dir, "err", err)
		}
	}
	s.db.LogEvent("", "info", "fields", "field deleted", map[string]interface{}{"field_id": p.ID, "name": p.Name}, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadFieldFile is the field detail view's "add to shared files"
// — an explicit, deliberate path straight into the pool, unlike a chat
// attachment (which lands in that thread's own workspace and is only shared
// via save_to_field). A name collision renames rather than overwrites, the
// same rule save_to_field follows.
func (s *Server) handleUploadFieldFile(w http.ResponseWriter, r *http.Request) {
	p := s.loadField(w, r.PathValue("id"))
	if p == nil {
		return
	}
	dir := s.fieldDir(p.ID)
	if dir == "" {
		http.Error(w, "this deployment has no workspace configured for shared field files", http.StatusServiceUnavailable)
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
		log.Warn("saving field file failed", "id", p.ID, "err", err)
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
	// Touch the field so an upload counts as activity in the hub's recency order.
	s.db.TouchField(p.ID)
	writeJSON(w, FieldFile{Name: saved, SizeBytes: info.Size()})
}

func (s *Server) handleDeleteFieldFile(w http.ResponseWriter, r *http.Request) {
	p := s.loadField(w, r.PathValue("id"))
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
	dir := s.fieldDir(p.ID)
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

// handleSetThreadField moves a thread into a field, or out of any field
// with {"field_id": null}. Deliberately a separate route from PATCH
// /api/threads/{id} (title etc.) and from the turn's field_id field — a
// re-home is a distinct, explicit action, and a stray field on an ordinary
// message must never silently move a conversation.
func (s *Server) handleSetThreadField(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FieldID *string `json:"field_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.FieldID != nil && *req.FieldID == "" {
		req.FieldID = nil // "" and null both mean "remove from field"
	}
	err := s.db.SetThreadField(r.PathValue("id"), req.FieldID)
	switch {
	case errors.Is(err, store.ErrFieldNotFound):
		http.Error(w, "field not found", http.StatusNotFound)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		log.Warn("moving thread to field failed", "thread_id", r.PathValue("id"), "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
