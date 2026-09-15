// attachments.go handles file uploads from the composer's "+" menu
// (see web/src/lib/components/ComposerMenu.svelte) — a separate REST
// call ahead of the WebSocket message, same two-step shape as push-to-talk
// voice memos (POST /api/transcribe, then the transcribed text rides
// along in the next ClientMessage). Here, the upload returns an opaque
// ID; the frontend sends that ID in ClientMessage.Attachments, and
// handleTurn resolves it back to a file on disk — never a path the
// client supplies directly.
package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"polaris/config"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
)

// maxUploadBytes caps a single attachment. Raised from an original 20MB
// (found live: a real ~27MB multi-hundred-page PDF model card bounced off
// the old cap) to 100MB, still a real ceiling rather than unbounded — the
// whole file still gets buffered once during saveUploadedFile's write and
// resolveAttachment's move into the workspace, on hardware as constrained
// as the potato.
const maxUploadBytes = 100 << 20

// allowedUploadContentTypes is the plain-text/structured-data set beyond
// PDF/image — leftover from when an upload only ever fed one of two
// pipelines (PDF-text extraction, vision-model description), so anything
// neither pipeline understood was rejected outright. Now that an upload
// just becomes a workspace file (see resolveAttachment), code_exec can
// already open/parse any of these directly (open().read(),
// pd.read_csv(), json.load(), ...) — there's no processing-pipeline reason
// left to reject them, only the genuine safety reason of not accepting
// something script/executable-shaped, which this stays a real (if
// generous) allowlist against.
var allowedUploadContentTypes = map[string]bool{
	"text/plain":         true,
	"text/markdown":      true,
	"text/x-markdown":    true,
	"text/csv":           true,
	"application/csv":    true,
	"application/json":   true,
	"application/x-yaml": true,
	"text/yaml":          true,
	"text/x-yaml":        true,
	"application/xml":    true,
	"text/xml":           true,
}

// allowedUploadContentType accepts exactly what ComposerMenu's file input
// offers (accept="image/*,.pdf,.md,.txt,.json,.csv,.yaml,.yml,.xml") —
// anything else is almost certainly a mistake or a client someone's about
// to abuse, not the human uploading a photo or document.
func allowedUploadContentType(ct string) bool {
	return ct == "application/pdf" || strings.HasPrefix(ct, "image/") || allowedUploadContentTypes[ct]
}

// UploadResponse is what POST /api/upload returns — ID is what the
// frontend echoes back in ClientMessage.Attachments.
type UploadResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// handleUpload accepts a multipart/form-data POST with a single "file"
// field, saves it to config.Attachments.Dir under a generated name (never
// the client-supplied filename — that's only kept for display, see
// store.Message.Attachments), and returns its ID.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20) // +1MiB slack for multipart overhead/headers
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

	resp, uerr := s.saveUploadedFile(file, header)
	if uerr != nil {
		var ue *uploadError
		status := http.StatusInternalServerError
		if errors.As(uerr, &ue) {
			status = ue.status
		}
		http.Error(w, uerr.Error(), status)
		return
	}
	writeJSON(w, resp)
}

// uploadError carries the HTTP status a failure from saveUploadedFile
// should map to — shared between handleUpload and handleAsk/
// handleAskStream's inline-multipart path (see ask.go), so both surface
// the same status codes for the same failures (bad content type, oversized,
// storage error) without duplicating that mapping in two places.
type uploadError struct {
	status int
	msg    string
}

func (e *uploadError) Error() string { return e.msg }

