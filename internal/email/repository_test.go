package email

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func emailRepoTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedEmailOrg(t *testing.T, pool *pgxpool.Pool) (orgID, ticketID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, settings) VALUES ('Email Org', $1, '{"support_email":"support@example.com"}') RETURNING id`,
		"email-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, "customer-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject)
		 VALUES ($1, 1, $2, 'email test') RETURNING id`,
		orgID, customerID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM email_deliveries WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID, ticketID
}

// seedEvent inserts a ticket_events row so email_deliveries.event_id FK is
// satisfied. Returns the event id.
func seedEvent(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', $3, $4) RETURNING id`,
		orgID, ticketID, "ticket.created", `{"number":1}`,
	).Scan(&id); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return id
}

func TestRepositoryInsertDeliveryIsIdempotent(t *testing.T) {
	pool := emailRepoTestPool(t)
	orgID, ticketID := seedEmailOrg(t, pool)
	repo := NewRepository(pool)

	eventID := seedEvent(t, pool, orgID, ticketID)
	planned := PlannedDelivery{
		ToEmail:  "customer@example.com",
		Template: TemplateTicketCreated,
		Payload:  DeliveryPayload{EventType: "ticket.created", TicketNumber: 1},
	}

	d1, err := repo.InsertDelivery(context.Background(), orgID, eventID, planned)
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if d1.Status != StatusPending {
		t.Fatalf("expected pending, got %s", d1.Status)
	}

	// Replaying the same event returns the existing row (upsert), not a duplicate.
	d2, err := repo.InsertDelivery(context.Background(), orgID, eventID, planned)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if d2.ID != d1.ID {
		t.Fatalf("idempotent insert returned a different id: %s vs %s", d2.ID, d1.ID)
	}

	// A different recipient or template is a separate delivery.
	d3, err := repo.InsertDelivery(context.Background(), orgID, eventID,
		PlannedDelivery{ToEmail: "other@example.com", Template: TemplateTicketCreated, Payload: planned.Payload})
	if err != nil {
		t.Fatalf("insert 3: %v", err)
	}
	if d3.ID == d1.ID {
		t.Fatal("expected a different id for a different recipient")
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_deliveries WHERE org_id = $1`, orgID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 deliveries, got %d", count)
	}
}

func TestRepositoryMarkSentAndRecordAttempt(t *testing.T) {
	pool := emailRepoTestPool(t)
	orgID, ticketID := seedEmailOrg(t, pool)
	repo := NewRepository(pool)

	eventID := seedEvent(t, pool, orgID, ticketID)
	d, err := repo.InsertDelivery(context.Background(), orgID, eventID,
		PlannedDelivery{ToEmail: "customer@example.com", Template: TemplateTicketCreated,
			Payload: DeliveryPayload{EventType: "ticket.created", TicketNumber: 1}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := repo.MarkSent(context.Background(), d.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	var status string
	var sentAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT status, sent_at FROM email_deliveries WHERE id = $1`, d.ID,
	).Scan(&status, &sentAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != StatusSent || sentAt == nil {
		t.Fatalf("expected sent with sent_at, got status=%s sent_at=%v", status, sentAt)
	}

	// Record a failure on a different delivery.
	d2, err := repo.InsertDelivery(context.Background(), orgID, eventID,
		PlannedDelivery{ToEmail: "other@example.com", Template: TemplateTicketAssigned,
			Payload: DeliveryPayload{EventType: "ticket.assigned", TicketNumber: 1}})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	retryAt := time.Now().Add(2 * time.Minute)
	lastErr := "smtp: connection refused"
	if err := repo.RecordAttempt(context.Background(), d2.ID, StatusFailed, 2, &retryAt, &lastErr); err != nil {
		t.Fatalf("record failed attempt: %v", err)
	}

	var attempts int
	var nextRetry *time.Time
	var lastError *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, attempts, next_retry_at, last_error FROM email_deliveries WHERE id = $1`, d2.ID,
	).Scan(&status, &attempts, &nextRetry, &lastError); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != StatusFailed || attempts != 2 {
		t.Fatalf("expected failed/2, got %s/%d", status, attempts)
	}
	if nextRetry == nil || nextRetry.Before(retryAt.Add(-time.Second)) || nextRetry.After(retryAt.Add(time.Second)) {
		t.Fatalf("next_retry_at: %v", nextRetry)
	}
	if lastError == nil || *lastError != "smtp: connection refused" {
		t.Fatalf("last_error: %v", lastError)
	}

	// Mark it dead.
	if err := repo.RecordAttempt(context.Background(), d2.ID, StatusDead, 5, nil, &lastErr); err != nil {
		t.Fatalf("record dead: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT status, attempts FROM email_deliveries WHERE id = $1`, d2.ID,
	).Scan(&status, &attempts); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != StatusDead || attempts != 5 {
		t.Fatalf("expected dead/5, got %s/%d", status, attempts)
	}
}

func TestRepositoryLoadTicketContextMissing(t *testing.T) {
	pool := emailRepoTestPool(t)
	_, _ = seedEmailOrg(t, pool)
	repo := NewRepository(pool)

	_, err := repo.LoadTicketContext(context.Background(), uuid.New(), uuid.New())
	if err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
}

func TestRepositoryLoadCommentMissing(t *testing.T) {
	pool := emailRepoTestPool(t)
	_, _ = seedEmailOrg(t, pool)
	repo := NewRepository(pool)

	_, err := repo.LoadComment(context.Background(), uuid.New(), uuid.New())
	if err != ErrCommentNotFound {
		t.Fatalf("expected ErrCommentNotFound, got %v", err)
	}
}
