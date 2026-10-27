package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
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

func TestRecord_InsertsEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// need a real org + ticket for the FKs
	var orgID, userID, ticketID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM organizations LIMIT 1`).Scan(&orgID); err != nil {
		t.Skipf("no org in DB: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("no user in DB: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject)
		 VALUES ($1, 999999, $2, 'event test') RETURNING id`,
		orgID, userID,
	).Scan(&ticketID); err != nil {
		// requester_id must be a customer, not a user — try to find one
		if err := pool.QueryRow(ctx, `SELECT id FROM customers LIMIT 1`).Scan(&userID); err != nil {
			t.Skipf("no customer in DB: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO tickets (org_id, number, requester_id, subject)
			 VALUES ($1, 999999, $2, 'event test') RETURNING id`,
			orgID, userID,
		).Scan(&ticketID); err != nil {
			t.Fatalf("insert ticket: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ticket_events WHERE ticket_id = $1`, ticketID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tickets WHERE id = $1`, ticketID)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := Record(ctx, tx, orgID, ticketID, Actor{Type: "user", ID: &userID}, "ticket.created", map[string]any{"subject": "event test"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_events WHERE ticket_id = $1 AND event_type = 'ticket.created'`, ticketID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 event, got %d", count)
	}
}
