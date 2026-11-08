// Package notifications provides data access for in-app notifications.
package notifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TicketContext is the org-scoped ticket data needed to compute notification
// recipients. Recipients are always resolved from the database, never from the
// task payload.
type TicketContext struct {
	OrgID             uuid.UUID
	TicketID          uuid.UUID
	Number            int64
	Subject           string
	RequesterCustomer uuid.UUID
	RequesterEmail    string
	RequesterName     string
	AssigneeID        *uuid.UUID
	AssigneeEmail     string
	AssigneeName      string
}

// CommentContext is the comment a notification refers to.
type CommentContext struct {
	ID         uuid.UUID
	TicketID   uuid.UUID
	AuthorType string
	AuthorID   *uuid.UUID
	Body       string
	IsInternal bool
}

// Repo is the data access the notification service needs.
type Repo interface {
	LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error)
	LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error)
	ListWatchers(ctx context.Context, ticketID uuid.UUID) ([]uuid.UUID, error)
	InsertNotification(ctx context.Context, n Notification) (id uuid.UUID, inserted bool, err error)
	ListPreferences(ctx context.Context, userID uuid.UUID) ([]Preference, error)
	ReplacePreferences(ctx context.Context, userID uuid.UUID, prefs []Preference) error
	MarkRead(ctx context.Context, orgID, userID, notificationID uuid.UUID) error
	MarkAllRead(ctx context.Context, orgID, userID uuid.UUID) (int64, error)
	ListNotifications(ctx context.Context, orgID, userID uuid.UUID, unreadOnly bool, limit int, cursor string) ([]Notification, string, error)
}

// Repository persists and reads notifications.
type Repository struct {
	pool *pgxpool.Pool
}

var (
	ErrTicketNotFound       = errors.New("notifications: ticket not found")
	ErrCommentNotFound      = errors.New("notifications: comment not found")
	ErrNotificationNotFound = errors.New("notifications: notification not found")
)

// NewRepository builds a PostgreSQL-backed notification repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// LoadTicketContext loads the ticket data needed to compute notification
// recipients.
func (r *Repository) LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error) {
	var tc TicketContext
	var assigneeID sql.NullString
	var assigneeEmail sql.NullString
	var assigneeName sql.NullString
	err := r.pool.QueryRow(ctx,
		`SELECT t.org_id, t.id, t.number, t.subject,
		        t.requester_id, c.email, COALESCE(c.name, ''),
		        t.assignee_id, u.email, u.name
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 LEFT JOIN users u ON u.id = t.assignee_id
		 WHERE t.id = $1 AND t.org_id = $2 AND t.deleted_at IS NULL`,
		ticketID, orgID,
	).Scan(
		&tc.OrgID, &tc.TicketID, &tc.Number, &tc.Subject,
		&tc.RequesterCustomer, &tc.RequesterEmail, &tc.RequesterName,
		&assigneeID, &assigneeEmail, &assigneeName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TicketContext{}, ErrTicketNotFound
		}
		return TicketContext{}, fmt.Errorf("load ticket context: %w", err)
	}
	if assigneeID.Valid {
		id, err := uuid.Parse(assigneeID.String)
		if err != nil {
			return TicketContext{}, fmt.Errorf("parse assignee id: %w", err)
		}
		tc.AssigneeID = &id
		tc.AssigneeEmail = assigneeEmail.String
		tc.AssigneeName = assigneeName.String
	}
	return tc, nil
}

// LoadComment reads the comment a notification refers to. The author id is
// polymorphic: a comment is authored by either a user or a customer.
func (r *Repository) LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error) {
	var c CommentContext
	var authorUserID sql.NullString
	var authorCustomerID sql.NullString
	err := r.pool.QueryRow(ctx,
		`SELECT id, ticket_id, author_type, author_user_id, author_customer_id, body, is_internal
		 FROM comments
		 WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		commentID, orgID,
	).Scan(
		&c.ID, &c.TicketID, &c.AuthorType, &authorUserID, &authorCustomerID,
		&c.Body, &c.IsInternal,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CommentContext{}, ErrCommentNotFound
		}
		return CommentContext{}, fmt.Errorf("load comment: %w", err)
	}
	author := authorUserID
	if !author.Valid {
		author = authorCustomerID
	}
	if author.Valid {
		id, err := uuid.Parse(author.String)
		if err != nil {
			return CommentContext{}, fmt.Errorf("parse author id: %w", err)
		}
		c.AuthorID = &id
	}
	return c, nil
}

// ListWatchers returns the user IDs watching a ticket.
func (r *Repository) ListWatchers(ctx context.Context, ticketID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id FROM ticket_watchers WHERE ticket_id = $1`,
		ticketID,
	)
	if err != nil {
		return nil, fmt.Errorf("list watchers: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertNotification inserts one notification row. The unique (event_id,
// user_id) partial index makes this idempotent: replaying the same event for
// the same user creates no duplicate. The returned bool reports whether a new
// row was inserted (false when the dedupe key already existed).
func (r *Repository) InsertNotification(ctx context.Context, n Notification) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`INSERT INTO notifications (org_id, user_id, event_id, type, ticket_id, comment_id, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (event_id, user_id) WHERE event_id IS NOT NULL DO NOTHING
		 RETURNING id`,
		n.OrgID, n.UserID, n.EventID, n.Type, n.TicketID, n.CommentID, n.Payload,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING skipped the insert; find the existing row.
			if err := r.pool.QueryRow(ctx,
				`SELECT id FROM notifications WHERE event_id = $1 AND user_id = $2`,
				n.EventID, n.UserID,
			).Scan(&id); err != nil {
				return uuid.Nil, false, fmt.Errorf("find existing notification: %w", err)
			}
			return id, false, nil
		}
		return uuid.Nil, false, fmt.Errorf("insert notification: %w", err)
	}
	return id, true, nil
}

