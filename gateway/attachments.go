// attachments.go handles file uploads from the composer's "+" menu
// (see web/src/lib/components/ComposerMenu.svelte) — a separate REST
// call ahead of the WebSocket message, same two-step shape as push-to-talk
// voice memos (POST /api/transcribe, then the transcribed text rides
// along in the next ClientMessage). Here, the upload returns an opaque
// ID; the frontend sends that ID as ClientMessage.AttachmentID, and
// handleTurn resolves it back to a file on disk — never a path the
// client supplies directly.
package gateway

import (
	"context"
	"encoding/base64"
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
	"polaris/tools"
)

// maxUploadBytes caps a single attachment. Raised from an original 20MB
// (found live: a real ~27MB multi-hundred-page PDF model card — exactly
// the "massive PDF" case read_attachment exists for — bounced off the old
// cap) to 100MB, still a real ceiling rather than unbounded, since the
// whole file gets buffered into memory to parse (see saveUploadedFile and
// tools.ExtractPDFText/pdfPageText) on hardware as constrained as the
// potato.
const maxUploadBytes = 100 << 20

// allowedUploadContentType accepts exactly what ComposerMenu's file input
// offers (accept="image/*,.pdf") — anything else is almost certainly a
// mistake or a client someone's about to abuse, not the human uploading
// a photo or document.
func allowedUploadContentType(ct string) bool {
	return ct == "application/pdf" || strings.HasPrefix(ct, "image/")
}

