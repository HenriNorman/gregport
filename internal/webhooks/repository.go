package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract the service and task handler use.
type Store interface {
	List(ctx context.Context, orgID uuid.UUID) ([]Subscription, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Subscription, error)
	Create(ctx context.Context, orgID uuid.UUID, sub Subscription) (Subscription, error)
	Update(ctx context.Context, orgID uuid.UUID, sub Subscription, resetFailures bool) (Subscription, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	ListActiveForEvent(ctx context.Context, orgID uuid.UUID, eventType string) ([]Subscription, error)
	InsertDelivery(ctx context.Context, d Delivery) (Delivery, bool, error)
	LoadDeliveryForAttempt(ctx context.Context, deliveryID uuid.UUID) (Delivery, Subscription, error)
	GetDeliveryScoped(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error)
	ListDeliveries(ctx context.Context, orgID, subscriptionID uuid.UUID, limit int, cursor string) ([]Delivery, string, error)
	RecordDeliverySuccess(ctx context.Context, d Delivery) error
	RecordDeliveryFailure(ctx context.Context, d Delivery, failureThreshold int, countSubscriptionFailure bool) (bool, error)
	ResetDeliveryForRedelivery(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error)
	LoadTicketSummary(ctx context.Context, orgID, ticketID uuid.UUID) (TicketSummary, error)
}

// Repository persists subscriptions and their delivery log.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a PostgreSQL-backed webhook repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const subscriptionColumns = `id, org_id, url, secret, events, is_active,
	consecutive_failures, last_failure_at, disabled_at, created_at, updated_at`

// List returns subscriptions for orgID in creation order.
func (r *Repository) List(ctx context.Context, orgID uuid.UUID) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+subscriptionColumns+`
		 FROM webhook_subscriptions
		 WHERE org_id = $1
		 ORDER BY created_at, id`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	out := make([]Subscription, 0)
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// Get returns one subscription scoped to orgID.
func (r *Repository) Get(ctx context.Context, orgID, id uuid.UUID) (Subscription, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+subscriptionColumns+`
		 FROM webhook_subscriptions
		 WHERE org_id = $1 AND id = $2`,
		orgID, id,
	)
	sub, err := scanSubscription(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrWebhookNotFound
	}
	return sub, err
}

// Create inserts a subscription.
func (r *Repository) Create(ctx context.Context, orgID uuid.UUID, sub Subscription) (Subscription, error) {
	events, err := json.Marshal(sub.Events)
	if err != nil {
		return Subscription{}, fmt.Errorf("marshal webhook events: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO webhook_subscriptions (org_id, url, secret, events, is_active)
		 VALUES ($1, $2, $3, $4, true)
		 RETURNING `+subscriptionColumns,
		orgID, sub.URL, sub.Secret, events,
	)
	created, err := scanSubscription(row)
	if err != nil {
		return Subscription{}, fmt.Errorf("create webhook: %w", err)
	}
	return created, nil
}

