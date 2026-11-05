package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func autocloseTestPool(t *testing.T) *pgxpool.Pool {
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

// seedAutocloseOrg seeds an org, customer and a solved ticket resolved
// resolvedAt ago, returning org id, ticket id and customer id.
func seedAutocloseOrg(t *testing.T, pool *pgxpool.Pool, resolvedAt time.Time) (orgID, ticketID, customerID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Autoclose Org', $1) RETURNING id`,
		"autoclose-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, "autoclose-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
		                      sla_policy_id, status, resolved_at, first_response_at)
		 VALUES ($1, 1, $2, 'autoclose ticket', 'urgent', 'api', NULL,
		         'solved', $3, $4)
		 RETURNING id`,
		orgID, customerID, resolvedAt, resolvedAt.Add(-time.Minute),
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID, ticketID, customerID
}

func scanClosedAt(t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) (closedAt *time.Time, status string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT closed_at, status FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&closedAt, &status); err != nil {
		t.Fatalf("scan closed_at: %v", err)
	}
	return
}

func countClosedEvents(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE org_id = $1 AND event_type = 'ticket.closed'`,
		orgID,
	).Scan(&n); err != nil {
		t.Fatalf("count closed events: %v", err)
	}
	return n
}

func TestAutoCloseClosesAgedTicket(t *testing.T) {
	pool := autocloseTestPool(t)
	orgID, ticketID, _ := seedAutocloseOrg(t, pool, time.Now().Add(-8*24*time.Hour))

	checker := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)

	n, err := checker.CheckAutoClose(context.Background(), 100)
	if err != nil {
		t.Fatalf("check auto close: %v", err)
	}
	if n != 1 {
		t.Fatalf("tickets closed: %d, want 1", n)
	}

	closedAt, status := scanClosedAt(t, pool, ticketID)
	if closedAt == nil {
		t.Fatal("closed_at not set")
	}
	if status != "closed" {
		t.Fatalf("status = %q, want closed", status)
	}

	if count := countClosedEvents(t, pool, orgID); count != 1 {
		t.Fatalf("closed events: %d, want 1", count)
	}
}

func TestAutoCloseIsIdempotent(t *testing.T) {
	pool := autocloseTestPool(t)
	orgID, ticketID, _ := seedAutocloseOrg(t, pool, time.Now().Add(-8*24*time.Hour))

	checker := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)

	if _, err := checker.CheckAutoClose(context.Background(), 100); err != nil {
		t.Fatalf("first check: %v", err)
	}

	n, err := checker.CheckAutoClose(context.Background(), 100)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if n != 0 {
		t.Fatalf("second check closed %d tickets, want 0", n)
	}
	if count := countClosedEvents(t, pool, orgID); count != 1 {
		t.Fatalf("closed events after second check: %d, want 1", count)
	}

	_, status := scanClosedAt(t, pool, ticketID)
	if status != "closed" {
		t.Fatalf("status = %q, want closed", status)
	}
}

func TestAutoCloseSkipsNotYetAgedTicket(t *testing.T) {
	pool := autocloseTestPool(t)
	_, ticketID, _ := seedAutocloseOrg(t, pool, time.Now().Add(-1*time.Hour))

	checker := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)

	n, err := checker.CheckAutoClose(context.Background(), 100)
	if err != nil {
		t.Fatalf("check auto close: %v", err)
	}
	if n != 0 {
		t.Fatalf("young ticket closed %d, want 0", n)
	}

	closedAt, status := scanClosedAt(t, pool, ticketID)
	if closedAt != nil {
		t.Fatal("closed_at set for young ticket")
	}
	if status != "solved" {
		t.Fatalf("status = %q, want solved", status)
	}
}

func TestAutoCloseSkipsAlreadyClosedTicket(t *testing.T) {
	pool := autocloseTestPool(t)
	orgID, ticketID, _ := seedAutocloseOrg(t, pool, time.Now().Add(-8*24*time.Hour))

	closedAt := time.Now()
	if _, err := pool.Exec(context.Background(),
		`UPDATE tickets SET status = 'closed', closed_at = $1 WHERE id = $2`,
		closedAt, ticketID,
	); err != nil {
		t.Fatalf("pre-close: %v", err)
	}

	checker := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)
	n, err := checker.CheckAutoClose(context.Background(), 100)
	if err != nil {
		t.Fatalf("check auto close: %v", err)
	}
	if n != 0 {
		t.Fatalf("already closed ticket closed %d, want 0", n)
	}
	if count := countClosedEvents(t, pool, orgID); count != 0 {
		t.Fatalf("closed events: %d, want 0", count)
	}
}

func TestAutoCloseSkipsNonSolvedTicket(t *testing.T) {
	pool := autocloseTestPool(t)
	orgID, ticketID, _ := seedAutocloseOrg(t, pool, time.Now().Add(-8*24*time.Hour))

	if _, err := pool.Exec(context.Background(),
		`UPDATE tickets SET status = 'open', resolved_at = NULL WHERE id = $1`,
		ticketID,
	); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	checker := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)
	n, err := checker.CheckAutoClose(context.Background(), 100)
	if err != nil {
		t.Fatalf("check auto close: %v", err)
	}
	if n != 0 {
		t.Fatalf("open ticket closed %d, want 0", n)
	}
	if count := countClosedEvents(t, pool, orgID); count != 0 {
		t.Fatalf("closed events: %d, want 0", count)
	}
}

func TestAutoCloseConcurrentRunners(t *testing.T) {
	pool := autocloseTestPool(t)
	orgID, _, customerID := seedAutocloseOrg(t, pool, time.Now().Add(-8*24*time.Hour))

	for i := 2; i <= 15; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
			                      sla_policy_id, status, resolved_at, first_response_at)
			 VALUES ($1, $2, $3, $4, 'urgent', 'api', NULL,
			         'solved', $5, $6)`,
			orgID, i, customerID, "concurrent autoclose ticket",
			time.Now().Add(-8*24*time.Hour), time.Now().Add(-8*24*time.Hour).Add(-time.Minute),
		); err != nil {
			t.Fatalf("seed ticket %d: %v", i, err)
		}
	}

	checker1 := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)
	checker2 := NewAutoCloseChecker(NewRepository(pool), 7*24*time.Hour)

	type result struct {
		n   int
		err error
	}
	resCh := make(chan result, 2)
	go func() {
		n, err := checker1.CheckAutoClose(context.Background(), 50)
		resCh <- result{n, err}
	}()
	go func() {
		n, err := checker2.CheckAutoClose(context.Background(), 50)
		resCh <- result{n, err}
	}()

	var total int
	for i := 0; i < 2; i++ {
		r := <-resCh
		if r.err != nil {
			t.Fatalf("concurrent check: %v", r.err)
		}
		total += r.n
	}

	if total != 15 {
		t.Fatalf("total tickets closed: %d, want 15", total)
	}

	var closed int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tickets WHERE org_id = $1 AND status = 'closed'`,
		orgID,
	).Scan(&closed); err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if closed != 15 {
		t.Fatalf("closed rows: %d, want 15", closed)
	}
	if count := countClosedEvents(t, pool, orgID); count != 15 {
		t.Fatalf("closed events: %d, want 15", count)
	}
}
