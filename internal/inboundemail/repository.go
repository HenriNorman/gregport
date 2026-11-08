package inboundemail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/database"
)

// Repo is the persistence and ticket/comment/customer contract the service
// needs. The real implementation lives in this package (Repository); tests
// substitute a fake. Tickets, comments and customers are created through
// this interface so inboundemail stays independent of the tickets package's
// internals.
type Repo interface {
	// InsertInbound records the received email. status is one of
	// received/ticket_created/comment_added/duplicate/rejected. It returns
	// ErrDuplicateMessageID when the Message-ID was already processed.
	InsertInbound(ctx context.Context, orgID uuid.UUID, e InboundEmail, status string, ticketID, commentID *uuid.UUID) error

	// FindByMessageID looks up a previously received email by Message-ID,
	// used to resolve In-Reply-To references.
	FindByMessageID(ctx context.Context, messageID string) (InboundEmailRow, error)

	// ResolveOrg returns the org that owns the inbound recipient address. The
	// recipient local part carries the org UUID (e.g. support+{org}@host).
	ResolveOrg(ctx context.Context, recipient string) (uuid.UUID, error)

	// FindTicketByNumber returns the ticket id for an org-scoped ticket number.
	FindTicketByNumber(ctx context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error)

	// FindOrCreateCustomer returns the customer for the sender email in the
	// org, creating one if absent.
	FindOrCreateCustomer(ctx context.Context, orgID uuid.UUID, email, name string) (uuid.UUID, error)

	// CreateTicket creates a new ticket (channel "email") from an inbound
	// email and records the first customer comment.
	CreateTicket(ctx context.Context, orgID, customerID uuid.UUID, fromEmail, fromName, subject, body string) (Ticket, error)

	// AddComment appends a public customer comment to an existing ticket.
	AddComment(ctx context.Context, orgID, ticketID, customerID uuid.UUID, body string) error
}

// Repository persists inbound email and delegates ticket/customer work to
// the tickets and organizations packages.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a PostgreSQL-backed inbound email repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// InsertInbound records the received email. On a duplicate Message-ID it
// returns ErrDuplicateMessageID without touching the row.
func (r *Repository) InsertInbound(ctx context.Context, orgID uuid.UUID, e InboundEmail, status string, ticketID, commentID *uuid.UUID) error {
	var ticketArg, commentArg any
	if ticketID != nil {
		ticketArg = *ticketID
	}
	if commentID != nil {
		commentArg = *commentID
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO inbound_emails
			(org_id, message_id, in_reply_to, from_email, from_name, recipient,
			 subject, body, status, ticket_id, comment_id, raw)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		orgID, e.MessageID, e.InReplyTo, e.FromEmail, e.FromName, e.Recipient,
		e.Subject, e.Body, status, ticketArg, commentArg, string(e.Raw),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateMessageID
		}
	}
	return err
}

// FindByMessageID looks up a previously received email by Message-ID.
func (r *Repository) FindByMessageID(ctx context.Context, messageID string) (InboundEmailRow, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, org_id, message_id, in_reply_to, from_email, from_name,
		       recipient, subject, body, status, ticket_id, comment_id,
		       received_at
		   FROM inbound_emails
		   WHERE message_id = $1`,
		messageID,
	)
	return scanInboundRow(row)
}

// ResolveOrg parses the org UUID from the recipient local part. Supported
// forms: {org}@host and support+{org}@host.
func (r *Repository) ResolveOrg(ctx context.Context, recipient string) (uuid.UUID, error) {
	local := recipient
	if at := strings.LastIndexByte(local, '@'); at >= 0 {
		local = local[:at]
	}
	if plus := strings.LastIndexByte(local, '+'); plus >= 0 {
		local = local[plus+1:]
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return uuid.Nil, ErrOrgNotFound
	}
	orgID, err := uuid.Parse(local)
	if err != nil {
		return uuid.Nil, ErrOrgNotFound
	}
	// Confirm the org actually exists.
	var id uuid.UUID
	if err := r.pool.QueryRow(ctx, `SELECT id FROM organizations WHERE id = $1`, orgID).Scan(&id); err != nil {
		return uuid.Nil, ErrOrgNotFound
	}
	return id, nil
}

// FindTicketByNumber returns the ticket id for an org-scoped ticket number.
func (r *Repository) FindTicketByNumber(ctx context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error) {
	var id uuid.UUID
	if err := r.pool.QueryRow(ctx,
		`SELECT id FROM tickets WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
		orgID, number,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrTicketNotFound
		}
		return uuid.Nil, err
	}
	return id, nil
}