// Update replaces the mutable fields of a subscription. resetFailures is set
// when a disabled subscription is explicitly re-enabled, so its circuit
// breaker starts closed again.
func (r *Repository) Update(ctx context.Context, orgID uuid.UUID, sub Subscription, resetFailures bool) (Subscription, error) {
	events, err := json.Marshal(sub.Events)
	if err != nil {
		return Subscription{}, fmt.Errorf("marshal webhook events: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`UPDATE webhook_subscriptions
		 SET url = $3,
		     events = $4,
		     is_active = $5,
		     consecutive_failures = CASE WHEN $6 THEN 0 ELSE consecutive_failures END,
		     last_failure_at = CASE WHEN $6 THEN NULL ELSE last_failure_at END,
		     disabled_at = CASE WHEN $6 THEN NULL ELSE disabled_at END,
		     updated_at = now()
		 WHERE org_id = $1 AND id = $2
		 RETURNING `+subscriptionColumns,
		orgID, sub.ID, sub.URL, events, sub.IsActive, resetFailures,
	)
	updated, err := scanSubscription(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrWebhookNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("update webhook: %w", err)
	}
	return updated, nil
}

// Delete removes a subscription scoped to orgID. Its deliveries are removed by
// the ON DELETE CASCADE foreign key.
func (r *Repository) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM webhook_subscriptions WHERE org_id = $1 AND id = $2`,
		orgID, id,
	)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// ListActiveForEvent returns the active subscriptions in orgID that include
// eventType. The JSONB ? operator checks membership in the events array.
func (r *Repository) ListActiveForEvent(ctx context.Context, orgID uuid.UUID, eventType string) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+subscriptionColumns+`
		 FROM webhook_subscriptions
		 WHERE org_id = $1 AND is_active AND events ? $2
		 ORDER BY created_at, id`,
		orgID, eventType,
	)
	if err != nil {
		return nil, fmt.Errorf("list active webhooks: %w", err)
	}
	defer rows.Close()

	out := make([]Subscription, 0)
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// InsertDelivery creates one pending delivery. The unique index over
// (subscription_id, event_id) makes replay converge on the existing row; the
// bool reports whether a new row was inserted.
func (r *Repository) InsertDelivery(ctx context.Context, d Delivery) (Delivery, bool, error) {
	var eventArg any
	if d.EventID != nil {
		eventArg = *d.EventID
	}
	payload := d.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	var stored Delivery
	var eventID sql.NullString
	var responseStatus sql.NullInt32
	var responseBody, lastError sql.NullString
	var nextRetryAt, deliveredAt sql.NullTime
	var durationMS sql.NullInt32
	err := r.pool.QueryRow(ctx,
		`INSERT INTO webhook_deliveries
		   (org_id, subscription_id, event_id, event_type, payload, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')
		 ON CONFLICT (subscription_id, event_id) WHERE event_id IS NOT NULL DO NOTHING
		 RETURNING id, org_id, subscription_id, event_id, event_type, payload, status,
		           attempts, response_status, response_body, last_error,
		           next_retry_at, delivered_at, duration_ms, created_at`,
		d.OrgID, d.SubscriptionID, eventArg, d.EventType, payload,
	).Scan(
		&stored.ID, &stored.OrgID, &stored.SubscriptionID, &eventID, &stored.EventType,
		&stored.Payload, &stored.Status, &stored.Attempts, &responseStatus,
		&responseBody, &lastError, &nextRetryAt, &deliveredAt, &durationMS, &stored.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// The delivery already exists; return it and report inserted=false.
		if d.EventID == nil {
			return Delivery{}, false, fmt.Errorf("insert webhook delivery: no row returned")
		}
		row := r.pool.QueryRow(ctx,
			`SELECT id, org_id, subscription_id, event_id, event_type, payload, status,
			        attempts, response_status, response_body, last_error,
			        next_retry_at, delivered_at, duration_ms, created_at
			 FROM webhook_deliveries
			 WHERE subscription_id = $1 AND event_id = $2`,
			d.SubscriptionID, *d.EventID,
		)
		existing, scanErr := scanDelivery(row)
		if scanErr != nil {
			return Delivery{}, false, scanErr
		}
		return existing, false, nil
	}
	if err != nil {
		return Delivery{}, false, fmt.Errorf("insert webhook delivery: %w", err)
	}
	applyDeliveryNulls(&stored, eventID, responseStatus, responseBody, lastError, nextRetryAt, deliveredAt, durationMS)
	return stored, true, nil
}

// LoadDeliveryForAttempt loads one delivery and its subscription. It is not
// org-scoped because the worker only holds the delivery id; the row's org
// travels with it.
func (r *Repository) LoadDeliveryForAttempt(ctx context.Context, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	return r.loadDelivery(ctx,
		`WHERE d.id = $1`, deliveryID,
	)
}

// GetDeliveryScoped loads a delivery belonging to one org and subscription.
func (r *Repository) GetDeliveryScoped(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	return r.loadDelivery(ctx,
		`WHERE d.org_id = $1 AND d.subscription_id = $2 AND d.id = $3`,
		orgID, subscriptionID, deliveryID,
	)
}

func (r *Repository) loadDelivery(ctx context.Context, where string, args ...any) (Delivery, Subscription, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT d.id, d.org_id, d.subscription_id, d.event_id, d.event_type, d.payload,
		        d.status, d.attempts, d.response_status, d.response_body, d.last_error,
		        d.next_retry_at, d.delivered_at, d.duration_ms, d.created_at,
		        s.id, s.org_id, s.url, s.secret, s.events, s.is_active,
		        s.consecutive_failures, s.last_failure_at, s.disabled_at,
		        s.created_at, s.updated_at
		 FROM webhook_deliveries d
		 JOIN webhook_subscriptions s ON s.id = d.subscription_id
		 `+where,
		args...,
	)
	d, s, err := scanDeliveryWithSubscription(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	if err != nil {
		return Delivery{}, Subscription{}, fmt.Errorf("load webhook delivery: %w", err)
	}
	return d, s, nil
}

// ListDeliveries returns deliveries for one subscription, newest first. The
// cursor is (created_at, id) for keyset pagination.
func (r *Repository) ListDeliveries(ctx context.Context, orgID, subscriptionID uuid.UUID, limit int, cursorStr string) ([]Delivery, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	cur, err := decodeCursor(cursorStr)
	if err != nil {
		return nil, "", err
	}

	args := []any{orgID, subscriptionID}
	where := "WHERE org_id = $1 AND subscription_id = $2"
	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, limit+1)

	rows, err := r.pool.Query(ctx,
		fmt.Sprintf(
			`SELECT id, org_id, subscription_id, event_id, event_type, payload, status,
			        attempts, response_status, response_body, last_error,
			        next_retry_at, delivered_at, duration_ms, created_at
			 FROM webhook_deliveries
			 %s
			 ORDER BY created_at DESC, id DESC
			 LIMIT $%d`,
			where, len(args),
		),
		args...,
	)
	if err != nil {
		return nil, "", fmt.Errorf("list webhook deliveries: %w", err)
	}
	defer rows.Close()

	out := make([]Delivery, 0)
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, d)
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

// RecordDeliverySuccess marks a delivery sent and closes the subscription's
// circuit breaker.
func (r *Repository) RecordDeliverySuccess(ctx context.Context, d Delivery) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin webhook success: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = 'sent',
		     attempts = $2,
		     response_status = $3,
		     response_body = $4,
		     last_error = NULL,
		     next_retry_at = NULL,
		     delivered_at = now(),
		     duration_ms = $5
		 WHERE id = $1`,
		d.ID, d.Attempts, d.ResponseStatus, d.ResponseBody, d.DurationMS,
	); err != nil {
		return fmt.Errorf("mark webhook delivery sent: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE webhook_subscriptions
		 SET consecutive_failures = 0, last_failure_at = NULL, updated_at = now()
		 WHERE id = $1 AND consecutive_failures > 0`,
		d.SubscriptionID,
	); err != nil {
		return fmt.Errorf("reset webhook failures: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit webhook success: %w", err)
	}
	return nil
}

// RecordDeliveryFailure records a failed attempt. When the delivery reaches the
// terminal attempt it is marked dead. Otherwise it is left failed with a
// next_retry_at matching the asynq retry schedule. The subscription failure
// counter is incremented atomically; reaching failureThreshold disables the
// subscription and notifies its owners and admins once.
func (r *Repository) RecordDeliveryFailure(ctx context.Context, d Delivery, failureThreshold int, countSubscriptionFailure bool) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin webhook failure: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = $2,
		     attempts = $3,
		     response_status = $4,
		     response_body = $5,
		     last_error = $6,
		     next_retry_at = $7,
		     duration_ms = $8
		 WHERE id = $1`,
		d.ID, d.Status, d.Attempts, d.ResponseStatus, d.ResponseBody,
		d.LastError, d.NextRetryAt, d.DurationMS,
	); err != nil {
		return false, fmt.Errorf("record webhook failure: %w", err)
	}

	if failureThreshold <= 0 {
		failureThreshold = DefaultFailureThreshold
	}
	if !countSubscriptionFailure {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit webhook failure: %w", err)
		}
		return false, nil
	}
	var active bool
	var failures int
	var url string
	err = tx.QueryRow(ctx,
		`UPDATE webhook_subscriptions
		 SET consecutive_failures = consecutive_failures + 1,
		     last_failure_at = now(),
		     is_active = CASE WHEN consecutive_failures + 1 >= $3 THEN false ELSE is_active END,
		     disabled_at = CASE WHEN consecutive_failures + 1 >= $3 THEN now() ELSE disabled_at END,
		     updated_at = now()
		 WHERE id = $1 AND org_id = $2 AND is_active
		 RETURNING is_active, consecutive_failures, url`,
		d.SubscriptionID, d.OrgID, failureThreshold,
	).Scan(&active, &failures, &url)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("increment webhook failures: %w", err)
	}

	disabled := err == nil && !active
	if disabled {
		if _, err := tx.Exec(ctx,
			`INSERT INTO notifications (org_id, user_id, type, payload)
			 SELECT $1, m.user_id, 'webhook_disabled',
			        jsonb_build_object(
			          'webhook_id', $2::text,
			          'url', $3::text,
			          'consecutive_failures', $4::int
			        )
			 FROM memberships m
			 WHERE m.org_id = $1 AND m.role IN ('owner', 'admin')
			   AND NOT EXISTS (
			     SELECT 1 FROM notifications n
			     WHERE n.org_id = $1
			       AND n.user_id = m.user_id
			       AND n.type = 'webhook_disabled'
			       AND n.payload->>'webhook_id' = $2::text
			   )`,
			d.OrgID, d.SubscriptionID.String(), url, failures,
		); err != nil {
			return false, fmt.Errorf("notify webhook admins: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit webhook failure: %w", err)
	}
	return disabled, nil
}

