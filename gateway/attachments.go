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
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"polaris/config"
	"polaris/llm"
	"polaris/tools"
)

// maxUploadBytes caps a single attachment — generous for a PDF or photo,
// tight enough that a stray huge upload can't fill the potato's disk.
const maxUploadBytes = 20 << 20

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
	cfg := s.liveConfig()

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20) // +1MiB slack for multipart overhead/headers
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "invalid or too-large upload (max 20MB): "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing \"file\" field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(header.Filename))
	}
	contentType, _, _ = mime.ParseMediaType(firstNonEmpty(contentType, "application/octet-stream"))
	if !allowedUploadContentType(contentType) {
		http.Error(w, fmt.Sprintf("unsupported content type %q — only PDFs and images are accepted", contentType), http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(cfg.Attachments.Dir, 0o755); err != nil {
		log.Warn("creating attachments dir failed", "dir", cfg.Attachments.Dir, "err", err)
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}

	id := uuid.NewString()
	destPath := filepath.Join(cfg.Attachments.Dir, id)
	dest, err := os.Create(destPath)
	if err != nil {
		log.Warn("creating attachment file failed", "path", destPath, "err", err)
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	written, err := io.Copy(dest, io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		os.Remove(destPath)
		log.Warn("writing attachment file failed", "path", destPath, "err", err)
		http.Error(w, "server storage error", http.StatusInternalServerError)
		return
	}
	if written > maxUploadBytes {
		os.Remove(destPath)
		http.Error(w, "attachment too large (max 20MB)", http.StatusRequestEntityTooLarge)
		return
	}

	log.Info("attachment uploaded", "id", id, "filename", header.Filename, "content_type", contentType, "size_bytes", written)
	writeJSON(w, UploadResponse{ID: id, Filename: header.Filename, ContentType: contentType, SizeBytes: written})
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
func resolveAttachment(ctx context.Context, cfg *config.Config, msg ClientMessage) (content string, costUSD float64, err error) {
	if msg.AttachmentID == "" {
		return msg.Content, 0, nil
	}

	// AttachmentID becomes a filesystem path component (see handleUpload,
	// which only ever names files with uuid.NewString()) — validate it's
	// actually a UUID before joining it into a path, rather than trusting
	// whatever a client sends here.
	if _, err := uuid.Parse(msg.AttachmentID); err != nil {
		return msg.Content, 0, fmt.Errorf("invalid attachment id: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(cfg.Attachments.Dir, msg.AttachmentID))
	if err != nil {
		return msg.Content, 0, fmt.Errorf("reading attachment: %w", err)
	}

	filename := msg.AttachmentFilename
	if filename == "" {
		filename = "attachment"
	}

	switch {
	case msg.AttachmentContentType == "application/pdf":
		_, text, err := tools.ExtractPDFText(data)
		if err != nil {
			return msg.Content, 0, fmt.Errorf("extracting pdf text: %w", err)
		}
		return fmt.Sprintf("%s\n\n[Attached file: %s]\n%s", msg.Content, filename, text), 0, nil

	case strings.HasPrefix(msg.AttachmentContentType, "image/"):
		visionModel, ok := cfg.MultimodalModel()
		if !ok {
			return msg.Content, 0, fmt.Errorf("no multimodal model configured to describe images")
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
		description, cost, err := client.DescribeImage(ctx, base64.StdEncoding.EncodeToString(data), msg.AttachmentContentType)
		if err != nil {
			return msg.Content, 0, fmt.Errorf("describing image: %w", err)
		}
		return fmt.Sprintf("%s\n\n[Attached image: %s]\nImage description (the model itself can't see "+
			"the image — this description is all it has to go on): %s", msg.Content, filename, description), cost, nil

	default:
		// Upload-time validation (handleUpload) only ever accepts PDF or
		// image/*, so this shouldn't be reachable — but fail safe rather
		// than silently drop an attachment type added there later without
		// a matching case here.
		return msg.Content, 0, fmt.Errorf("no extraction pipeline for content type %q", msg.AttachmentContentType)
	}
}