// ListPreferences returns the user's stored preference exceptions. Missing
// rows mean the default (enabled) applies.
func (r *Repository) ListPreferences(ctx context.Context, userID uuid.UUID) ([]Preference, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id, event_type, channel, enabled
		 FROM notification_preferences
		 WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list preferences: %w", err)
	}
	defer rows.Close()

	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.UserID, &p.EventType, &p.Channel, &p.Enabled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReplacePreferences replaces the user's stored preferences. The table stores
// only exceptions, so enabled rows are dropped and only disabled rows are
// persisted; anything not stored falls back to the default (enabled).
func (r *Repository) ReplacePreferences(ctx context.Context, userID uuid.UUID, prefs []Preference) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin preferences: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM notification_preferences WHERE user_id = $1`, userID,
	); err != nil {
		return fmt.Errorf("delete preferences: %w", err)
	}
	for _, p := range prefs {
		if p.Enabled {
			// Default is enabled; storing it would not be an exception.
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO notification_preferences (user_id, event_type, channel, enabled)
			 VALUES ($1, $2, $3, false)`,
			userID, p.EventType, p.Channel,
		); err != nil {
			return fmt.Errorf("insert preference: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit preferences: %w", err)
	}
	return nil
}

// MarkRead marks one notification as read. It is idempotent: marking an
// already-read notification succeeds, while an unknown or other-user
// notification returns ErrNotificationNotFound.
func (r *Repository) MarkRead(ctx context.Context, orgID, userID, notificationID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications
		 SET read_at = now()
		 WHERE id = $1 AND user_id = $2 AND org_id = $3 AND read_at IS NULL`,
		notificationID, userID, orgID,
	)
	if err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	// Either the notification is already read (idempotent success) or it does
	// not belong to this user/org.
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM notifications
		   WHERE id = $1 AND user_id = $2 AND org_id = $3
		 )`,
		notificationID, userID, orgID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check notification: %w", err)
	}
	if !exists {
		return ErrNotificationNotFound
	}
	return nil
}

// MarkAllRead marks all unread notifications for a user in one org as read.
func (r *Repository) MarkAllRead(ctx context.Context, orgID, userID uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications
		 SET read_at = now()
		 WHERE user_id = $1 AND org_id = $2 AND read_at IS NULL`,
		userID, orgID,
	)
	if err != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListNotifications returns notifications for a user in one org, newest
// first. When unreadOnly is true, only notifications with read_at IS NULL are
// returned. The cursor is (created_at, id) for keyset pagination.
func (r *Repository) ListNotifications(
	ctx context.Context,
	orgID, userID uuid.UUID,
	unreadOnly bool,
	limit int,
	cursorStr string,
) ([]Notification, string, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	cur, err := decodeCursor(cursorStr)
	if err != nil {
		return nil, "", err
	}

	args := []any{orgID, userID}
	where := "WHERE org_id = $1 AND user_id = $2"
	if unreadOnly {
		where += " AND read_at IS NULL"
	}
	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, limit+1)

	query := fmt.Sprintf(
		`SELECT id, org_id, user_id, type, ticket_id, comment_id, payload, read_at, created_at
		 FROM notifications
		 %s
		 ORDER BY created_at DESC, id DESC
		 LIMIT $%d`,
		where, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	var out []Notification
	for rows.Next() {
		var n Notification
		var ticketID sql.NullString
		var commentID sql.NullString
		var readAt sql.NullTime
		if err := rows.Scan(
			&n.ID, &n.OrgID, &n.UserID, &n.Type,
			&ticketID, &commentID, &n.Payload, &readAt, &n.CreatedAt,
		); err != nil {
			return nil, "", err
		}
		if ticketID.Valid {
			id, err := uuid.Parse(ticketID.String)
			if err != nil {
				return nil, "", fmt.Errorf("parse ticket id: %w", err)
			}
			n.TicketID = &id
		}
		if commentID.Valid {
			id, err := uuid.Parse(commentID.String)
			if err != nil {
				return nil, "", fmt.Errorf("parse comment id: %w", err)
			}
			n.CommentID = &id
		}
		if readAt.Valid {
			n.ReadAt = &readAt.Time
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var next string
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}
