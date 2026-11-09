package tickets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

	// RecomputeSLA is set when priority changed. When true the three SLA
	// fields are written as given, including NULL when the new priority has no
	// policy.
	RecomputeSLA       bool
	SLAPolicyID        *uuid.UUID
	FirstResponseDueAt *time.Time
	ResolutionDueAt    *time.Time
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

	SLAPolicyID        *uuid.UUID
	FirstResponseDueAt *time.Time
	ResolutionDueAt    *time.Time
}

type AssignInput struct {
	OrgID      uuid.UUID
	Number     int64
	Actor      Actor
	AssigneeID *uuid.UUID
	TeamID     *uuid.UUID
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
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
			                      sla_policy_id, first_response_due_at, resolution_due_at)
			 VALUES ($1, $2, $3, $4, $5, 'api', $6, $7, $8)
			 RETURNING id, org_id, number, requester_id, subject, status, priority,
			           sla_policy_id, first_response_due_at, resolution_due_at,
			           channel, version, created_at, updated_at`,
			in.OrgID, number, requesterID, in.Subject, in.Priority,
			in.SLAPolicyID, in.FirstResponseDueAt, in.ResolutionDueAt,
		).Scan(
			&ticket.ID, &ticket.OrgID, &ticket.Number, &ticket.RequesterID,
			&ticket.Subject, &ticket.Status, &ticket.Priority,
			&ticket.SLAPolicyID, &ticket.FirstResponseDueAt, &ticket.ResolutionDueAt,
			&ticket.Channel, &ticket.Version, &ticket.CreatedAt, &ticket.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert ticket: %w", err)
		}

		ticket.RequesterEmail = in.RequesterEmail
		ticket.RequesterName = in.RequesterName

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
	where := "WHERE t.org_id = $1 AND t.deleted_at IS NULL"
	add := func(clause string, val any) {
		args = append(args, val)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}

	if f.Status != nil {
		add("t.status", *f.Status)
	}
	if f.Priority != nil {
		add("t.priority", *f.Priority)
	}
	if f.AssigneeID != nil {
		add("t.assignee_id", *f.AssigneeID)
	}
	if f.RequesterID != nil {
		add("t.requester_id", *f.RequesterID)
	}
	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (t.created_at, t.id) < ($%d, $%d)", len(args)-1, len(args))
	}

	args = append(args, f.Limit+1)
	query := fmt.Sprintf(
		`SELECT t.id, t.org_id, t.number, t.requester_id, t.assignee_id, t.team_id,
		        t.sla_policy_id, t.merged_into_id, t.subject, t.status, t.priority,
		        t.channel, t.first_response_due_at, t.resolution_due_at,
		        t.first_response_at, t.resolved_at, t.closed_at, t.version,
		        t.created_at, t.updated_at, t.deleted_at,
		        c.email, COALESCE(c.name, '')
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 %s
		 ORDER BY t.created_at DESC, t.id DESC
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
			&t.RequesterEmail, &t.RequesterName,
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
		`SELECT t.id, t.org_id, t.number, t.requester_id, t.assignee_id, t.team_id,
		        t.sla_policy_id, t.merged_into_id, t.subject, t.status, t.priority,
		        t.channel, t.first_response_due_at, t.resolution_due_at,
		        t.first_response_at, t.resolved_at, t.closed_at, t.version,
		        t.created_at, t.updated_at, t.deleted_at,
		        c.email, COALESCE(c.name, '')
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 WHERE t.org_id = $1 AND t.number = $2 AND t.deleted_at IS NULL`,
		orgID, number,
	).Scan(
		&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
		&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
		&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
		&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
		&t.RequesterEmail, &t.RequesterName,
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
		var (
			curID       uuid.UUID
			curStatus   string
			curVersion  int
			curResolved *time.Time
			curMerged   *uuid.UUID
		)
		if err := tx.QueryRow(ctx,
			`SELECT id, status, version, resolved_at, merged_into_id
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&curID, &curStatus, &curVersion, &curResolved, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		if curMerged != nil {
			return ErrTicketMerged
		}

		if curVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}

		newStatus := curStatus
		if in.Status != nil && *in.Status != curStatus {
			if !CanTransition(curStatus, *in.Status) {
				return ErrInvalidTransition
			}
			newStatus = *in.Status
		}

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
					sets = append(sets, "resolved_at = NULL")
				} else {
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
		if in.RecomputeSLA {
			add("sla_policy_id", in.SLAPolicyID)
			add("first_response_due_at", in.FirstResponseDueAt)
			add("resolution_due_at", in.ResolutionDueAt)
		}
		if in.Subject != nil {
			add("subject", *in.Subject)
		}

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
			strings.Join(sets, ", "), whereOrg, whereNum, whereVer,
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

		if newStatus != curStatus {
			if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.status_changed", map[string]any{
				"old": curStatus,
				"new": newStatus,
			}); err != nil {
				return err
			}
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

type CreateCommentInput struct {
	OrgID      uuid.UUID
	Number     int64
	Actor      Actor
	Body       string
	IsInternal bool
}

func (r *Repository) SoftDeleteTicket(ctx context.Context, orgID uuid.UUID, number int64, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var curMerged *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, merged_into_id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			orgID, number,
		).Scan(&ticketID, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}
		if curMerged != nil {
			return ErrTicketMerged
		}

		if _, err := tx.Exec(ctx,
			`UPDATE tickets
			 SET deleted_at = now(), version = version + 1, updated_at = now()
			 WHERE id = $1`,
			ticketID,
		); err != nil {
			return fmt.Errorf("soft delete ticket: %w", err)
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.deleted", nil)
	})
}

// PurgeDispatchedEvents deletes dispatched ticket_events older than before,
// limited to batch rows. Events still referenced by an email_delivery are
// skipped so the event_id foreign key is never violated; the email delivery
// purge should run first to unblock them.
func (r *Repository) PurgeDispatchedEvents(ctx context.Context, before time.Time, batch int) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge time is required")
	}
	if batch <= 0 {
		batch = 100
	}
	tag, err := r.pool.Exec(ctx,
		`WITH to_delete AS (
		         SELECT id FROM ticket_events
		         WHERE dispatched_at IS NOT NULL
		           AND created_at <= $1
		           AND NOT EXISTS (
		                   SELECT 1 FROM email_deliveries e WHERE e.event_id = ticket_events.id
		               )
		         LIMIT $2
		     )
		     DELETE FROM ticket_events
		     WHERE id IN (SELECT id FROM to_delete)`,
		before, batch,
	)
	if err != nil {
		return 0, fmt.Errorf("purge dispatched ticket events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ObjectDeleter removes one object-storage key. *storage.S3 satisfies it.
type ObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// PurgeDeletedTickets hard-deletes tickets soft-deleted before before, along
// with their comments, events, tags, watchers, ratings, notifications and
// attachments. Attachment objects (originals and thumbnails) are removed from
// storage before any rows are deleted; the deletes are idempotent, so a
// retried run safely repeats them.
func (r *Repository) PurgeDeletedTickets(ctx context.Context, before time.Time, batch int, objects ObjectDeleter) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge time is required")
	}
	if batch <= 0 {
		batch = 100
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id FROM tickets
		 WHERE deleted_at IS NOT NULL AND deleted_at < $1
		 ORDER BY deleted_at
		 LIMIT $2`,
		before, batch,
	)
	if err != nil {
		return 0, fmt.Errorf("list deleted tickets: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan deleted ticket: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("deleted ticket rows: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	if objects != nil {
		keys, err := r.attachmentKeysForTickets(ctx, ids)
		if err != nil {
			return 0, err
		}
		for _, key := range keys {
			if err := objects.Delete(ctx, key); err != nil {
				return 0, fmt.Errorf("delete attachment object %q: %w", key, err)
			}
		}
	}

	var deleted int64
	err = database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		// Break the self-reference before deleting rows.
		if _, err := tx.Exec(ctx,
			`UPDATE tickets SET merged_into_id = NULL WHERE merged_into_id = ANY($1)`, ids,
		); err != nil {
			return fmt.Errorf("clear merged tickets: %w", err)
		}

		// Rows that reference a comment on one of these tickets must go (or
		// be detached) before the comments themselves.
		if _, err := tx.Exec(ctx,
			`DELETE FROM inbound_emails
			 WHERE ticket_id = ANY($1)
			    OR comment_id IN (SELECT id FROM comments WHERE ticket_id = ANY($1))`,
			ids,
		); err != nil {
			return fmt.Errorf("delete inbound emails: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM notifications
			 WHERE ticket_id = ANY($1)
			    OR comment_id IN (SELECT id FROM comments WHERE ticket_id = ANY($1))`,
			ids,
		); err != nil {
			return fmt.Errorf("delete notifications: %w", err)
		}

		childTables := []struct {
			query string
			name  string
		}{
			{`DELETE FROM attachments WHERE ticket_id = ANY($1)`, "attachments"},
			{`DELETE FROM satisfaction_ratings WHERE ticket_id = ANY($1)`, "satisfaction ratings"},
			{`DELETE FROM ticket_ratings WHERE ticket_id = ANY($1)`, "ticket ratings"},
			{`DELETE FROM ticket_tags WHERE ticket_id = ANY($1)`, "ticket tags"},
			{`DELETE FROM ticket_watchers WHERE ticket_id = ANY($1)`, "ticket watchers"},
			{`DELETE FROM ticket_events WHERE ticket_id = ANY($1)`, "ticket events"},
			{`DELETE FROM comments WHERE ticket_id = ANY($1)`, "comments"},
		}
		for _, child := range childTables {
			if _, err := tx.Exec(ctx, child.query, ids); err != nil {
				return fmt.Errorf("delete %s: %w", child.name, err)
			}
		}

		tag, err := tx.Exec(ctx,
			`DELETE FROM tickets WHERE id = ANY($1) AND deleted_at IS NOT NULL`, ids,
		)
		if err != nil {
			return fmt.Errorf("delete tickets: %w", err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// attachmentKeysForTickets returns every storage key (original and thumbnail)
// belonging to the given tickets.
func (r *Repository) attachmentKeysForTickets(ctx context.Context, ticketIDs []uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT storage_key, COALESCE(thumbnail_key, '')
		 FROM attachments WHERE ticket_id = ANY($1)`,
		ticketIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list attachment keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key, thumb string
		if err := rows.Scan(&key, &thumb); err != nil {
			return nil, fmt.Errorf("scan attachment key: %w", err)
		}
		keys = append(keys, key)
		if thumb != "" {
			keys = append(keys, thumb)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attachment key rows: %w", err)
	}
	return keys, nil
}

func (r *Repository) CreateComment(ctx context.Context, in CreateCommentInput) (Comment, error) {
	var out Comment
	var curMerged *uuid.UUID
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, merged_into_id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			in.OrgID, in.Number,
		).Scan(&ticketID, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}
		if curMerged != nil {
			return ErrTicketMerged
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_user_id, body, is_internal)
			 VALUES ($1, $2, 'user', $3, $4, $5)
			 RETURNING id, org_id, ticket_id, author_type, author_user_id,
			           author_customer_id, body, is_internal, created_at`,
			in.OrgID, ticketID, in.Actor.ID, in.Body, in.IsInternal,
		).Scan(
			&out.ID, &out.OrgID, &out.TicketID, &out.AuthorType,
			&out.AuthorUserID, &out.AuthorCustomerID, &out.Body,
			&out.IsInternal, &out.CreatedAt,
		); err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}

		// set first_response_at if this is the first public agent comment
		if !in.IsInternal {
			if _, err := tx.Exec(ctx,
				`UPDATE tickets
				 SET updated_at = now(),
				     first_response_at = COALESCE(first_response_at, now())
				 WHERE id = $1`,
				ticketID,
			); err != nil {
				return fmt.Errorf("bump ticket: %w", err)
			}
		}
		// customer comments have actor_user_id NULL and can't be watchers)
		if in.Actor.ID != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO ticket_watchers (ticket_id, user_id) VALUES ($1, $2)
				 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
				ticketID, *in.Actor.ID,
			); err != nil {
				return fmt.Errorf("auto-watch commenter: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE tickets SET updated_at = now() WHERE id = $1`,
				ticketID,
			); err != nil {
				return fmt.Errorf("bump ticket: %w", err)
			}
		}

		return Record(ctx, tx, in.OrgID, ticketID, in.Actor, "comment.added", map[string]any{
			"comment_id":  out.ID.String(),
			"is_internal": out.IsInternal,
		})
	})
	if err != nil {
		return Comment{}, err
	}
	return out, nil
}

