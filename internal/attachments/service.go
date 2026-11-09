package attachments

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

// Enqueuer enqueues follow-up work after an upload is confirmed.
type Enqueuer interface {
	EnqueueJob(ctx context.Context, job queue.Job) error
}

// Service implements the attachment lifecycle.
type Service struct {
	repo               Store
	store              storage.Backend
	enqueuer           Enqueuer
	maxBytes           int
	presignPutTTL      time.Duration
	presignDownloadTTL time.Duration
	pendingTTL         time.Duration
	now                func() time.Time
}

// NewService wires the attachment service.
func NewService(repo Store, store storage.Backend, maxBytes int, presignPutTTL, presignDownloadTTL, pendingTTL time.Duration) *Service {
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if presignPutTTL <= 0 {
		presignPutTTL = 5 * time.Minute
	}
	if presignDownloadTTL <= 0 {
		presignDownloadTTL = 2 * time.Minute
	}
	if pendingTTL <= 0 {
		pendingTTL = time.Hour
	}
	return &Service{
		repo:               repo,
		store:              store,
		maxBytes:           maxBytes,
		presignPutTTL:      presignPutTTL,
		presignDownloadTTL: presignDownloadTTL,
		pendingTTL:         pendingTTL,
		now:                time.Now,
	}
}

// SetEnqueuer wires the queue used for validation and thumbnail jobs.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

// Presign validates metadata, creates a pending attachment and returns a
// short-lived upload URL. No object is written by the API.
func (s *Service) Presign(ctx context.Context, orgID uuid.UUID, ticketNumber int64, req PresignRequest) (PresignResponse, error) {
	filename, err := SanitizeFilename(req.Filename)
	if err != nil {
		return PresignResponse{}, err
	}
	mediaType, err := NormalizeMIMEType(req.MIMEType)
	if err != nil {
		return PresignResponse{}, err
	}
	if req.Size <= 0 {
		return PresignResponse{}, ErrInvalidFile
	}
	if req.Size > int64(s.maxBytes) {
		return PresignResponse{}, ErrFileTooLarge
	}

	ticketID, err := s.repo.GetTicketIDByNumber(ctx, orgID, ticketNumber)
	if err != nil {
		return PresignResponse{}, err
	}

	objectKey := ObjectKey(orgID, ticketID, uuid.New())
	expiresAt := s.now().UTC().Add(s.pendingTTL)
	att, err := s.repo.InsertAttachment(ctx, Attachment{
		OrgID:      orgID,
		TicketID:   ticketID,
		StorageKey: objectKey,
		Filename:   filename,
		MIMEType:   mediaType,
		SizeBytes:  req.Size,
		ExpiresAt:  &expiresAt,
	})
	if err != nil {
		return PresignResponse{}, err
	}

	url, err := s.store.PresignPut(ctx, objectKey, s.presignPutTTL)
	if err != nil {
		return PresignResponse{}, err
	}
	return PresignResponse{
		AttachmentID: att.ID.String(),
		UploadURL:    url,
		ExpiresAt:    s.now().UTC().Add(s.presignPutTTL),
	}, nil
}

// Confirm verifies that the declared object landed with the expected size and
// moves the attachment to uploaded. Confirming twice is idempotent.
func (s *Service) Confirm(ctx context.Context, orgID, id uuid.UUID) (Attachment, error) {
	att, err := s.repo.GetAttachment(ctx, orgID, id)
	if err != nil {
		return Attachment{}, err
	}
	switch att.UploadStatus {
	case StatusUploaded:
		return att, nil
	case StatusQuarantined:
		return Attachment{}, ErrQuarantined
	case StatusDeleted:
		return Attachment{}, ErrAttachmentNotFound
	}

	info, err := s.store.Head(ctx, att.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			return Attachment{}, ErrUploadNotFound
		}
		return Attachment{}, err
	}
	if info.Size != att.SizeBytes {
		return Attachment{}, ErrSizeMismatch
	}

	updated, err := s.repo.MarkUploaded(ctx, orgID, id)
	if err != nil {
		return Attachment{}, err
	}
	if info.ETag != "" {
		_ = s.repo.SetChecksum(ctx, orgID, id, info.ETag)
		updated.Checksum = info.ETag
	}

	s.enqueueAfterUpload(ctx, updated)
	return updated, nil
}

