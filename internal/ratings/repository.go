package ratings

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo is the persistence contract used by the rating service.
type Repo interface {
	LoadRatableTicket(ctx context.Context, ticketID, customerID uuid.UUID) (Ticket, error)
	InsertRating(ctx context.Context, orgID, ticketID, customerID uuid.UUID, score int, comment string) (Rating, error)
	AverageScore(ctx context.Context, orgID uuid.UUID) (float64, int64, error)
}

// Repository persists ratings in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a PostgreSQL-backed rating repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// LoadRatableTicket resolves the ticket and verifies that the token's customer
// is the requester. A mismatch is indistinguishable from a missing ticket, so
// a tampered token cannot probe for ticket ids.
func (r *Repository) LoadRatableTicket(ctx context.Context, ticketID, customerID uuid.UUID) (Ticket, error) {
	var t Ticket
	err := r.pool.QueryRow(ctx,
		`SELECT id, org_id, requester_id, number, status
		 FROM tickets
		 WHERE id = $1 AND deleted_at IS NULL`,
		ticketID,
	).Scan(&t.ID, &t.OrgID, &t.CustomerID, &t.Number, &t.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, ErrTicketNotFound
		}
		return Ticket{}, fmt.Errorf("load rating ticket: %w", err)
	}
	if t.CustomerID != customerID {
		return Ticket{}, ErrTicketNotFound
	}
	return t, nil
}

// InsertRating stores one rating. The unique ticket_id constraint makes the
// link single-use: a second submission returns ErrAlreadyRated.
func (r *Repository) InsertRating(ctx context.Context, orgID, ticketID, customerID uuid.UUID, score int, comment string) (Rating, error) {
	var rating Rating
	err := r.pool.QueryRow(ctx,
		`INSERT INTO ticket_ratings (org_id, ticket_id, customer_id, score, comment)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (ticket_id) DO NOTHING
		 RETURNING id, org_id, ticket_id, customer_id, score, comment, created_at`,
		orgID, ticketID, customerID, score, comment,
	).Scan(
		&rating.ID, &rating.OrgID, &rating.TicketID, &rating.CustomerID,
		&rating.Score, &rating.Comment, &rating.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rating{}, ErrAlreadyRated
		}
		return Rating{}, fmt.Errorf("insert rating: %w", err)
	}
	return rating, nil
}

// AverageScore returns the average score and number of ratings for one org.
// An org with no ratings returns 0 and 0.
func (r *Repository) AverageScore(ctx context.Context, orgID uuid.UUID) (float64, int64, error) {
	var average float64
	var count int64
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(avg(score), 0)::float8, count(*)
		 FROM ticket_ratings
		 WHERE org_id = $1`,
		orgID,
	).Scan(&average, &count); err != nil {
		return 0, 0, fmt.Errorf("average rating: %w", err)
	}
	return average, count, nil
}