func (r *Repository) ListComments(ctx context.Context, orgID uuid.UUID, number int64, includeInternal bool) ([]Comment, error) {
	query := `SELECT c.id, c.org_id, c.ticket_id, c.author_type, c.author_user_id,
	                 c.author_customer_id, c.body, c.is_internal, c.created_at
	          FROM comments c
	          JOIN tickets t ON t.id = c.ticket_id
	          WHERE t.org_id = $1 AND t.number = $2 AND t.deleted_at IS NULL
	            AND c.deleted_at IS NULL`
	if !includeInternal {
		query += " AND c.is_internal = false"
	}
	query += " ORDER BY c.created_at ASC, c.id ASC"

	rows, err := r.pool.Query(ctx, query, orgID, number)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(
			&c.ID, &c.OrgID, &c.TicketID, &c.AuthorType, &c.AuthorUserID,
			&c.AuthorCustomerID, &c.Body, &c.IsInternal, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) CreateTeam(ctx context.Context, orgID uuid.UUID, name string) (Team, error) {
	var t Team
	err := r.pool.QueryRow(ctx,
		`INSERT INTO teams (org_id, name) VALUES ($1, $2)
		 RETURNING id, org_id, name, created_at`,
		orgID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Team{}, ErrTeamNameTaken
		}
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	return t, nil
}

func (r *Repository) ListTeams(ctx context.Context, orgID uuid.UUID) ([]Team, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, created_at
		 FROM teams WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateTeam(ctx context.Context, orgID, teamID uuid.UUID, name string) (Team, error) {
	var t Team
	err := r.pool.QueryRow(ctx,
		`UPDATE teams SET name = $3
		 WHERE org_id = $1 AND id = $2
		 RETURNING id, org_id, name, created_at`,
		orgID, teamID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, ErrTeamNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Team{}, ErrTeamNameTaken
		}
		return Team{}, fmt.Errorf("update team: %w", err)
	}
	return t, nil
}

func (r *Repository) DeleteTeam(ctx context.Context, orgID, teamID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM teams WHERE org_id = $1 AND id = $2`,
		orgID, teamID,
	)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTeamNotFound
	}
	return nil
}

// AddTeamMember verifies org membership and inserts in one transaction.
// The membership check is in the same query as the insert, so a user id
// from the client is never trusted on its own.
func (r *Repository) AddTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var teamExists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM teams WHERE org_id = $1 AND id = $2)`,
			orgID, teamID,
		).Scan(&teamExists); err != nil {
			return fmt.Errorf("check team: %w", err)
		}
		if !teamExists {
			return ErrTeamNotFound
		}

		var isMember bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM memberships
			   WHERE org_id = $1 AND user_id = $2
			 )`,
			orgID, userID,
		).Scan(&isMember); err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		if !isMember {
			return ErrMemberNotInOrg
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO team_members (team_id, user_id)
			 VALUES ($1, $2)
			 ON CONFLICT (team_id, user_id) DO NOTHING`,
			teamID, userID,
		); err != nil {
			return fmt.Errorf("insert team member: %w", err)
		}
		return nil
	})
}

