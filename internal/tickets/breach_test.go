package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func breachTestPool(t *testing.T) *pgxpool.Pool {
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

// seedBreachOrg seeds an org, customer and ticket with the given due dates and
// returns the org id, ticket id and customer id.
func seedBreachOrg(t *testing.T, pool *pgxpool.Pool, firstResponseDue, resolutionDue time.Time) (orgID, ticketID, customerID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Breach Org', $1) RETURNING id`,
		"breach-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, "breach-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
		                      sla_policy_id, first_response_due_at, resolution_due_at)
		 VALUES ($1, 1, $2, 'breach ticket', 'urgent', 'api', NULL, $3, $4)
		 RETURNING id`,
		orgID, customerID, firstResponseDue, resolutionDue,
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

func scanBreachColumns(t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) (firstRespBreached, resolutionBreached *time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT first_response_breached_at, resolution_breached_at
		 FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&firstRespBreached, &resolutionBreached); err != nil {
		t.Fatalf("scan breach columns: %v", err)
	}
	return
}

func countBreachEvents(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE org_id = $1 AND event_type = 'sla.breached'`,
		orgID,
	).Scan(&n); err != nil {
		t.Fatalf("count breach events: %v", err)
	}
	return n
}

func past(hours float64) time.Time {
	return time.Now().Add(-time.Duration(hours) * time.Hour)
}

func future(hours float64) time.Time {
	return time.Now().Add(time.Duration(hours) * time.Hour)
}

func TestBreachCheckerMarksFirstResponseAndResolution(t *testing.T) {
	pool := breachTestPool(t)
	orgID, ticketID, _ := seedBreachOrg(t, pool, past(1), past(0.5))

	checker := NewBreachChecker(NewRepository(pool))

	n, err := checker.CheckBreaches(context.Background(), 100)
	if err != nil {
		t.Fatalf("check breaches: %v", err)
	}
	if n != 1 {
		t.Fatalf("breaches marked: %d, want 1", n)
	}

	firstResp, resolution := scanBreachColumns(t, pool, ticketID)
	if firstResp == nil {
		t.Fatal("first_response_breached_at not set")
	}
	if resolution == nil {
		t.Fatal("resolution_breached_at not set")
	}

	if count := countBreachEvents(t, pool, orgID); count != 1 {
		t.Fatalf("breach events: %d, want 1", count)
	}
}

func TestBreachCheckerIsIdempotent(t *testing.T) {
	pool := breachTestPool(t)
	orgID, _, _ := seedBreachOrg(t, pool, past(1), past(0.5))

	checker := NewBreachChecker(NewRepository(pool))

	// First run marks both breaches.
	if _, err := checker.CheckBreaches(context.Background(), 100); err != nil {
		t.Fatalf("first check: %v", err)
	}

	// Second run must not mark anything or record another event.
	n, err := checker.CheckBreaches(context.Background(), 100)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if n != 0 {
		t.Fatalf("second check marked %d breaches, want 0", n)
	}
	if count := countBreachEvents(t, pool, orgID); count != 1 {
		t.Fatalf("breach events after second check: %d, want 1", count)
	}
}

func TestBreachCheckerSkipsRespondedTicket(t *testing.T) {
	pool := breachTestPool(t)
	_, ticketID, _ := seedBreachOrg(t, pool, past(1), future(1))

	// The requester already got a first response before the due date passed,
	// so no breach should be recorded.
	firstResponseAt := past(0.1)
	if _, err := pool.Exec(context.Background(),
		`UPDATE tickets SET first_response_at = $1 WHERE id = $2`,
		firstResponseAt, ticketID,
	); err != nil {
		t.Fatalf("set first response: %v", err)
	}

	checker := NewBreachChecker(NewRepository(pool))
	n, err := checker.CheckBreaches(context.Background(), 100)
	if err != nil {
		t.Fatalf("check breaches: %v", err)
	}
	if n != 0 {
		t.Fatalf("responded ticket marked %d breaches, want 0", n)
	}
}

func TestBreachCheckerSkipsResolvedTicket(t *testing.T) {
	pool := breachTestPool(t)
	_, ticketID, _ := seedBreachOrg(t, pool, future(1), past(0.5))

	// The ticket was resolved before the resolution due date passed,
	// so no resolution breach should be recorded.
	resolvedAt := past(0.1)
	if _, err := pool.Exec(context.Background(),
		`UPDATE tickets SET resolved_at = $1 WHERE id = $2`,
		resolvedAt, ticketID,
	); err != nil {
		t.Fatalf("set resolved: %v", err)
	}

	checker := NewBreachChecker(NewRepository(pool))
	n, err := checker.CheckBreaches(context.Background(), 100)
	if err != nil {
		t.Fatalf("check breaches: %v", err)
	}
	if n != 0 {
		t.Fatalf("resolved ticket marked %d breaches, want 0", n)
	}
}

func TestBreachCheckerSoftDeletedSkipped(t *testing.T) {
	pool := breachTestPool(t)
	_, ticketID, _ := seedBreachOrg(t, pool, past(1), past(0.5))

	deletedAt := time.Now()
	if _, err := pool.Exec(context.Background(),
		`UPDATE tickets SET deleted_at = $1 WHERE id = $2`,
		deletedAt, ticketID,
	); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	checker := NewBreachChecker(NewRepository(pool))
	n, err := checker.CheckBreaches(context.Background(), 100)
	if err != nil {
		t.Fatalf("check breaches: %v", err)
	}
	if n != 0 {
		t.Fatalf("deleted ticket marked %d breaches, want 0", n)
	}
}

func TestBreachCheckerConcurrentRunners(t *testing.T) {
	pool := breachTestPool(t)
	orgID, _, customerID := seedBreachOrg(t, pool, past(1), past(0.5))

	// Seed 20 more tickets past their due dates, all belonging to the same
	// customer so the requester_id foreign key is satisfied.
	for i := 2; i <= 20; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel,
			                      sla_policy_id, first_response_due_at, resolution_due_at)
			 VALUES ($1, $2, $3, $4, 'urgent', 'api', NULL, $5, $6)`,
			orgID, i, customerID, "concurrent breach ticket",
			past(1), past(0.5),
		); err != nil {
			t.Fatalf("seed ticket %d: %v", i, err)
		}
	}

	checker1 := NewBreachChecker(NewRepository(pool))
	checker2 := NewBreachChecker(NewRepository(pool))

	type result struct {
		n   int
		err error
	}
	resCh := make(chan result, 2)
	go func() {
		n, err := checker1.CheckBreaches(context.Background(), 50)
		resCh <- result{n, err}
	}()
	go func() {
		n, err := checker2.CheckBreaches(context.Background(), 50)
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

	// Exactly 20 tickets should be marked across both runners (no duplicates).
	if total != 20 {
		t.Fatalf("total breaches marked: %d, want 20", total)
	}

	var breached int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tickets WHERE org_id = $1
		 AND (first_response_breached_at IS NOT NULL OR resolution_breached_at IS NOT NULL)`,
		orgID,
	).Scan(&breached); err != nil {
		t.Fatalf("count breached: %v", err)
	}
	if breached != 20 {
		t.Fatalf("breached rows: %d, want 20", breached)
	}
	if count := countBreachEvents(t, pool, orgID); count != 20 {
		t.Fatalf("breach events: %d, want 20", count)
	}
}

func TestBreachTypesToStrings(t *testing.T) {
	got := breachTypesToStrings([]BreachType{BreachFirstResponse, BreachResolution})
	want := []string{"first_response", "resolution"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