// saveUploadedFile validates and saves one already-opened multipart file
// part to config.Attachments.Dir under a generated UUID name — the shared
// core behind both POST /api/upload (a dedicated upload-then-reference
// call) and POST /api/ask's inline multipart path (upload and ask in one
// round trip, for a caller like `curl -F file=@modelcard.pdf -F
// content="highlights from page 50"` that doesn't want a separate upload
// step first). Never trusts a caller-supplied path or filename for
// storage — same trust boundary either caller goes through, only the
// filename is ever kept (for display), never used to name the file on
// disk.
func (s *Server) saveUploadedFile(file multipart.File, header *multipart.FileHeader) (UploadResponse, error) {
	cfg := s.liveConfig()

	contentType := header.Header.Get("Content-Type")
	// "" (no guess at all) and the generic "application/octet-stream" both
	// mean the browser didn't have a real answer — worth a second try via
	// extension before falling all the way back to rejecting the upload.
	// mime.TypeByExtension alone isn't enough for .md/.yaml/.yml: they're
	// not in Go's built-in table and often missing from a container's
	// /etc/mime.types too (confirmed: absent on this dev machine),
	// unlike .txt/.json/.csv/.xml, which mime.TypeByExtension already
	// knows without any override.
	if contentType == "" || contentType == "application/octet-stream" {
		ext := filepath.Ext(header.Filename)
		if guessed := mime.TypeByExtension(ext); guessed != "" {
			contentType = guessed
		} else if overridden := extensionContentTypeOverride(ext); overridden != "" {
			contentType = overridden
		}
	}
	parsedType, _, parseErr := mime.ParseMediaType(firstNonEmpty(contentType, "application/octet-stream"))
	if parseErr != nil {
		log.Warn("parsing upload content type failed", "filename", header.Filename, "raw_content_type", contentType, "err", parseErr)
		s.db.LogEvent("", "warn", "upload", "parsing content type failed", map[string]interface{}{"filename": header.Filename, "err": parseErr.Error()}, "")
		return UploadResponse{}, &uploadError{http.StatusBadRequest, fmt.Sprintf("couldn't parse content type %q", contentType)}
	}
	contentType = parsedType
	if !allowedUploadContentType(contentType) {
		return UploadResponse{}, &uploadError{http.StatusBadRequest,
			fmt.Sprintf("unsupported content type %q — only PDFs, images, and common text/data formats "+
				"(.md/.txt/.json/.csv/.yaml/.xml) are accepted", contentType)}
	}

	if err := os.MkdirAll(cfg.Attachments.Dir, 0o755); err != nil {
		log.Warn("creating attachments dir failed", "dir", cfg.Attachments.Dir, "err", err)
		s.db.LogEvent("", "error", "upload", "creating attachments dir failed", map[string]interface{}{"dir": cfg.Attachments.Dir, "err": err.Error()}, "")
		return UploadResponse{}, &uploadError{http.StatusInternalServerError, "server storage error"}
	}

	id := uuid.NewString()
	destPath := filepath.Join(cfg.Attachments.Dir, id)
	dest, err := os.Create(destPath)
	if err != nil {
		log.Warn("creating attachment file failed", "path", destPath, "err", err)
		s.db.LogEvent("", "error", "upload", "creating attachment file failed", map[string]interface{}{"err": err.Error()}, "")
		return UploadResponse{}, &uploadError{http.StatusInternalServerError, "server storage error"}
	}
	defer dest.Close()

	written, err := io.Copy(dest, io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		dest.Close()
		if rmErr := os.Remove(destPath); rmErr != nil {
			log.Warn("cleaning up partial attachment failed", "path", destPath, "err", rmErr)
		}
		log.Warn("writing attachment file failed", "path", destPath, "err", err)
		s.db.LogEvent("", "error", "upload", "writing attachment file failed", map[string]interface{}{"err": err.Error()}, "")
		return UploadResponse{}, &uploadError{http.StatusInternalServerError, "server storage error"}
	}
	if written > maxUploadBytes {
		dest.Close()
		if rmErr := os.Remove(destPath); rmErr != nil {
			log.Warn("cleaning up oversized attachment failed", "path", destPath, "err", rmErr)
		}
		return UploadResponse{}, &uploadError{http.StatusRequestEntityTooLarge, "attachment too large (max 100MB)"}
	}

	log.Info("attachment uploaded", "id", id, "filename", header.Filename, "content_type", contentType, "size_bytes", written)
	s.db.LogEvent("", "info", "upload", "attachment uploaded", map[string]interface{}{
		"id": id, "filename": header.Filename, "content_type": contentType, "size_bytes": written,
	}, "")
	return UploadResponse{ID: id, Filename: header.Filename, ContentType: contentType, SizeBytes: written}, nil
}