func (r *Repository) RemoveTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM team_members
		 WHERE team_id = (SELECT id FROM teams WHERE org_id = $1 AND id = $2)
		   AND user_id = $3`,
		orgID, teamID, userID,
	)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTeamNotFound
	}
	return nil
}

func (r *Repository) AssignTicket(ctx context.Context, in AssignInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var oldAssignee *uuid.UUID
		var oldTeam *uuid.UUID
		var curVersion int
		var curMerged *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, assignee_id, team_id, version, merged_into_id
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&ticketID, &oldAssignee, &oldTeam, &curVersion, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		if curMerged != nil {
			return ErrTicketMerged
		}

		if in.AssigneeID != nil {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM memberships
				   WHERE org_id = $1 AND user_id = $2
				     AND role IN ('owner','admin','agent')
				 )`,
				in.OrgID, *in.AssigneeID,
			).Scan(&ok); err != nil {
				return fmt.Errorf("check assignee: %w", err)
			}
			if !ok {
				return ErrAssigneeNotAgent
			}
		}

		if in.TeamID != nil {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM teams WHERE org_id = $1 AND id = $2)`,
				in.OrgID, *in.TeamID,
			).Scan(&ok); err != nil {
				return fmt.Errorf("check team: %w", err)
			}
			if !ok {
				return ErrTeamNotFound
			}
		}

		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET assignee_id = COALESCE($3, assignee_id),
			     team_id     = COALESCE($4, team_id),
			     version     = version + 1,
			     updated_at  = now()
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			in.OrgID, in.Number, in.AssigneeID, in.TeamID,
		).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			return fmt.Errorf("update ticket: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.assigned", map[string]any{
			"old_assignee_id": oldAssignee,
			"new_assignee_id": in.AssigneeID,
			"old_team_id":     oldTeam,
			"new_team_id":     in.TeamID,
		}); err != nil {
			return err
		}

		if in.AssigneeID != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO ticket_watchers (ticket_id, user_id)
				 VALUES ($1, $2)
				 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
				out.ID, *in.AssigneeID,
			); err != nil {
				return fmt.Errorf("add watcher: %w", err)
			}
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

// AssignAuto picks the team member with the oldest last_assigned_at and
// assigns the ticket to them, all inside one transaction. SKIP LOCKED on
// team_members means two concurrent auto-assigns pick different agents
// instead of both choosing the same one.
func (r *Repository) AssignAuto(ctx context.Context, in AssignInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var oldAssignee *uuid.UUID
		var oldTeam *uuid.UUID
		var curMerged *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, assignee_id, team_id, merged_into_id
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&ticketID, &oldAssignee, &oldTeam, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}
		if curMerged != nil {
			return ErrTicketMerged
		}
		if in.TeamID == nil {
			return ErrInvalidInput
		}

		var picked uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT tm.user_id
			 FROM team_members tm
			 JOIN memberships m ON m.user_id = tm.user_id AND m.org_id = $2
			 WHERE tm.team_id = $1
			   AND m.role IN ('owner','admin','agent')
			 ORDER BY tm.last_assigned_at NULLS FIRST
			 LIMIT 1
			 FOR UPDATE OF tm SKIP LOCKED`,
			*in.TeamID, in.OrgID,
		).Scan(&picked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoTeamMembers
			}
			return fmt.Errorf("pick assignee: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET assignee_id = $3,
			     team_id     = $4,
			     version     = version + 1,
			     updated_at  = now()
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			in.OrgID, in.Number, picked, *in.TeamID,
		).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			return fmt.Errorf("update ticket: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE team_members SET last_assigned_at = now()
			 WHERE team_id = $1 AND user_id = $2`,
			*in.TeamID, picked,
		); err != nil {
			return fmt.Errorf("update last_assigned_at: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.assigned", map[string]any{
			"old_assignee_id": oldAssignee,
			"new_assignee_id": picked,
			"old_team_id":     oldTeam,
			"new_team_id":     *in.TeamID,
			"auto":            true,
		}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_watchers (ticket_id, user_id)
			 VALUES ($1, $2)
			 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
			out.ID, picked,
		); err != nil {
			return fmt.Errorf("add watcher: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

func (r *Repository) CreateTag(ctx context.Context, orgID uuid.UUID, name string) (Tag, error) {
	var t Tag
	err := r.pool.QueryRow(ctx,
		`INSERT INTO tags (org_id, name) VALUES ($1, $2)
		 RETURNING id, org_id, name, created_at`,
		orgID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Tag{}, ErrTagNameTaken
		}
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

func (r *Repository) ListTags(ctx context.Context, orgID uuid.UUID) ([]Tag, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, created_at
		 FROM tags WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// AttachTag adds a tag to a ticket by name. Idempotent: a second attach
// writes no event and returns nil.
func (r *Repository) AttachTag(ctx context.Context, orgID uuid.UUID, number int64, tagName string, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var curMerged *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, merged_into_id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		if curMerged != nil {
			return ErrTicketMerged
		}
		var tagID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tags WHERE org_id = $1 AND name = $2`,
			orgID, tagName,
		).Scan(&tagID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTagNotFound
			}
			return fmt.Errorf("find tag: %w", err)
		}

		tag, err := tx.Exec(ctx,
			`INSERT INTO ticket_tags (ticket_id, tag_id) VALUES ($1, $2)
			 ON CONFLICT (ticket_id, tag_id) DO NOTHING`,
			ticketID, tagID,
		)
		if err != nil {
			return fmt.Errorf("insert ticket_tag: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.tagged", map[string]any{
			"tag": tagName,
		})
	})
}

