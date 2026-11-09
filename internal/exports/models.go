// Package exports builds asynchronous CSV exports of tickets.
//
// A request creates an export_jobs row and enqueues an export:run task. The
// worker streams matching tickets with a keyset cursor, writes CSV through an
// io.Pipe into a multipart object-storage upload and records the row count
// when it finishes. Memory stays flat regardless of the number of rows: only
// one batch of tickets and one CSV record are held at a time.
package exports

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Export job statuses match the export_jobs_status_check constraint.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// MaxBatchSize bounds how many ticket rows are read per keyset page. It is
// deliberately small so memory use is independent of the export size.
const MaxBatchSize = 5000

// ErrExportNotFound is returned when an export belongs to another org or does
// not exist. Foreign exports are indistinguishable from missing ones.
var ErrExportNotFound = httpx.NotFound("export_not_found", "export not found")

// allowedStatuses and allowedPriorities mirror the ticket enum types. They
// are validated here so an invalid filter fails before the job is created.
var (
	allowedStatuses = map[string]bool{
		"new": true, "open": true, "pending": true, "solved": true, "closed": true,
	}
	allowedPriorities = map[string]bool{
		"low": true, "normal": true, "high": true, "urgent": true,
	}
)

// Filter narrows the tickets included in an export. A nil field means "no
// constraint". The zero value exports every non-deleted ticket in the org.
type Filter struct {
	Status      *string    `json:"status,omitempty"`
	Priority    *string    `json:"priority,omitempty"`
	AssigneeID  *uuid.UUID `json:"assignee_id,omitempty"`
	RequesterID *uuid.UUID `json:"requester_id,omitempty"`
	CreatedFrom *time.Time `json:"created_from,omitempty"`
	CreatedTo   *time.Time `json:"created_to,omitempty"`
}

// CreateRequest is the API request body. IDs are strings so malformed values
// produce a clear validation error instead of a JSON decoding failure.
type CreateRequest struct {
	Status      string `json:"status,omitempty" example:"open"`
	Priority    string `json:"priority,omitempty" example:"high"`
	AssigneeID  string `json:"assignee_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440000"`
	RequesterID string `json:"requester_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440001"`
	CreatedFrom string `json:"created_from,omitempty" example:"2026-01-01T00:00:00Z"`
	CreatedTo   string `json:"created_to,omitempty" example:"2026-02-01T00:00:00Z"`
}

// ToFilter validates the request and converts it to a Filter.
func (r CreateRequest) ToFilter() (Filter, error) {
	var f Filter

	if r.Status != "" {
		if !allowedStatuses[r.Status] {
			return Filter{}, invalidField("status", "status is not a valid ticket status")
		}
		status := r.Status
		f.Status = &status
	}
	if r.Priority != "" {
		if !allowedPriorities[r.Priority] {
			return Filter{}, invalidField("priority", "priority is not a valid ticket priority")
		}
		priority := r.Priority
		f.Priority = &priority
	}
	if r.AssigneeID != "" {
		id, err := uuid.Parse(r.AssigneeID)
		if err != nil {
			return Filter{}, invalidField("assignee_id", "assignee_id must be a UUID")
		}
		f.AssigneeID = &id
	}
	if r.RequesterID != "" {
		id, err := uuid.Parse(r.RequesterID)
		if err != nil {
			return Filter{}, invalidField("requester_id", "requester_id must be a UUID")
		}
		f.RequesterID = &id
	}
	if r.CreatedFrom != "" {
		t, err := time.Parse(time.RFC3339, r.CreatedFrom)
		if err != nil {
			return Filter{}, invalidField("created_from", "created_from must be an RFC3339 timestamp")
		}
		f.CreatedFrom = &t
	}
	if r.CreatedTo != "" {
		t, err := time.Parse(time.RFC3339, r.CreatedTo)
		if err != nil {
			return Filter{}, invalidField("created_to", "created_to must be an RFC3339 timestamp")
		}
		f.CreatedTo = &t
	}
	if f.CreatedFrom != nil && f.CreatedTo != nil && f.CreatedTo.Before(*f.CreatedFrom) {
		return Filter{}, invalidField("created_to", "created_to must not be before created_from")
	}
	return f, nil
}

func invalidField(field, message string) error {
	return &httpx.APIError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "invalid_export_filter",
		Message: "export filter is invalid",
		Details: []httpx.FieldError{{Field: field, Message: message}},
	}
}

// Job is one export_jobs row.
type Job struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	RequestedBy uuid.UUID
	Status      string
	StorageKey  string
	Filters     []byte
	Error       string
	RowCount    *int64
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// DTO is the API representation of an export. Storage keys are never exposed;
// a finished export carries a short-lived presigned download URL instead.
type DTO struct {
	ID          string     `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Status      string     `json:"status" example:"done" enums:"pending,running,done,failed"`
	RowCount    *int64     `json:"row_count,omitempty" example:"1042"`
	DownloadURL string     `json:"download_url,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TicketRow is the flattened projection written to CSV. It carries only the
// fields a support export needs.
type TicketRow struct {
	ID             uuid.UUID
	Number         int64
	Subject        string
	Status         string
	Priority       string
	Channel        string
	RequesterEmail string
	RequesterName  string
	AssigneeEmail  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     *time.Time
	ClosedAt       *time.Time
}

// CSVHeader is the first line of every export.
var CSVHeader = []string{
	"number", "subject", "status", "priority", "channel",
	"requester_email", "requester_name", "assignee_email",
	"created_at", "updated_at", "resolved_at", "closed_at",
}

// CSVRecord renders a ticket row in CSVHeader order.
func CSVRecord(r TicketRow) []string {
	return []string{
		fmt.Sprintf("%d", r.Number),
		r.Subject,
		r.Status,
		r.Priority,
		r.Channel,
		r.RequesterEmail,
		r.RequesterName,
		r.AssigneeEmail,
		r.CreatedAt.UTC().Format(time.RFC3339),
		r.UpdatedAt.UTC().Format(time.RFC3339),
		formatOptionalTime(r.ResolvedAt),
		formatOptionalTime(r.ClosedAt),
	}
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ExportKey is the server-generated object key for one export.
func ExportKey(orgID, exportID uuid.UUID) string {
	return fmt.Sprintf("org/%s/exports/%s.csv", orgID, exportID)
}

// AsDTO converts a job, attaching a signed URL when the export finished.
func AsDTO(j Job, url string, expiresAt *time.Time) DTO {
	return DTO{
		ID:          j.ID.String(),
		Status:      j.Status,
		RowCount:    j.RowCount,
		DownloadURL: url,
		ExpiresAt:   expiresAt,
		Error:       j.Error,
		CreatedAt:   j.CreatedAt,
		CompletedAt: j.CompletedAt,
	}
}