// remoteImageDialContext is a var, not a direct reference to
// tools.SafeDialContext, so tests can point it at a plain, unrestricted
// dialer for a loopback httptest.Server — same pattern as
// tools/web_read.go's own dialContext var. Real Pulsar Daily traffic
// always targets a public image URL surfaced by image_search, which is
// exactly as attacker-influenceable as anything web_read fetches, so it
// needs the same SSRF/DNS-rebinding-aware guard rather than
// http.DefaultClient's unrestricted dialer — without this, a crafted
// search-result image URL pointing at an internal host (a cloud metadata
// endpoint, a LAN admin panel) would get fetched, saved to disk, and
// handed back as a normal attachment.
var remoteImageDialContext = tools.SafeDialContext

// fetchImageURLBytes is a var (not a plain function call) so tests can
// stub the network fetch — same pattern as search.nominatimBaseURL and
// web_read.go's waybackAvailabilityAPI.
var fetchImageURLBytes = func(reqCtx context.Context, url string) (data []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: remoteImageDialContext},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetching image: status %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxUploadBytes {
		return nil, "", fmt.Errorf("image too large (max 100MB)")
	}
	contentType = resp.Header.Get("Content-Type")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	return data, strings.TrimSpace(contentType), nil
}

// saveRemoteImageAttachment downloads imageURL and saves it to
// config.Attachments.Dir exactly like a normal upload (saveUploadedFile)
// — same directory, same generated-UUID naming, same content-type gate —
// so it flows through the ordinary attachment-reference/resolveAttachments/vision
// pipeline indistinguishably from a file the user picked themselves. Built
// for Pulsar Daily's Picture of the Day expand-to-chat: the block's image
// lives at a remote URL (an image_search result), not a local upload, but
// the seeded turn needs the model to actually see it, not just read a
// caption — see gateway/pulsar_daily_routes.go's handleExpandDailyBlock.
func (s *Server) saveRemoteImageAttachment(reqCtx context.Context, imageURL string) (UploadResponse, error) {
	cfg := s.liveConfig()

	data, contentType, err := fetchImageURLBytes(reqCtx, imageURL)
	if err != nil {
		return UploadResponse{}, fmt.Errorf("fetching image: %w", err)
	}
	if !strings.HasPrefix(contentType, "image/") {
		return UploadResponse{}, fmt.Errorf("fetched content isn't an image (got %q)", contentType)
	}

	if err := os.MkdirAll(cfg.Attachments.Dir, 0o755); err != nil {
		return UploadResponse{}, fmt.Errorf("creating attachments dir: %w", err)
	}
	id := uuid.NewString()
	destPath := filepath.Join(cfg.Attachments.Dir, id)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return UploadResponse{}, fmt.Errorf("writing attachment file: %w", err)
	}

	filename := "picture-of-the-day" + extensionForContentType(contentType)
	log.Info("pulsar daily: saved remote image as attachment", "id", id, "content_type", contentType, "size_bytes", len(data))
	return UploadResponse{ID: id, Filename: filename, ContentType: contentType, SizeBytes: int64(len(data))}, nil
}

// extensionForContentType is a small, deliberately incomplete map — just
// enough for the image types image_search actually returns — not a
// general MIME-to-extension resolver. Falls back to no extension at all
// rather than guessing wrong; the extension is cosmetic (display filename
// only), never used to pick a decoder.
func extensionForContentType(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}