// DetachTag removes a tag from a ticket by name. Idempotent: a second detach
// writes no event and returns nil.
func (r *Repository) DetachTag(ctx context.Context, orgID uuid.UUID, number int64, tagName string, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var curMerged *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, merged_into_id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID, &curMerged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		if curMerged != nil {
			return ErrTicketMerged
		}
		var tagID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tags WHERE org_id = $1 AND name = $2`,
			orgID, tagName,
		).Scan(&tagID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTagNotFound
			}
			return fmt.Errorf("find tag: %w", err)
		}

		tag, err := tx.Exec(ctx,
			`DELETE FROM ticket_tags WHERE ticket_id = $1 AND tag_id = $2`,
			ticketID, tagID,
		)
		if err != nil {
			return fmt.Errorf("delete ticket_tag: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.untagged", map[string]any{
			"tag": tagName,
		})
	})
}

func (r *Repository) AddWatcher(ctx context.Context, orgID uuid.UUID, number int64, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		var isMember bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM memberships WHERE org_id = $1 AND user_id = $2
			 )`,
			orgID, userID,
		).Scan(&isMember); err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		if !isMember {
			return ErrMemberNotInOrg
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_watchers (ticket_id, user_id) VALUES ($1, $2)
			 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
			ticketID, userID,
		); err != nil {
			return fmt.Errorf("insert watcher: %w", err)
		}
		return nil
	})
}

