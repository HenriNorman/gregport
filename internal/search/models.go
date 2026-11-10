// Package search implements full-text search over tickets and comments.
//
// Postgres full-text search is the baseline and the fallback: it is always
// available, needs no extra infrastructure, and is used to compare latency
// against Elasticsearch on a seeded dataset. Elasticsearch is the primary
// backend in production; its index is derived data that can be rebuilt from
// Postgres at any time.
//
// Both backends return ticket ids plus highlight snippets, then the service
// loads the canonical rows from Postgres so soft deletes, permission checks
// and fresh data always come from the source of truth.
package search

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// DefaultLimit and MaxLimit bound the number of hits per page.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

// MaxQueryBytes bounds free-text input so a pathological query cannot force
// the backend to parse an unbounded expression.
const MaxQueryBytes = 512

// ErrInvalidCursor is returned when the opaque cursor cannot be decoded.
var ErrInvalidCursor = httpx.BadRequest("invalid_cursor", "cursor is invalid")

// ErrMissingOrg is returned when a search is attempted without an org id. The
// repository refuses to run rather than risk an unscoped query.
var ErrMissingOrg = errors.New("search: org id is required")

// Filter narrows a search. Query is free text; the remaining fields map to
// endpoint query parameters. A nil optional field means "no constraint".
type Filter struct {
	Query      string
	Status     *string
	Tag        *string
	AssigneeID *uuid.UUID
	Cursor     string
	Limit      int
}

// TicketHit is one result. The highlight fields contain safe HTML where
// matched terms are wrapped in <mark>; the text itself is HTML-escaped.
type TicketHit struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	Number           int64
	Subject          string
	Status           string
	Priority         string
	AssigneeID       *uuid.UUID
	RequesterEmail   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	SubjectHighlight string
	CommentHighlight string
	Rank             float64
}

// Result is one page of hits plus the cursor for the next page.
type Result struct {
	Hits       []TicketHit
	NextCursor string
}

// HitDTO is the API representation of one hit.
type HitDTO struct {
	ID             string        `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Number         int64         `json:"number" example:"1042"`
	Subject        string        `json:"subject" example:"Cannot log in"`
	Status         string        `json:"status" example:"open" enums:"new,open,pending,solved,closed"`
	Priority       string        `json:"priority" example:"high" enums:"low,normal,high,urgent"`
	AssigneeID     string        `json:"assignee_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440001"`
	RequesterEmail string        `json:"requester_email" example:"h@acme.com"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	Highlights     HighlightsDTO `json:"highlights"`
}

// HighlightsDTO carries the matched snippets for a hit.
type HighlightsDTO struct {
	Subject  string `json:"subject,omitempty"`
	Comments string `json:"comments,omitempty"`
}

// SearchResponse is the endpoint response.
type SearchResponse struct {
	Results    []HitDTO `json:"results"`
	NextCursor string   `json:"next_cursor,omitempty" example:"eyJyIjowLjQyLCJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"`
}

// AsDTO converts a hit to its API representation.
func AsDTO(h TicketHit) HitDTO {
	dto := HitDTO{
		ID:             h.ID.String(),
		Number:         h.Number,
		Subject:        h.Subject,
		Status:         h.Status,
		Priority:       h.Priority,
		RequesterEmail: h.RequesterEmail,
		CreatedAt:      h.CreatedAt,
		UpdatedAt:      h.UpdatedAt,
		Highlights: HighlightsDTO{
			Subject:  h.SubjectHighlight,
			Comments: h.CommentHighlight,
		},
	}
	if h.AssigneeID != nil {
		dto.AssigneeID = h.AssigneeID.String()
	}
	return dto
}

// NormalizeLimit clamps a requested page size into the supported range.
func NormalizeLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

// ValidStatus reports whether s is a ticket status. The endpoint rejects
// unknown values instead of silently returning an empty page.
func ValidStatus(s string) bool {
	switch s {
	case "new", "open", "pending", "solved", "closed":
		return true
	}
	return false
}