// extensionContentTypeOverride covers extensions mime.TypeByExtension
// doesn't reliably know — Go's built-in table and a typical container's
// /etc/mime.types both lack .md/.yaml/.yml, unlike .txt/.json/.csv/.xml,
// which already resolve correctly without this. Only used as a second
// fallback in saveUploadedFile, after both the browser's own guess and
// mime.TypeByExtension have already come up empty/generic.
func extensionContentTypeOverride(ext string) string {
	switch strings.ToLower(ext) {
	case ".md":
		return "text/markdown"
	case ".yaml", ".yml":
		return "application/x-yaml"
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// shortFileIDAlphabet excludes visually-ambiguous characters (0/O, 1/l/I)
// — this ID is meant to be reliably reproduced by a model in a tool call
// (see attachWorkspaceFilePattern's doc comment), not just machine-read.
const shortFileIDAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// shortFileIDLength is short enough for a model to reproduce in a tool
// call without transcription errors (the design goal — see
// docs/plans/workspace-store-unification.md's "Per-file identity"), while
// keeping collisions negligible: 32^10 possibilities against a single
// operator's realistic upload volume.
const shortFileIDLength = 10

// generateShortFileID mints this upload's on-disk addressing ID — the
// same idea as a thread ID (store.go's uuid.NewString()) but deliberately
// shorter, since a model has to type this one back verbatim in a tool
// call's arguments rather than just carrying it through opaque plumbing.
func generateShortFileID() (string, error) {
	buf := make([]byte, shortFileIDLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := make([]byte, shortFileIDLength)
	for i, b := range buf {
		id[i] = shortFileIDAlphabet[int(b)%len(shortFileIDAlphabet)]
	}
	return string(id), nil
}

// extensionForUpload prefers the original filename's own extension (the
// common case — "report.pdf" already carries one) and only falls back to
// content-type sniffing for the rare upload with none, e.g. a pasted
// image the browser named without one.
func extensionForUpload(filename, contentType string) string {
	if ext := filepath.Ext(filename); ext != "" {
		return ext
	}
	if contentType == "application/pdf" {
		return ".pdf"
	}
	return extensionForContentType(contentType)
}

// resolveOneAttachment moves one staged upload (written by
// saveUploadedFile into cfg.Attachments.Dir under an opaque upload ID)
// into this thread's persistent code_exec workspace directory, renamed to
// a short generated ID plus its extension — the same durable,
// never-deleted directory fetch_url/code_exec already write into (see
// tools/code_exec.go's workspaceDir), and the same exact filename GET
// /api/workspace/:thread_id/:filename (gateway/workspace.go) serves — so
// the filename this returns is directly usable as a download link, not
// just a tool-call reference. This supersedes the old
// single-read-then-deleted lifecycle entirely: no PDF text extraction, no
// vision-model description call, no removeAttachmentFile. The model only
// pays to actually look at the file (via code_exec) when it decides to,
// not eagerly on every turn it's attached — see
// docs/plans/workspace-store-unification.md's "How the model finds out a
// file exists".
//
// threadID is storageThreadID from handleTurn — the workspace directory a
// short-ID upload lands in must be the same one code_exec/fetch_url mount
// for this thread, not the client-facing thread id a fork briefly diverges
// from.
func resolveOneAttachment(cfg *config.Config, ref AttachmentRef, threadID string) (workspaceFilename string, err error) {
	// ref.ID becomes a filesystem path component (see handleUpload, which
	// only ever names files with uuid.NewString()) — validate it's
	// actually a UUID before joining it into a path, rather than trusting
	// whatever a client sends here.
	if _, err := uuid.Parse(ref.ID); err != nil {
		return "", fmt.Errorf("invalid attachment id: %w", err)
	}

	shortID, err := generateShortFileID()
	if err != nil {
		return "", fmt.Errorf("generating file id: %w", err)
	}
	ext := extensionForUpload(ref.Filename, ref.ContentType)
	filename := shortID + ext

	// Same 0o777-plus-explicit-Chmod dance as code_exec.go/fetch_url.go's
	// identical workspace-directory creation: this process and the
	// host-side codeexec.sh script that later mounts this same directory
	// run as different uids, and MkdirAll's requested mode alone gets
	// silently stripped back down by the process umask.
	workspaceDir := filepath.Join(cfg.CodeExec.WorkspaceDir, threadID)
	if err := os.MkdirAll(workspaceDir, 0o777); err != nil {
		return "", fmt.Errorf("preparing workspace directory: %w", err)
	}
	if err := os.Chmod(workspaceDir, 0o777); err != nil {
		return "", fmt.Errorf("setting workspace directory permissions: %w", err)
	}

	srcPath := filepath.Join(cfg.Attachments.Dir, ref.ID)
	destPath := filepath.Join(workspaceDir, filename)
	if err := moveUploadedFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("moving upload into workspace: %w", err)
	}

	return filename, nil
}

// resolveAttachments turns a ClientMessage carrying zero or more uploads
// (issue #71 — multiple attachments per turn) into the text agent.Run
// should actually see, plus the resolved attachments to persist via
// store.SetMessageAttachments. msg.Content is returned unchanged (and
// resolved is nil) when there are no attachments. Called once per turn
// from handleTurn, before agent.Run.
//
// A single attachment failing to resolve (bad ID, missing file on disk,
// workspace directory unwritable) is reported to onError — so the caller
// can log/record it with its own turn context — but doesn't drop the
// others or fail the whole turn, the same tolerance handleTurn already
// had for one attachment before this went multi-valued.
func resolveAttachments(cfg *config.Config, msg ClientMessage, threadID string, onError func(ref AttachmentRef, err error)) (content string, resolved []store.Attachment) {
	content = msg.Content
	for _, ref := range msg.Attachments {
		filename, err := resolveOneAttachment(cfg, ref, threadID)
		if err != nil {
			if onError != nil {
				onError(ref, err)
			}
			continue
		}
		content += fmt.Sprintf("\n\n[A file has been included named %s. Read it if relevant to answering this question.]", filename)
		resolved = append(resolved, store.Attachment{Filename: ref.Filename, ContentType: ref.ContentType, WorkspaceFileID: filename})
	}
	return content, resolved
}

// moveUploadedFile relocates an uploaded file from the upload staging area
// into a thread's workspace directory. Not a plain os.Rename: cfg.Attachments.Dir
// and cfg.CodeExec.WorkspaceDir aren't guaranteed to be on the same
// filesystem/volume (see docker-compose.yml's separate bind mounts), and a
// cross-device os.Rename fails outright with EXDEV — copy-then-remove works
// regardless of mount topology, at the cost of buffering the file once,
// which is already bounded by maxUploadBytes.
func moveUploadedFile(srcPath, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dest, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dest, src); err != nil {
		dest.Close()
		os.Remove(destPath)
		return err
	}
	if err := dest.Close(); err != nil {
		os.Remove(destPath)
		return err
	}
	return os.Remove(srcPath)
}

// visionClient picks which model actually describes/sees an image: the
// thread's own selected model if it's multimodal itself, otherwise
// cfg.MultimodalModel()'s configured fallback. Shared by resolveAttachment
// (upload-time description) and tools/view_image.go (on-demand describe/see
// calls) so both paths pick the same model the same way, rather than two
// copies of this logic drifting apart. ok is false only when neither the
// selected model nor any configured fallback is multimodal — there's
// nothing this can do about that, the caller decides how to fail.
//
// Deliberately NOT pinned to visionModel.Provider the way the main chat
// client pins its provider — that pin exists for prompt-cache consistency
// across an ongoing conversation, which doesn't apply to this single
// one-off call, and it actively hurts here: found live that the pinned
// "xiaomi/fp8" endpoint 404s with "No endpoints found that support image
// input" even though the model itself is vision-capable — some
// provider-specific deployments of a multimodal model quietly drop vision
// support. Leaving provider routing open lets OpenRouter pick whichever
// endpoint actually handles image input for this model.
func visionClient(cfg *config.Config, selectedModel config.ModelConfig) (client *llm.Client, ok bool) {
	visionModel := selectedModel
	if !visionModel.Multimodal {
		var found bool
		visionModel, found = cfg.MultimodalModel()
		if !found {
			return nil, false
		}
	}
	return llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, visionModel.Model, visionModel.Temperature, visionModel.MaxTokens), true
}

// PruneOldAttachments removes files under dir older than maxAge — a safety
// net for uploads written to disk by handleUpload but never actually sent
// in a message (the user picked a file then removed it before hitting
// send, or closed the tab first). Those have no other cleanup path, since
// nothing persists a reference to an attachment until it's actually used
// in a turn (see removeAttachmentFile). maxAge should be generous enough
// that a slow upload-then-send never risks colliding with this — called
// once at startup, not on a timer, so "generous" costs nothing.
func PruneOldAttachments(dir string, maxAge time.Duration) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			log.Warn("stat'ing attachment during prune failed", "name", e.Name(), "err", err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				log.Warn("pruning old attachment failed", "name", e.Name(), "err", err)
			}
		}
	}
	return nil
}