func (r *Repository) RemoveWatcher(ctx context.Context, orgID uuid.UUID, number int64, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		_, err := tx.Exec(ctx,
			`DELETE FROM ticket_watchers WHERE ticket_id = $1 AND user_id = $2`,
			ticketID, userID,
		)
		if err != nil {
			return fmt.Errorf("delete watcher: %w", err)
		}
		return nil
	})
}

func (r *Repository) ListTicketEvents(ctx context.Context, orgID uuid.UUID, number int64, cursorStr string, limit int) ([]TicketEvent, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	cur, err := decodeCursor(cursorStr)
	if err != nil {
		return nil, "", err
	}

	args := []any{orgID, number}
	where := `WHERE t.org_id = $1 AND t.number = $2 AND t.deleted_at IS NULL`

	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (e.created_at, e.id) > ($%d, $%d)", len(args)-1, len(args))
	}

	args = append(args, limit+1)
	query := fmt.Sprintf(
		`SELECT e.id, e.org_id, e.ticket_id, e.actor_type, e.actor_id,
		        e.event_type, e.data, e.created_at
		 FROM ticket_events e
		 JOIN tickets t ON t.id = e.ticket_id
		 %s
		 ORDER BY e.created_at ASC, e.id ASC
		 LIMIT $%d`,
		where, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []TicketEvent
	for rows.Next() {
		var e TicketEvent
		if err := rows.Scan(
			&e.ID, &e.OrgID, &e.TicketID, &e.ActorType, &e.ActorID,
			&e.EventType, &e.Data, &e.CreatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("rows: %w", err)
	}

	var next string
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}

	return out, next, nil
}

