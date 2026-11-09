package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

// SniffBytes is how much of an upload is inspected to determine its real type.
const SniffBytes = 512

// blockedTypes are always quarantined, regardless of what the client declared.
// They are executables or scripts that must never be served back.
var blockedTypes = map[string]bool{
	"application/x-msdownload":  true,
	"application/x-dosexec":     true,
	"application/x-elf":         true,
	"application/x-executable":  true,
	"application/x-sh":          true,
	"application/x-shellscript": true,
	"text/x-shellscript":        true,
	"application/x-php":         true,
	"text/x-php":                true,
}

// jobPayload is shared by the validation and thumbnail tasks.
type jobPayload struct {
	AttachmentID uuid.UUID `json:"attachment_id"`
	OrgID        uuid.UUID `json:"org_id"`
}

// TaskHandler consumes attachment:validate and thumbnail:generate tasks. The
// thumbnail task runs on the CPU queue; validation runs on the I/O queue.
type TaskHandler struct {
	repo   Store
	store  storage.Backend
	logger *slog.Logger
	maxDim int
}

// NewTaskHandler builds the attachment task handler. maxDim is the longest
// edge of a generated thumbnail.
func NewTaskHandler(repo Store, store storage.Backend, maxDim int, logger *slog.Logger) *TaskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if maxDim <= 0 {
		maxDim = 512
	}
	return &TaskHandler{repo: repo, store: store, logger: logger, maxDim: maxDim}
}

// Validate sniffs the first bytes of an uploaded object. A type that does not
// match the declaration, or an executable/script signature, quarantines the
// attachment.
func (h *TaskHandler) Validate(ctx context.Context, task *asynq.Task) error {
	payload, err := decodeJobPayload(task)
	if err != nil {
		return queue.Permanent(err)
	}
	att, err := h.repo.GetAttachment(ctx, payload.OrgID, payload.AttachmentID)
	if err != nil {
		// A deleted attachment is a permanent no-op.
		return queue.Permanent(err)
	}
	if att.UploadStatus != StatusUploaded {
		return nil
	}

	head, err := h.store.GetRange(ctx, att.StorageKey, SniffBytes)
	if err != nil {
		if err == storage.ErrObjectNotFound {
			if qerr := h.repo.MarkQuarantined(ctx, att.OrgID, att.ID, "object_missing"); qerr != nil {
				return qerr
			}
			h.logger.Warn("attachment object missing during validation", "attachment_id", att.ID)
			return nil
		}
		return err
	}
	detected, err := DetectContent(head)
	if err != nil {
		if qerr := h.repo.MarkQuarantined(ctx, att.OrgID, att.ID, "unreadable"); qerr != nil {
			return qerr
		}
		return nil
	}

	reason := QuarantineReason(att.MIMEType, detected)
	if reason != "" {
		if err := h.repo.MarkQuarantined(ctx, att.OrgID, att.ID, reason); err != nil {
			return err
		}
		h.logger.Warn("attachment quarantined",
			"attachment_id", att.ID,
			"declared", att.MIMEType,
			"detected", detected,
			"reason", reason,
		)
	}
	return nil
}

// Thumbnail decodes an uploaded image, resizes it to a bounded edge and
// stores the result under a thumbnail prefix. It runs on the CPU queue.
func (h *TaskHandler) Thumbnail(ctx context.Context, task *asynq.Task) error {
	payload, err := decodeJobPayload(task)
	if err != nil {
		return queue.Permanent(err)
	}
	att, err := h.repo.GetAttachment(ctx, payload.OrgID, payload.AttachmentID)
	if err != nil {
		return queue.Permanent(err)
	}
	if att.UploadStatus != StatusUploaded || !IsImage(att.MIMEType) {
		return nil
	}

	reader, _, err := h.store.Open(ctx, att.StorageKey)
	if err != nil {
		return err
	}
	defer reader.Close()

	img, err := imaging.Decode(reader)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	thumb := imaging.Fit(img, h.maxDim, h.maxDim, imaging.Lanczos)

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, thumb, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return fmt.Errorf("encode thumbnail: %w", err)
	}

	key := ThumbnailKey(att.OrgID, att.TicketID, att.ID)
	if err := h.store.Put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "image/jpeg"); err != nil {
		return err
	}
	if err := h.repo.SetThumbnail(ctx, att.OrgID, att.ID, key); err != nil {
		return err
	}
	h.logger.Info("thumbnail generated", "attachment_id", att.ID, "bytes", buf.Len())
	return nil
}

// ThumbnailKey builds the server-generated thumbnail object key.
func ThumbnailKey(orgID, ticketID, attachmentID uuid.UUID) string {
	return fmt.Sprintf("org/%s/tickets/%s/thumbnails/%s.jpg", orgID, ticketID, attachmentID)
}

// DetectContent returns the media type inferred from the leading bytes.
func DetectContent(head []byte) (string, error) {
	if len(head) == 0 {
		return "", fmt.Errorf("attachments: empty object")
	}
	detected := http.DetectContentType(head)
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}
	return strings.ToLower(strings.TrimSpace(detected)), nil
}

// QuarantineReason returns a non-empty reason when the declared type is not
// compatible with the detected type, or when either is an executable/script.
func QuarantineReason(declared, detected string) string {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if blockedTypes[declared] {
		return "declared executable type"
	}
	if blockedTypes[detected] {
		return "detected executable type"
	}
	if compatibleTypes(declared, detected) {
		return ""
	}
	return fmt.Sprintf("type mismatch: declared %s, detected %s", declared, detected)
}

// compatibleTypes treats equivalent container families as a match. Office
// documents are ZIP containers; CSV is plain text on the wire.
func compatibleTypes(declared, detected string) bool {
	if declared == detected {
		return true
	}
	switch declared {
	case "text/csv", "text/plain":
		return detected == "text/plain"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel",
		"application/msword",
		"application/zip":
		return detected == "application/zip"
	case "video/mp4":
		return detected == "video/mp4" || detected == "application/octet-stream"
	}
	return false
}

func decodeJobPayload(task *asynq.Task) (jobPayload, error) {
	var p jobPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return jobPayload{}, fmt.Errorf("decode attachment payload: %w", err)
	}
	if p.AttachmentID == uuid.Nil || p.OrgID == uuid.Nil {
		return jobPayload{}, fmt.Errorf("attachment payload is incomplete")
	}
	return p, nil
}