// ResetDeliveryForRedelivery returns a delivery to pending and clears its
// previous attempt metadata so a manual redelivery starts from attempt one.
func (r *Repository) ResetDeliveryForRedelivery(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Delivery{}, Subscription{}, fmt.Errorf("begin webhook redelivery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = 'pending',
		     attempts = 0,
		     response_status = NULL,
		     response_body = NULL,
		     last_error = NULL,
		     next_retry_at = NULL,
		     delivered_at = NULL,
		     duration_ms = NULL
		 WHERE org_id = $1 AND subscription_id = $2 AND id = $3`,
		orgID, subscriptionID, deliveryID,
	)
	if err != nil {
		return Delivery{}, Subscription{}, fmt.Errorf("reset webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Delivery{}, Subscription{}, ErrDeliveryNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, Subscription{}, fmt.Errorf("commit webhook redelivery: %w", err)
	}
	return r.GetDeliveryScoped(ctx, orgID, subscriptionID, deliveryID)
}

// LoadTicketSummary reads the ticket snapshot embedded in an envelope.
// Soft-deleted tickets are included so a ticket.deleted webhook still carries
// context.
func (r *Repository) LoadTicketSummary(ctx context.Context, orgID, ticketID uuid.UUID) (TicketSummary, error) {
	var t TicketSummary
	var assignee sql.NullString
	err := r.pool.QueryRow(ctx,
		`SELECT id, number, subject, status, priority, requester_id, assignee_id
		 FROM tickets
		 WHERE id = $1 AND org_id = $2`,
		ticketID, orgID,
	).Scan(&t.ID, &t.Number, &t.Subject, &t.Status, &t.Priority, &t.RequesterID, &assignee)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketSummary{}, ErrNotFound
	}
	if err != nil {
		return TicketSummary{}, fmt.Errorf("load webhook ticket: %w", err)
	}
	if assignee.Valid {
		id, err := uuid.Parse(assignee.String)
		if err != nil {
			return TicketSummary{}, fmt.Errorf("parse assignee id: %w", err)
		}
		t.AssigneeID = &id
	}
	return t, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSubscription(row rowScanner) (Subscription, error) {
	var sub Subscription
	var events []byte
	var lastFailure, disabled sql.NullTime
	err := row.Scan(
		&sub.ID, &sub.OrgID, &sub.URL, &sub.Secret, &events, &sub.IsActive,
		&sub.ConsecutiveFailures, &lastFailure, &disabled, &sub.CreatedAt, &sub.UpdatedAt,
	)
	if err != nil {
		return Subscription{}, err
	}
	if err := json.Unmarshal(events, &sub.Events); err != nil {
		return Subscription{}, fmt.Errorf("decode webhook events: %w", err)
	}
	if lastFailure.Valid {
		sub.LastFailureAt = &lastFailure.Time
	}
	if disabled.Valid {
		sub.DisabledAt = &disabled.Time
	}
	return sub, nil
}

func scanDelivery(row rowScanner) (Delivery, error) {
	var d Delivery
	var eventID sql.NullString
	var responseStatus sql.NullInt32
	var responseBody, lastError sql.NullString
	var nextRetryAt, deliveredAt sql.NullTime
	var durationMS sql.NullInt32
	err := row.Scan(
		&d.ID, &d.OrgID, &d.SubscriptionID, &eventID, &d.EventType, &d.Payload,
		&d.Status, &d.Attempts, &responseStatus, &responseBody, &lastError,
		&nextRetryAt, &deliveredAt, &durationMS, &d.CreatedAt,
	)
	if err != nil {
		return Delivery{}, err
	}
	applyDeliveryNulls(&d, eventID, responseStatus, responseBody, lastError, nextRetryAt, deliveredAt, durationMS)
	return d, nil
}

func scanDeliveryWithSubscription(row rowScanner) (Delivery, Subscription, error) {
	var d Delivery
	var s Subscription
	var events []byte
	var eventID sql.NullString
	var responseStatus sql.NullInt32
	var responseBody, lastError sql.NullString
	var nextRetryAt, deliveredAt sql.NullTime
	var durationMS sql.NullInt32
	var lastFailure, disabled sql.NullTime
	err := row.Scan(
		&d.ID, &d.OrgID, &d.SubscriptionID, &eventID, &d.EventType, &d.Payload,
		&d.Status, &d.Attempts, &responseStatus, &responseBody, &lastError,
		&nextRetryAt, &deliveredAt, &durationMS, &d.CreatedAt,
		&s.ID, &s.OrgID, &s.URL, &s.Secret, &events, &s.IsActive,
		&s.ConsecutiveFailures, &lastFailure, &disabled, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return Delivery{}, Subscription{}, err
	}
	applyDeliveryNulls(&d, eventID, responseStatus, responseBody, lastError, nextRetryAt, deliveredAt, durationMS)
	if err := json.Unmarshal(events, &s.Events); err != nil {
		return Delivery{}, Subscription{}, fmt.Errorf("decode webhook events: %w", err)
	}
	if lastFailure.Valid {
		s.LastFailureAt = &lastFailure.Time
	}
	if disabled.Valid {
		s.DisabledAt = &disabled.Time
	}
	return d, s, nil
}

func applyDeliveryNulls(
	d *Delivery,
	eventID sql.NullString,
	responseStatus sql.NullInt32,
	responseBody, lastError sql.NullString,
	nextRetryAt, deliveredAt sql.NullTime,
	durationMS sql.NullInt32,
) {
	if eventID.Valid {
		id, err := uuid.Parse(eventID.String)
		if err == nil {
			d.EventID = &id
		}
	}
	if responseStatus.Valid {
		status := int(responseStatus.Int32)
		d.ResponseStatus = &status
	}
	if responseBody.Valid {
		d.ResponseBody = &responseBody.String
	}
	if lastError.Valid {
		d.LastError = &lastError.String
	}
	if nextRetryAt.Valid {
		d.NextRetryAt = &nextRetryAt.Time
	}
	if deliveredAt.Valid {
		d.DeliveredAt = &deliveredAt.Time
	}
	if durationMS.Valid {
		ms := int(durationMS.Int32)
		d.DurationMS = &ms
	}
}