func (r *Repository) MergeTickets(ctx context.Context, orgID uuid.UUID, sourceNumber, targetNumber int64, actor Actor) (Ticket, Ticket, error) {
	var source, target Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, org_id, number, requester_id, assignee_id, team_id,
			        sla_policy_id, merged_into_id, subject, status, priority,
			        channel, first_response_due_at, resolution_due_at,
			        first_response_at, resolved_at, closed_at, version,
			        created_at, updated_at, deleted_at
			 FROM tickets
			 WHERE org_id = $1 AND number IN ($2, $3) AND deleted_at IS NULL
			 ORDER BY id
			 FOR UPDATE`,
			orgID, sourceNumber, targetNumber,
		)
		if err != nil {
			return fmt.Errorf("lock tickets: %w", err)
		}
		var found []Ticket
		for rows.Next() {
			var t Ticket
			if err := rows.Scan(
				&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
				&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
				&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
				&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan ticket: %w", err)
			}
			found = append(found, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows: %w", err)
		}

		if len(found) != 2 {
			return ErrTicketNotFound
		}
		for _, t := range found {
			if t.Number == sourceNumber {
				source = t
			} else {
				target = t
			}
		}

		if source.Number == target.Number {
			return ErrMergeSelf
		}
		if target.MergedIntoID != nil || target.Status == "closed" {
			return ErrMergeTargetInvalid
		}
		if source.MergedIntoID != nil {
			return ErrSourceMerged
		}
		if source.RequesterID != target.RequesterID {
			return ErrMergeRequester
		}

		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET merged_into_id = $3,
			     status         = 'closed',
			     closed_at      = now(),
			     version        = version + 1,
			     updated_at     = now()
			 WHERE org_id = $1 AND number = $2
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			orgID, sourceNumber, target.ID,
		).Scan(
			&source.ID, &source.OrgID, &source.Number, &source.RequesterID, &source.AssigneeID,
			&source.TeamID, &source.SLAPolicyID, &source.MergedIntoID, &source.Subject,
			&source.Status, &source.Priority, &source.Channel, &source.FirstResponseDueAt,
			&source.ResolutionDueAt, &source.FirstResponseAt, &source.ResolvedAt,
			&source.ClosedAt, &source.Version, &source.CreatedAt, &source.UpdatedAt, &source.DeletedAt,
		); err != nil {
			return fmt.Errorf("close source: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, body, is_internal)
			 VALUES ($1, $2, 'system', $3, false)`,
			orgID, source.ID, fmt.Sprintf("Merged into #%d", target.Number),
		); err != nil {
			return fmt.Errorf("source comment: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, body, is_internal)
			 VALUES ($1, $2, 'system', $3, false)`,
			orgID, target.ID, fmt.Sprintf("Merged from #%d", source.Number),
		); err != nil {
			return fmt.Errorf("target comment: %w", err)
		}

		if err := Record(ctx, tx, orgID, source.ID, actor, "ticket.merged", map[string]any{
			"into_number": target.Number,
			"into_id":     target.ID.String(),
		}); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			source.RequesterID,
		).Scan(&source.RequesterEmail, &source.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}
		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			target.RequesterID,
		).Scan(&target.RequesterEmail, &target.RequesterName); err != nil {
			return fmt.Errorf("load target requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, Ticket{}, err
	}
	return source, target, nil
}

// in the Model it was Actions, not body
// but in the DB its body, so now body fetched from the DB is assigned into
// actions field in internal/tickets/models.go
func (r *Repository) CreateMacro(ctx context.Context, orgID, createdBy uuid.UUID, name string, actions []byte) (Macro, error) {
	var m Macro
	err := r.pool.QueryRow(ctx,
		`INSERT INTO macros (org_id, name, body, created_by)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, org_id, name, body, created_by, created_at, updated_at`,
		orgID, name, actions, createdBy,
	).Scan(&m.ID, &m.OrgID, &m.Name, &m.Actions, &m.CreatedBy, &m.CreatedAT, &m.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Macro{}, ErrMacroNameTaken
		}
		return Macro{}, fmt.Errorf("create macro: %w", err)
	}
	return m, nil
}

func (r *Repository) ListMacros(ctx context.Context, orgID uuid.UUID) ([]Macro, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, body, created_by, created_at, updated_at
		 FROM macros WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list macros: %w", err)
	}
	defer rows.Close()

	var out []Macro
	for rows.Next() {
		var m Macro
		if err := rows.Scan(&m.ID, &m.OrgID, &m.Name, &m.Actions, &m.CreatedBy, &m.CreatedAT, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan macro: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) GetMacro(ctx context.Context, orgID, macroID uuid.UUID) (Macro, error) {
	var m Macro
	err := r.pool.QueryRow(ctx,
		`SELECT id, org_id, name, body, created_by, created_at, updated_at
		 FROM macros WHERE org_id = $1 AND id = $2`,
		orgID, macroID,
	).Scan(&m.ID, &m.OrgID, &m.Name, &m.Actions, &m.CreatedBy, &m.CreatedAT, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Macro{}, ErrMacroNotFound
		}
		return Macro{}, fmt.Errorf("get macro: %w", err)
	}
	return m, nil
}

func (r *Repository) UpdateMacro(ctx context.Context, orgID, macroID uuid.UUID, name string, actions []byte) (Macro, error) {
	var m Macro
	err := r.pool.QueryRow(ctx,
		`UPDATE macros SET name = $3, body = $4, updated_at = now()
		 WHERE org_id = $1 AND id = $2
		 RETURNING id, org_id, name, body, created_by, created_at, updated_at`,
		orgID, macroID, name, actions,
	).Scan(&m.ID, &m.OrgID, &m.Name, &m.Actions, &m.CreatedBy, &m.CreatedAT, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Macro{}, ErrMacroNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Macro{}, ErrMacroNameTaken
		}
		return Macro{}, fmt.Errorf("update macro: %w", err)
	}
	return m, nil
}

func (r *Repository) DeleteMacro(ctx context.Context, orgID, macroID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM macros WHERE org_id = $1 AND id = $2`,
		orgID, macroID,
	)
	if err != nil {
		return fmt.Errorf("delete macro: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMacroNotFound
	}
	return nil
}

func (r *Repository) CreateInvitation(ctx context.Context, in Invitation) (Invitation, error) {
	var out Invitation
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = r.CreateInvitationTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return Invitation{}, err
	}
	return out, nil
}

