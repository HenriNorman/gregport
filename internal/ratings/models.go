package ratings

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors returned by the service.
var (
	ErrInvalidScore       = errors.New("ratings: score must be between 1 and 5")
	ErrInvalidComment     = errors.New("ratings: comment is too long")
	ErrTicketNotFound     = errors.New("ratings: ticket not found")
	ErrAlreadyRated       = errors.New("ratings: ticket has already been rated")
	ErrRatingNotAvailable = errors.New("ratings: rating is not available")
)

// Rating is one persisted satisfaction rating.
type Rating struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	TicketID   uuid.UUID
	CustomerID uuid.UUID
	Score      int
	Comment    string
	CreatedAt  time.Time
}

// Ticket is the org-scoped ticket a rating token refers to.
type Ticket struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	CustomerID uuid.UUID
	Number     int64
	Status     string
}

// CreateRequest is the body of POST /v1/public/ratings.
type CreateRequest struct {
	Token   string `json:"token"   example:"VGVzdFRva2Vu.c2lnbmF0dXJl"`
	Score   int    `json:"score"   example:"5" enums:"1,2,3,4,5"`
	Comment string `json:"comment" example:"Fast and helpful"`
}

// RatingResponse is the public representation of a submitted rating. It
// deliberately omits org and customer identifiers.
type RatingResponse struct {
	TicketNumber int64     `json:"ticket_number" example:"1042"`
	Score        int       `json:"score"         example:"5"`
	Comment      string    `json:"comment,omitempty" example:"Fast and helpful"`
	CreatedAt    time.Time `json:"created_at"    example:"2026-01-15T10:30:00Z"`
}

// MaxCommentLength bounds the free-text comment accepted from the public form.
const MaxCommentLength = 2000
