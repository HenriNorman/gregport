package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository persists delivery plans and loads the org-scoped ticket data they
// are derived from.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// LoadTicketContext reads the ticket, requester, assignee and org settings in
// one query. Soft-deleted tickets are treated as not found.
func (r *Repository) LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error) {
	var tc TicketContext
	err := r.pool.QueryRow(ctx,
		`SELECT t.org_id, t.id, t.number, t.subject, t.requester_id,
		        c.email, COALESCE(c.name, ''),
		        COALESCE(u.email, ''), COALESCE(u.name, ''),
		        o.name, COALESCE(o.settings->>'support_email', '')
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 JOIN organizations o ON o.id = t.org_id
		 LEFT JOIN users u ON u.id = t.assignee_id
		 WHERE t.id = $1 AND t.org_id = $2 AND t.deleted_at IS NULL`,
		ticketID, orgID,
	).Scan(
		&tc.OrgID, &tc.TicketID, &tc.Number, &tc.Subject, &tc.RequesterID,
		&tc.RequesterEmail, &tc.RequesterName,
		&tc.AssigneeEmail, &tc.AssigneeName,
		&tc.OrgName, &tc.SupportEmail,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TicketContext{}, ErrTicketNotFound
		}
		return TicketContext{}, fmt.Errorf("load ticket context: %w", err)
	}
	return tc, nil
}

// LoadComment reads the comment an event refers to. Deleted comments are
// treated as not found.
func (r *Repository) LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error) {
	var c CommentContext
	err := r.pool.QueryRow(ctx,
		`SELECT author_type, body, is_internal
		 FROM comments
		 WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		commentID, orgID,
	).Scan(&c.AuthorType, &c.Body, &c.IsInternal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CommentContext{}, ErrCommentNotFound
		}
		return CommentContext{}, fmt.Errorf("load comment: %w", err)
	}
	return c, nil
}

// InsertDelivery creates one pending delivery. The partial unique index over
// (event_id, to_email, template) makes this an upsert that returns the existing
// row when the same event is replayed, so the caller can safely retry.
func (r *Repository) InsertDelivery(ctx context.Context, orgID, eventID uuid.UUID, planned PlannedDelivery) (Delivery, error) {
	payload, err := json.Marshal(planned.Payload)
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal delivery payload: %w", err)
	}

	var d Delivery
	err = r.pool.QueryRow(ctx,
		`INSERT INTO email_deliveries (org_id, event_id, to_email, template, payload, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')
		 ON CONFLICT (event_id, to_email, template) WHERE event_id IS NOT NULL
		 DO UPDATE SET to_email = EXCLUDED.to_email
		 RETURNING id, org_id, event_id, to_email, template, payload, status, attempts, created_at`,
		orgID, eventID, planned.ToEmail, planned.Template, payload,
	).Scan(
		&d.ID, &d.OrgID, &d.EventID, &d.ToEmail, &d.Template,
		&d.Payload, &d.Status, &d.Attempts, &d.CreatedAt,
	)
	if err != nil {
		return Delivery{}, fmt.Errorf("insert delivery: %w", err)
	}
	return d, nil
}

// MarkSent records a successful delivery.
func (r *Repository) MarkSent(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE email_deliveries SET status = $1, sent_at = now() WHERE id = $2`,
		StatusSent, id,
	)
	if err != nil {
		return fmt.Errorf("mark delivery sent: %w", err)
	}
	return nil
}

// RecordAttempt records a delivery attempt. When attempts reach maxAttempts
// the delivery is dead; otherwise it is failed and retried later based on
// nextRetryAt. status must be "failed" or "dead".
func (r *Repository) RecordAttempt(ctx context.Context, id uuid.UUID, status string, attempts int, nextRetryAt *time.Time, lastError *string) error {
	if status != StatusFailed && status != StatusDead {
		return fmt.Errorf("invalid status %q", status)
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE email_deliveries
		 SET status = $1, attempts = $2, next_retry_at = $3, last_error = $4
		 WHERE id = $5`,
		status, attempts, nextRetryAt, lastError, id,
	)
	if err != nil {
		return fmt.Errorf("record delivery attempt: %w", err)
	}
	return nil
}

// PurgeOldDeliveries deletes email deliveries older than before, limited to
// batch rows. Sent and dead deliveries are eligible; pending and failed
// deliveries (which may still be retried) are kept.
func (r *Repository) PurgeOldDeliveries(ctx context.Context, before time.Time, batch int) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge time is required")
	}
	if batch <= 0 {
		batch = 100
	}
	tag, err := r.pool.Exec(ctx,
		`WITH to_delete AS (
		         SELECT id FROM email_deliveries
		         WHERE created_at <= $1
		           AND status IN ($2, $3)
		         LIMIT $4
		     )
		     DELETE FROM email_deliveries
		     WHERE id IN (SELECT id FROM to_delete)`,
		before, StatusSent, StatusDead, batch,
	)
	if err != nil {
		return 0, fmt.Errorf("purge old email deliveries: %w", err)
	}
	return tag.RowsAffected(), nil
}
