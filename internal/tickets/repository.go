package tickets

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/database"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type CreateInput struct {
	OrgID          uuid.UUID
	Actor          Actor
	RequesterEmail string
	RequesterName  string
	Subject        string
	Body           string
	Priority       string
}

func (r *Repository) CreateTicket(ctx context.Context, in CreateInput) (Ticket, Comment, error) {
	var (
		ticket  Ticket
		comment Comment
	)

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var number int64
		if err := tx.QueryRow(ctx,
			`UPDATE organizations
			 SET ticket_counter = ticket_counter + 1, updated_at = now()
			 WHERE id = $1
			 RETURNING ticket_counter`,
			in.OrgID,
		).Scan(&number); err != nil {
			return fmt.Errorf("increment counter: %w", err)
		}

		var requesterID uuid.UUID
		var requesterName *string
		if in.RequesterName != "" {
			requesterName = &in.RequesterName
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO customers (org_id, email, name)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (org_id, email)
			 DO UPDATE SET name = COALESCE(EXCLUDED.name, customers.name), updated_at = now()
			 RETURNING id`,
			in.OrgID, in.RequesterEmail, requesterName,
		).Scan(&requesterID); err != nil {
			return fmt.Errorf("upsert customer: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel)
			 VALUES ($1, $2, $3, $4, $5, 'api')
			 RETURNING id, org_id, number, requester_id, subject, status, priority,
			           channel, version, created_at, updated_at`,
			in.OrgID, number, requesterID, in.Subject, in.Priority,
		).Scan(
			&ticket.ID, &ticket.OrgID, &ticket.Number, &ticket.RequesterID,
			&ticket.Subject, &ticket.Status, &ticket.Priority, &ticket.Channel,
			&ticket.Version, &ticket.CreatedAt, &ticket.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert ticket: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_customer_id, body, is_internal)
			 VALUES ($1, $2, 'customer', $3, $4, false)
			 RETURNING id, org_id, ticket_id, author_type, author_user_id,
			           author_customer_id, body, is_internal, created_at`,
			in.OrgID, ticket.ID, requesterID, in.Body,
		).Scan(
			&comment.ID, &comment.OrgID, &comment.TicketID, &comment.AuthorType,
			&comment.AuthorUserID, &comment.AuthorCustomerID, &comment.Body,
			&comment.IsInternal, &comment.CreatedAt,
		); err != nil {
			return fmt.Errorf("insert first comment: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, ticket.ID, in.Actor, "ticket.created", map[string]any{
			"number":   ticket.Number,
			"subject":  ticket.Subject,
			"priority": ticket.Priority,
		}); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return Ticket{}, Comment{}, err
	}
	return ticket, comment, nil
}
