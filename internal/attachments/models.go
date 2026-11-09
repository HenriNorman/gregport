// Package attachments implements direct-to-storage ticket attachments:
// metadata validation, presigned upload URLs, upload confirmation, short-lived
// download URLs, post-upload content sniffing and thumbnail generation.
//
// Clients upload straight to the private bucket. The API never proxies file
// bytes; it only creates the attachment row, validates the declared metadata
// and signs URLs. Object keys are always server-generated and tenant-prefixed,
// so a client filename can never influence where an object lands.
package attachments

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Upload lifecycle values match the attachments_upload_status_check
// constraint.
const (
	StatusPending     = "pending"
	StatusUploaded    = "uploaded"
	StatusQuarantined = "quarantined"
	StatusDeleted     = "deleted"
)

// MaxFilenameBytes bounds the display filename stored with an attachment.
const MaxFilenameBytes = 255

// ErrTicketNotFound is returned when the ticket number is absent from the
// caller's org.
var ErrTicketNotFound = httpx.NotFound("ticket_not_found", "ticket not found")

// ErrAttachmentNotFound is returned when an attachment does not exist in the
// caller's org. Foreign attachments are indistinguishable from missing ones.
var ErrAttachmentNotFound = httpx.NotFound("attachment_not_found", "attachment not found")

// API sentinels. They are *httpx.APIError values so WriteError maps them.
var (
	ErrFileTooLarge       = httpx.Unprocessable("file_too_large", "file exceeds the maximum attachment size")
	ErrUnsupportedType    = httpx.Unprocessable("unsupported_file_type", "file type is not allowed")
	ErrInvalidFilename    = httpx.Unprocessable("invalid_filename", "filename is invalid")
	ErrInvalidFile        = httpx.Unprocessable("invalid_file", "filename, mime type and size are required")
	ErrUploadNotFound     = httpx.Conflict("upload_not_found", "no uploaded object was found for this attachment")
	ErrSizeMismatch       = httpx.Unprocessable("size_mismatch", "uploaded object size does not match the declared size")
	ErrAttachmentNotReady = httpx.Conflict("attachment_not_ready", "attachment is not available for download")
	ErrQuarantined        = httpx.Conflict("attachment_quarantined", "attachment was quarantined")
)

// ErrObjectMissing is the internal sentinel used when the storage object is
// gone. Callers translate it to ErrUploadNotFound.
var ErrObjectMissing = errors.New("attachments: object missing")

// Attachment is one attachments row plus its upload lifecycle.
type Attachment struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	TicketID         uuid.UUID
	CommentID        *uuid.UUID
	StorageKey       string
	Filename         string
	MIMEType         string
	SizeBytes        int64
	UploadStatus     string
	ThumbnailKey     string
	Checksum         string
	QuarantineReason string
	CreatedAt        time.Time
	ConfirmedAt      *time.Time
	ExpiresAt        *time.Time
}

// PresignRequest is the client-declared metadata for an upload.
type PresignRequest struct {
	Filename string `json:"filename" example:"receipt.pdf"`
	MIMEType string `json:"mime_type" example:"application/pdf"`
	Size     int64  `json:"size" example:"204800"`
}

// PresignResponse tells the client where to upload. The URL is short-lived
// and scoped to one server-generated object key.
type PresignResponse struct {
	AttachmentID string    `json:"attachment_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	UploadURL    string    `json:"upload_url" example:"http://127.0.0.1:9000/gregport-attachments/..."`
	ExpiresAt    time.Time `json:"expires_at"`
}

// DownloadResponse is a short-lived signed download URL.
type DownloadResponse struct {
	URL                string    `json:"download_url"`
	ExpiresAt          time.Time `json:"expires_at"`
	ContentType        string    `json:"content_type"`
	ContentDisposition string    `json:"content_disposition"`
	Filename           string    `json:"filename"`
}

// AttachmentDTO is the API representation of an attachment. Storage keys are
// never exposed.
type AttachmentDTO struct {
	ID           string     `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	TicketID     string     `json:"ticket_id" example:"550e8400-e29b-41d4-a716-446655440001"`
	Filename     string     `json:"filename" example:"receipt.pdf"`
	MIMEType     string     `json:"mime_type" example:"application/pdf"`
	Size         int64      `json:"size" example:"204800"`
	Status       string     `json:"status" example:"uploaded" enums:"pending,uploaded,quarantined,deleted"`
	HasThumbnail bool       `json:"has_thumbnail" example:"false"`
	CreatedAt    time.Time  `json:"created_at"`
	ConfirmedAt  *time.Time `json:"confirmed_at,omitempty"`
}

// ToDTO converts an attachment to its API representation.
func ToDTO(a Attachment) AttachmentDTO {
	return AttachmentDTO{
		ID:           a.ID.String(),
		TicketID:     a.TicketID.String(),
		Filename:     a.Filename,
		MIMEType:     a.MIMEType,
		Size:         a.SizeBytes,
		Status:       a.UploadStatus,
		HasThumbnail: a.ThumbnailKey != "",
		CreatedAt:    a.CreatedAt,
		ConfirmedAt:  a.ConfirmedAt,
	}
}