func (s *Service) enqueueAfterUpload(ctx context.Context, att Attachment) {
	if s.enqueuer == nil {
		return
	}
	payload := map[string]any{
		"attachment_id": att.ID.String(),
		"org_id":        att.OrgID.String(),
	}
	// Every upload is content-sniffed; only images get a thumbnail job.
	if task, err := queue.NewTask(queue.TaskAttachmentValidate, payload); err == nil {
		_ = s.enqueuer.EnqueueJob(ctx, queue.Job{
			ID:       queue.TaskAttachmentValidate + ":" + att.ID.String(),
			Type:     queue.TaskAttachmentValidate,
			Queue:    queue.QueueDefault,
			Payload:  task.Payload(),
			MaxRetry: 5,
		})
	}
	if IsImage(att.MIMEType) {
		if task, err := queue.NewTask(queue.TaskThumbnailGenerate, payload); err == nil {
			_ = s.enqueuer.EnqueueJob(ctx, queue.Job{
				ID:       queue.TaskThumbnailGenerate + ":" + att.ID.String(),
				Type:     queue.TaskThumbnailGenerate,
				Queue:    queue.QueueCPU,
				Payload:  task.Payload(),
				MaxRetry: 3,
			})
		}
	}
}

// Download returns a short-lived signed URL for an uploaded attachment.
func (s *Service) Download(ctx context.Context, orgID, id uuid.UUID) (DownloadResponse, error) {
	att, err := s.repo.GetAttachment(ctx, orgID, id)
	if err != nil {
		return DownloadResponse{}, err
	}
	if att.UploadStatus == StatusQuarantined {
		return DownloadResponse{}, ErrQuarantined
	}
	if att.UploadStatus != StatusUploaded {
		return DownloadResponse{}, ErrAttachmentNotReady
	}

	disposition := ContentDisposition(att.MIMEType, att.Filename)
	url, err := s.store.PresignGet(ctx, att.StorageKey, s.presignDownloadTTL, disposition)
	if err != nil {
		return DownloadResponse{}, err
	}
	return DownloadResponse{
		URL:                url,
		ExpiresAt:          s.now().UTC().Add(s.presignDownloadTTL),
		ContentType:        att.MIMEType,
		ContentDisposition: disposition,
		Filename:           att.Filename,
	}, nil
}

// ObjectKey builds the server-generated, tenant-prefixed key. The client
// filename is deliberately never part of the key.
func ObjectKey(orgID, ticketID, attachmentID uuid.UUID) string {
	return fmt.Sprintf("org/%s/tickets/%s/%s", orgID, ticketID, attachmentID)
}

// NormalizeMIMEType parses and lower-cases a declared media type, rejecting
// parameters and anything outside the allowlist.
func NormalizeMIMEType(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidFile
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", ErrUnsupportedType
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if !AllowedMIMEType(mediaType) {
		return "", ErrUnsupportedType
	}
	return mediaType, nil
}

// allowedMIMETypes is the upload allowlist. Executables and scripts are
// intentionally absent.
var allowedMIMETypes = map[string]bool{
	"image/png":          true,
	"image/jpeg":         true,
	"image/gif":          true,
	"image/webp":         true,
	"application/pdf":    true,
	"text/plain":         true,
	"text/csv":           true,
	"application/zip":    true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"video/mp4": true,
}

// AllowedMIMEType reports whether mediaType is uploadable.
func AllowedMIMEType(mediaType string) bool {
	return allowedMIMETypes[strings.ToLower(mediaType)]
}

// IsImage reports whether mediaType is an image handled by the thumbnail job.
func IsImage(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(mediaType), "image/")
}

// SanitizeFilename reduces a client filename to a safe display value. Path
// separators are stripped, control characters removed and traversal-only
// names rejected. The result is never used to build an object key.
func SanitizeFilename(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return "", ErrInvalidFilename
	}
	if len(name) > MaxFilenameBytes {
		return "", ErrInvalidFilename
	}
	return name, nil
}

// ContentDisposition builds an attachment disposition for non-images and an
// inline disposition for images. Quotes and backslashes are escaped so a
// filename cannot break the header.
func ContentDisposition(mediaType, filename string) string {
	kind := "attachment"
	if IsImage(mediaType) {
		kind = "inline"
	}
	safe := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(filename)
	return fmt.Sprintf(`%s; filename="%s"`, kind, safe)
}