func (r *Repository) CreateInvitationTx(ctx context.Context, tx pgx.Tx, in Invitation) (Invitation, error) {
	var out Invitation
	err := tx.QueryRow(ctx,
		`INSERT INTO invitations (org_id, email, role, token_hash, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, org_id, email, role, token_hash, invited_by, expires_at, accepted_at, created_at`,
		in.OrgID, in.Email, in.Role, in.TokenHash, in.InvitedBy, in.ExpiresAt,
	).Scan(&out.ID, &out.OrgID, &out.Email, &out.Role, &out.TokenHash,
		&out.InvitedBy, &out.ExpiresAt, &out.AcceptedAt, &out.CreatedAt)
	if err != nil {
		return Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	return out, nil
}

func (r *Repository) ListInvitations(ctx context.Context, orgID uuid.UUID) ([]Invitation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, email, role, token_hash, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > now()
		 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	var out []Invitation
	for rows.Next() {
		var i Invitation
		if err := rows.Scan(&i.ID, &i.OrgID, &i.Email, &i.Role, &i.TokenHash,
			&i.InvitedBy, &i.ExpiresAt, &i.AcceptedAt, &i.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteInvitation(ctx context.Context, orgID, invitationID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM invitations WHERE org_id = $1 AND id = $2`,
		orgID, invitationID,
	)
	if err != nil {
		return fmt.Errorf("delete invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvitationNotFound
	}
	return nil
}

// AcceptInvitation runs the accept flow: lock the invitation, then either
// create the user + membership (new user) or just the membership (existing
// user). Duplicate membership returns ErrDuplicateMembership.
func (r *Repository) AcceptInvitation(ctx context.Context, tokenHash string, existingUserID *uuid.UUID, name, passwordHash string) (auth.User, auth.Organization, auth.Membership, error) {
	var (
		outUser auth.User
		outOrg  auth.Organization
		outMem  auth.Membership
	)

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var inv Invitation
		if err := tx.QueryRow(ctx,
			`SELECT id, org_id, email, role, token_hash, invited_by, expires_at, accepted_at, created_at
			 FROM invitations
			 WHERE token_hash = $1
			 FOR UPDATE`,
			tokenHash,
		).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.TokenHash,
			&inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvitationNotFound
			}
			return fmt.Errorf("lock invitation: %w", err)
		}

		if inv.AcceptedAt != nil {
			return ErrInvitationUsed
		}
		if time.Now().After(inv.ExpiresAt) {
			return ErrInvitationExpired
		}

		if existingUserID != nil {
			// existing user path: verify email matches, create membership only
			var email string
			if err := tx.QueryRow(ctx,
				`SELECT email FROM users WHERE id = $1`, *existingUserID,
			).Scan(&email); err != nil {
				return fmt.Errorf("find user: %w", err)
			}
			if email != inv.Email {
				return ErrInvitationEmailMismatch
			}

			if err := tx.QueryRow(ctx,
				`SELECT id, email, password_hash, name, created_at, updated_at
				 FROM users WHERE id = $1`, *existingUserID,
			).Scan(&outUser.ID, &outUser.Email, &outUser.PasswordHash, &outUser.Name,
				&outUser.CreatedAt, &outUser.UpdatedAt); err != nil {
				return fmt.Errorf("load user: %w", err)
			}
		} else {
			// new user path: create user
			if err := tx.QueryRow(ctx,
				`INSERT INTO users (email, password_hash, name)
				 VALUES ($1, $2, $3)
				 RETURNING id, email, password_hash, name, created_at, updated_at`,
				inv.Email, passwordHash, name,
			).Scan(&outUser.ID, &outUser.Email, &outUser.PasswordHash, &outUser.Name,
				&outUser.CreatedAt, &outUser.UpdatedAt); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" {
					return auth.ErrEmailTaken
				}
				return fmt.Errorf("insert user: %w", err)
			}
		}

		// membership
		if err := tx.QueryRow(ctx,
			`INSERT INTO memberships (org_id, user_id, role)
			 VALUES ($1, $2, $3)
			 RETURNING id, org_id, user_id, role, created_at`,
			inv.OrgID, outUser.ID, inv.Role,
		).Scan(&outMem.ID, &outMem.OrgID, &outMem.UserID, &outMem.Role, &outMem.CreatedAt); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrDuplicateMembership
			}
			return fmt.Errorf("insert membership: %w", err)
		}

		// mark invitation accepted
		if _, err := tx.Exec(ctx,
			`UPDATE invitations SET accepted_at = now() WHERE id = $1`,
			inv.ID,
		); err != nil {
			return fmt.Errorf("mark accepted: %w", err)
		}

		// load org for the response
		if err := tx.QueryRow(ctx,
			`SELECT id, name, slug, plan, created_at, updated_at
			 FROM organizations WHERE id = $1`, inv.OrgID,
		).Scan(&outOrg.ID, &outOrg.Name, &outOrg.Slug, &outOrg.Plan,
			&outOrg.CreatedAt, &outOrg.UpdatedAt); err != nil {
			return fmt.Errorf("load org: %w", err)
		}

		return nil
	})
	if err != nil {
		return auth.User{}, auth.Organization{}, auth.Membership{}, err
	}
	return outUser, outOrg, outMem, nil
}

func (r *Repository) ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT u.id, u.email, u.name, m.role, m.created_at
		 FROM memberships m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.org_id = $1 AND u.deleted_at IS NULL
		 ORDER BY u.name, u.email`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// UpdateMemberRole changes a member's role. Refuses to demote the last owner.
func (r *Repository) UpdateMemberRole(ctx context.Context, orgID, userID uuid.UUID, newRole string) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		return r.UpdateMemberRoleTx(ctx, tx, orgID, userID, newRole)
	})
}

func (r *Repository) UpdateMemberRoleTx(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID, newRole string) error {
	var currentRole string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships
		 WHERE org_id = $1 AND user_id = $2
		 FOR UPDATE`,
		orgID, userID,
	).Scan(&currentRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberNotFound
		}
		return fmt.Errorf("lock membership: %w", err)
	}

	if currentRole == newRole {
		return nil
	}

	if currentRole == "owner" && newRole != "owner" {
		rows, err := tx.Query(ctx,
			`SELECT 1 FROM memberships
			 WHERE org_id = $1 AND role = 'owner'
			 FOR UPDATE`,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("lock owners: %w", err)
		}
		owners := 0
		for rows.Next() {
			owners++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows: %w", err)
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE memberships SET role = $3 WHERE org_id = $1 AND user_id = $2`,
		orgID, userID, newRole,
	); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	return nil
}

// RemoveMember deletes a membership and revokes the user's refresh tokens
// for this org. Refuses to remove the last owner.
func (r *Repository) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		return r.RemoveMemberTx(ctx, tx, orgID, userID)
	})
}