// FindOrCreateCustomer upserts the sender as a customer of the org.
func (r *Repository) FindOrCreateCustomer(ctx context.Context, orgID uuid.UUID, email, name string) (uuid.UUID, error) {
	var id uuid.UUID
	var nameArg any
	if name != "" {
		nameArg = name
	}
	err := r.pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, email)
		 DO UPDATE SET name = COALESCE(EXCLUDED.name, customers.name), updated_at = now()
		 RETURNING id`,
		orgID, email, nameArg,
	).Scan(&id)
	return id, err
}

// CreateTicket creates an email-sourced ticket and its first comment.
func (r *Repository) CreateTicket(ctx context.Context, orgID, customerID uuid.UUID, fromEmail, fromName, subject, body string) (Ticket, error) {
	var tk Ticket
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var number int64
		if err := tx.QueryRow(ctx,
			`UPDATE organizations
			 SET ticket_counter = ticket_counter + 1, updated_at = now()
			 WHERE id = $1
			 RETURNING ticket_counter`,
			orgID,
		).Scan(&number); err != nil {
			return fmt.Errorf("increment counter: %w", err)
		}

		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
			                      status)
			 VALUES ($1, $2, $3, $4, 'normal', 'email', 'new')
			 RETURNING id`,
			orgID, number, customerID, subject,
		).Scan(&ticketID); err != nil {
			return fmt.Errorf("insert ticket: %w", err)
		}

		var commentID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_customer_id, body, is_internal)
			 VALUES ($1, $2, 'customer', $3, $4, false)
			 RETURNING id`,
			orgID, ticketID, customerID, body,
		).Scan(&commentID); err != nil {
			return fmt.Errorf("insert first comment: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_events (org_id, ticket_id, actor_type, actor_id, event_type, data)
			 VALUES ($1, $2, 'customer', $3, 'ticket.created',
			         jsonb_build_object('number', $4, 'subject', $5, 'priority', 'normal', 'channel', 'email'))`,
			orgID, ticketID, customerID, number, subject,
		); err != nil {
			return fmt.Errorf("record event: %w", err)
		}

		tk.ID = ticketID
		tk.Number = number
		return nil
	})
	return tk, err
}

// AddComment appends a public customer comment to an existing ticket.
func (r *Repository) AddComment(ctx context.Context, orgID, ticketID, customerID uuid.UUID, body string) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var commentID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_customer_id, body, is_internal)
			 VALUES ($1, $2, 'customer', $3, $4, false)
			 RETURNING id`,
			orgID, ticketID, customerID, body,
		).Scan(&commentID); err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tickets SET updated_at = now() WHERE id = $1`, ticketID,
		); err != nil {
			return fmt.Errorf("bump ticket: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_events (org_id, ticket_id, actor_type, actor_id, event_type, data)
			 VALUES ($1, $2, 'customer', $3, 'comment.added',
			         jsonb_build_object('comment_id', $4))`,
			orgID, ticketID, customerID, commentID,
		); err != nil {
			return fmt.Errorf("record event: %w", err)
		}
		return nil
	})
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInboundRow(row rowScanner) (InboundEmailRow, error) {
	var e InboundEmailRow
	var ticketID, commentID sql.NullString
	var receivedAt sql.NullTime
	err := row.Scan(
		&e.ID, &e.OrgID, &e.MessageID, &e.InReplyTo, &e.FromEmail, &e.FromName,
		&e.Recipient, &e.Subject, &e.Body, &e.Status, &ticketID, &commentID,
		&receivedAt,
	)
	if err != nil {
		return InboundEmailRow{}, err
	}
	if ticketID.Valid {
		if id, perr := uuid.Parse(ticketID.String); perr == nil {
			e.TicketID = &id
		}
	}
	if commentID.Valid {
		if id, perr := uuid.Parse(commentID.String); perr == nil {
			e.CommentID = &id
		}
	}
	if receivedAt.Valid {
		e.ReceivedAt = receivedAt.Time
	}
	return e, nil
}

var _ = time.Time{}
