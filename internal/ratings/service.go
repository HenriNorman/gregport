package ratings

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Invalidator drops cached dashboard counts after a new rating, because the
// average score is part of the dashboard summary.
type Invalidator interface {
	InvalidateCounts(ctx context.Context, orgID uuid.UUID) error
}

// Service verifies rating tokens and records ratings.
type Service struct {
	repo        Repo
	tokens      *TokenManager
	invalidator Invalidator
}

// NewService builds a rating service. tokens is required to submit ratings;
// a nil manager makes every submission fail verification.
func NewService(repo Repo, tokens *TokenManager) *Service {
	return &Service{repo: repo, tokens: tokens}
}

// SetCountsInvalidator wires dashboard cache invalidation. It is optional.
func (s *Service) SetCountsInvalidator(inv Invalidator) {
	s.invalidator = inv
}

// Submit verifies the token and records the rating. The token is single-use:
// the unique ticket_id constraint returns ErrAlreadyRated for a replay.
func (s *Service) Submit(ctx context.Context, req CreateRequest) (RatingResponse, error) {
	if req.Score < 1 || req.Score > 5 {
		return RatingResponse{}, ErrInvalidScore
	}
	comment := strings.TrimSpace(req.Comment)
	if len(comment) > MaxCommentLength {
		return RatingResponse{}, ErrInvalidComment
	}
	if s.tokens == nil {
		return RatingResponse{}, ErrInvalidToken
	}

	claims, err := s.tokens.Verify(req.Token)
	if err != nil {
		return RatingResponse{}, err
	}

	ticket, err := s.repo.LoadRatableTicket(ctx, claims.TicketID, claims.CustomerID)
	if err != nil {
		if errors.Is(err, ErrTicketNotFound) {
			return RatingResponse{}, ErrRatingNotAvailable
		}
		return RatingResponse{}, err
	}

	rating, err := s.repo.InsertRating(ctx, ticket.OrgID, ticket.ID, claims.CustomerID, req.Score, comment)
	if err != nil {
		return RatingResponse{}, err
	}

	// Best effort: the dashboard cache has a short TTL, so a failed
	// invalidation must not turn a recorded rating into a public error.
	if s.invalidator != nil {
		_ = s.invalidator.InvalidateCounts(ctx, ticket.OrgID)
	}

	return RatingResponse{
		TicketNumber: ticket.Number,
		Score:        rating.Score,
		Comment:      rating.Comment,
		CreatedAt:    rating.CreatedAt,
	}, nil
}