func (r *Repository) RemoveMemberTx(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID) error {
	var currentRole string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships
		 WHERE org_id = $1 AND user_id = $2
		 FOR UPDATE`,
		orgID, userID,
	).Scan(&currentRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberNotFound
		}
		return fmt.Errorf("lock membership: %w", err)
	}

	if currentRole == "owner" {
		rows, err := tx.Query(ctx,
			`SELECT 1 FROM memberships
			 WHERE org_id = $1 AND role = 'owner'
			 FOR UPDATE`,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("lock owners: %w", err)
		}
		owners := 0
		for rows.Next() {
			owners++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows: %w", err)
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`,
		orgID, userID,
	); err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE user_id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		userID, orgID,
	); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}

	return nil
}
func (r *Repository) CreateAPIKey(ctx context.Context, in APIKey) (APIKey, error) {
	var out APIKey
	err := r.pool.QueryRow(ctx,
		`INSERT INTO api_keys (org_id, name, prefix, key_hash, created_by, rate_limit_per_min, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, org_id, name, prefix, key_hash, created_by, rate_limit_per_min,
		           last_used_at, expires_at, revoked_at, created_at`,
		in.OrgID, in.Name, in.Prefix, in.KeyHash, in.CreatedBy, in.RateLimitPerMin, in.ExpiresAt,
	).Scan(&out.ID, &out.OrgID, &out.Name, &out.Prefix, &out.KeyHash, &out.CreatedBy,
		&out.RateLimitPerMin, &out.LastUsedAt, &out.ExpiresAt, &out.RevokedAt, &out.CreatedAt)
	if err != nil {
		return APIKey{}, fmt.Errorf("create api key: %w", err)
	}
	return out, nil
}

func (r *Repository) ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]APIKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, prefix, key_hash, created_by, rate_limit_per_min,
		        last_used_at, expires_at, revoked_at, created_at
		 FROM api_keys WHERE org_id = $1 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.KeyHash, &k.CreatedBy,
			&k.RateLimitPerMin, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) RevokeAPIKey(ctx context.Context, orgID, keyID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		return r.RevokeAPIKeyTx(ctx, tx, orgID, keyID)
	})
}

func (r *Repository) RevokeAPIKeyTx(ctx context.Context, tx pgx.Tx, orgID, keyID uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`UPDATE api_keys SET revoked_at = now()
		 WHERE org_id = $1 AND id = $2 AND revoked_at IS NULL`,
		orgID, keyID,
	)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// FindAPIKeyByPrefix returns the key row for a prefix. Used by the auth path.
func (r *Repository) FindAPIKeyByPrefix(ctx context.Context, prefix string) (APIKey, error) {
	var k APIKey
	err := r.pool.QueryRow(ctx,
		`SELECT id, org_id, name, prefix, key_hash, created_by, rate_limit_per_min,
		        last_used_at, expires_at, revoked_at, created_at
		 FROM api_keys WHERE prefix = $1`,
		prefix,
	).Scan(&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.KeyHash, &k.CreatedBy,
		&k.RateLimitPerMin, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, ErrAPIKeyNotFound
		}
		return APIKey{}, fmt.Errorf("find api key: %w", err)
	}
	return k, nil
}