// UploadResponse is what POST /api/upload returns — ID is what the
// frontend echoes back as ClientMessage.AttachmentID.
type UploadResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// handleUpload accepts a multipart/form-data POST with a single "file"
// field, saves it to config.Attachments.Dir under a generated name (never
// the client-supplied filename — that's only kept for display, see
// store.Message.AttachmentFilename), and returns its ID.
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
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(header.Filename))
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
			fmt.Sprintf("unsupported content type %q — only PDFs and images are accepted", contentType)}
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveAttachment turns a ClientMessage carrying an upload into the
// text agent.Run should actually see — msg.Content unchanged if there's
// no attachment, otherwise msg.Content with the attachment's extracted
// content (PDF text, or an image's description) appended. Called once
// per turn from handleTurn, before agent.Run. costUSD is only ever
// nonzero for an image (the vision-model description call has a real
// cost); the caller folds it into the turn's total the same way a voice
// memo's transcription cost already is.
//
// selectedModel is the model the thread is actually about to talk to —
// when it's multimodal itself, it describes its own image rather than
// deferring to cfg.MultimodalModel()'s fixed pick. Without this, adding a
// second multimodal model to the registry (config.ModelConfig.Multimodal)
// would silently never be used for this: MultimodalModel() always returns
// the first Multimodal entry it finds, regardless of what's selected.
//
// emit, if non-nil, is handleTurn's same event-streaming closure the rest
// of a turn uses for real tool calls — an image description is a "blank
// screen" moment otherwise: it runs entirely before agent.Run even starts,
// so nothing was ever visible on the frontend while it happened, often for
// several seconds. Wrapping the vision-model call in a synthetic
// tool_call/tool_result pair (tool name "describe_image") makes it show up
// on the timeline exactly like a real tool call, description included, so
// there's finally something to look at instead of a blank wait. Nil is
// fine (and used by tests below) — this attachment path also runs from
// contexts with no live streaming connection to write to.
//
// attachmentData is only ever non-nil for a PDF — handleTurn threads it
// into tools.Context.AttachmentData so the read_attachment tool can page
// through or search the full document beyond the preview embedded in
// content below, for the one turn this attachment was uploaded on (never
// persisted past that — see AttachmentData's doc comment). Nil for every
// other case: no attachment, an image (already fully described here, no
// pagination story of its own), or any error path.
func resolveAttachment(ctx context.Context, cfg *config.Config, selectedModel config.ModelConfig, msg ClientMessage, emit func(eventType string, payload map[string]interface{})) (content string, attachmentData []byte, costUSD float64, err error) {
	if msg.AttachmentID == "" {
		return msg.Content, nil, 0, nil
	}

	// AttachmentID becomes a filesystem path component (see handleUpload,
	// which only ever names files with uuid.NewString()) — validate it's
	// actually a UUID before joining it into a path, rather than trusting
	// whatever a client sends here.
	if _, err := uuid.Parse(msg.AttachmentID); err != nil {
		return msg.Content, nil, 0, fmt.Errorf("invalid attachment id: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(cfg.Attachments.Dir, msg.AttachmentID))
	if err != nil {
		return msg.Content, nil, 0, fmt.Errorf("reading attachment: %w", err)
	}

	filename := msg.AttachmentFilename
	if filename == "" {
		filename = "attachment"
	}

	switch {
	case msg.AttachmentContentType == "application/pdf":
		_, text, totalPages, truncated, err := tools.ExtractPDFText(data)
		if err != nil {
			return msg.Content, nil, 0, fmt.Errorf("extracting pdf text: %w", err)
		}
		note := ""
		if truncated {
			note = fmt.Sprintf("\n\n... [truncated — %d pages total; call the read_attachment tool with a page number "+
				"to keep reading, or a query to search the full document]", totalPages)
		} else if totalPages > 1 {
			note = fmt.Sprintf("\n\n[%d pages total — call the read_attachment tool with a page number or a query "+
				"if you need to revisit something specific]", totalPages)
		}
		return fmt.Sprintf("%s\n\n[Attached file: %s]\n%s%s", msg.Content, filename, text, note), data, 0, nil

	case strings.HasPrefix(msg.AttachmentContentType, "image/"):
		visionModel := selectedModel
		if !visionModel.Multimodal {
			var ok bool
			visionModel, ok = cfg.MultimodalModel()
			if !ok {
				return msg.Content, nil, 0, fmt.Errorf("no multimodal model configured to describe images")
			}
		}
		// Deliberately NOT pinned to visionModel.Provider the way the main
		// chat client pins its provider — that pin exists for prompt-cache
		// consistency across an ongoing conversation, which doesn't apply
		// to this single one-off call, and it actively hurts here: found
		// live that the pinned "xiaomi/fp8" endpoint 404s with "No
		// endpoints found that support image input" even though the model
		// itself is vision-capable — some provider-specific deployments of
		// a multimodal model quietly drop vision support. Leaving provider
		// routing open lets OpenRouter pick whichever endpoint actually
		// handles image input for this model.
		client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, visionModel.Model, visionModel.Temperature, visionModel.MaxTokens)
		if emit != nil {
			emit("tool_call", map[string]interface{}{
				"tool": "describe_image",
				"args": map[string]interface{}{"filename": filename},
			})
		}
		description, cost, err := client.DescribeImage(ctx, base64.StdEncoding.EncodeToString(data), msg.AttachmentContentType)
		if err != nil {
			if emit != nil {
				emit("tool_result", map[string]interface{}{"tool": "describe_image", "result": "error: " + err.Error()})
			}
			return msg.Content, nil, 0, fmt.Errorf("describing image: %w", err)
		}
		if emit != nil {
			emit("tool_result", map[string]interface{}{"tool": "describe_image", "result": description})
		}
		return fmt.Sprintf("%s\n\n[Attached image: %s]\nImage description (the model itself can't see "+
			"the image — this description is all it has to go on): %s", msg.Content, filename, description), nil, cost, nil

	default:
		// Upload-time validation (handleUpload) only ever accepts PDF or
		// image/*, so this shouldn't be reachable — but fail safe rather
		// than silently drop an attachment type added there later without
		// a matching case here.
		return msg.Content, nil, 0, fmt.Errorf("no extraction pipeline for content type %q", msg.AttachmentContentType)
	}
}

// removeAttachmentFile deletes an uploaded attachment's file from disk once
// it's been consumed by resolveAttachment — nothing else ever reads the raw
// file again afterward. The messages table only ever stores the display
// filename/content-type (see SetMessageAttachment), never the file's actual
// disk name, so this is the one and only point where a file can be tied
// back to cleanup; without it, every attachment ever sent stayed on disk
// forever. Called regardless of whether resolveAttachment succeeded — a
// failed extraction still means the file was fully consumed for this turn,
// not that it'll be retried later.
func removeAttachmentFile(cfg *config.Config, attachmentID string) {
	if _, err := uuid.Parse(attachmentID); err != nil {
		return // never a valid attachment id to begin with — nothing to remove
	}
	path := filepath.Join(cfg.Attachments.Dir, attachmentID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Warn("removing attachment file after use failed", "path", path, "err", err)
	}
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
