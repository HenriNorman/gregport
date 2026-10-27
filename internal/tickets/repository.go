package tickets

import (
	"context"
	"fmt"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/database"
)


type ListFilter struct {
	Status      *string
	Priority    *string
	AssigneeID  *uuid.UUID
	RequesterID *uuid.UUID
	Limit       int
	Cursor      string
}

type ListResult struct {
	Tickets    []Ticket
	NextCursor string
}

type Repository struct {
	pool *pgxpool.Pool
}

type UpdateInput struct {
	OrgID           uuid.UUID
	Number          int64
	ExpectedVersion int
	Actor           Actor
	Status          *string
	Priority        *string
	Subject         *string
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

func (r *Repository) ListTickets(ctx context.Context, orgID uuid.UUID, f ListFilter) (ListResult, error) {
	if f.Limit <= 0 {
		f.Limit = 25
	}
	if f.Limit > 100 {
		f.Limit = 100
	}

	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return ListResult{}, err
	}

	args := []any{orgID}
	where := "WHERE org_id = $1 AND deleted_at IS NULL"
	add := func(clause string, val any) {
		args = append(args, val)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}

	if f.Status != nil {
		add("status", *f.Status)
	}
	if f.Priority != nil {
		add("priority", *f.Priority)
	}
	if f.AssigneeID != nil {
		add("assignee_id", *f.AssigneeID)
	}
	if f.RequesterID != nil {
		add("requester_id", *f.RequesterID)
	}
	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}

	args = append(args, f.Limit+1)
	query := fmt.Sprintf(
		`SELECT id, org_id, number, requester_id, assignee_id, team_id, sla_policy_id,
		        merged_into_id, subject, status, priority, channel,
		        first_response_due_at, resolution_due_at, first_response_at,
		        resolved_at, closed_at, version, created_at, updated_at, deleted_at
		 FROM tickets
		 %s
		 ORDER BY created_at DESC, id DESC
		 LIMIT $%d`,
		where, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()

	var out []Ticket
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(
			&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
			&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
			&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
			&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan ticket: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("rows: %w", err)
	}

	var next string
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}

	return ListResult{Tickets: out, NextCursor: next}, nil
}

func (r *Repository) GetTicket(ctx context.Context, orgID uuid.UUID, number int64) (Ticket, error) {
	var t Ticket
	err := r.pool.QueryRow(ctx,
		`SELECT id, org_id, number, requester_id, assignee_id, team_id, sla_policy_id,
		        merged_into_id, subject, status, priority, channel,
		        first_response_due_at, resolution_due_at, first_response_at,
		        resolved_at, closed_at, version, created_at, updated_at, deleted_at
		 FROM tickets
		 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
		orgID, number,
	).Scan(
		&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
		&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
		&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
		&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, ErrTicketNotFound
		}
		return Ticket{}, fmt.Errorf("get ticket: %w", err)
	}
	return t, nil
}

func (r *Repository) UpdateTicket(ctx context.Context, in UpdateInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		// 1. lock the row and read current state
		var (
			curID       uuid.UUID
			curStatus   string
			curVersion  int
			curResolved *time.Time
		)
		if err := tx.QueryRow(ctx,
			`SELECT id, status, version, resolved_at
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&curID, &curStatus, &curVersion, &curResolved); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		// 2. optimistic concurrency check
		if curVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}

		// 3. state machine
		newStatus := curStatus
		if in.Status != nil && *in.Status != curStatus {
			if !CanTransition(curStatus, *in.Status) {
				return ErrInvalidTransition
			}
			newStatus = *in.Status
		}

		// 4. build the UPDATE dynamically, positional params only
		args := []any{}
		sets := []string{"version = version + 1", "updated_at = now()"}
		add := func(col string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}

		if in.Status != nil && *in.Status != curStatus {
			add("status", *in.Status)
			switch *in.Status {
			case "open":
				if curStatus == "solved" {
					// reopen: clear resolved_at
					sets = append(sets, "resolved_at = NULL")
				} else {
					// first move out of new: set first_response_at if unset
					sets = append(sets, "first_response_at = COALESCE(first_response_at, now())")
				}
			case "solved":
				sets = append(sets, "resolved_at = now()")
			case "closed":
				sets = append(sets, "closed_at = now()")
			}
		}
		if in.Priority != nil {
			add("priority", *in.Priority)
		}
		if in.Subject != nil {
			add("subject", *in.Subject)
		}

		// WHERE uses org_id, number, and expected version
		args = append(args, in.OrgID, in.Number, in.ExpectedVersion)
		whereOrg := len(args) - 2
		whereNum := len(args) - 1
		whereVer := len(args)

		query := fmt.Sprintf(
			`UPDATE tickets SET %s
			 WHERE org_id = $%d AND number = $%d AND version = $%d
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			joinComma(sets), whereOrg, whereNum, whereVer,
		)

		if err := tx.QueryRow(ctx, query, args...).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrVersionMismatch
			}
			return fmt.Errorf("update ticket: %w", err)
		}

		// 5. event on status change
		if newStatus != curStatus {
			if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.status_changed", map[string]any{
				"old": curStatus,
				"new": newStatus,
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
