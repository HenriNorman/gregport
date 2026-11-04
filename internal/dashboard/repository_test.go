package dashboard

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

func seedOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	var orgID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('Dashboard Org', $1) RETURNING id`,
		"dash-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

type ticketSeed struct {
	number     int64
	status     string
	priority   string
	assignee   *uuid.UUID
	dueMinutes int // relative to now; 0 means NULL
	deleted    bool
}

func insertTicket(t *testing.T, pool *pgxpool.Pool, orgID, customerID uuid.UUID, seed ticketSeed) {
	t.Helper()

	var assignee any
	if seed.assignee != nil {
		assignee = *seed.assignee
	}
	var due any
	if seed.dueMinutes != 0 {
		due = time.Now().Add(time.Duration(seed.dueMinutes) * time.Minute)
	}
	var deleted any
	if seed.deleted {
		deleted = time.Now()
	}

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tickets
		   (org_id, number, requester_id, assignee_id, subject, status, priority,
		    resolution_due_at, deleted_at)
		 VALUES ($1, $2, $3, $4, 'Dashboard ticket', $5::ticket_status, $6::ticket_priority, $7, $8)`,
		orgID, seed.number, customerID, assignee, seed.status, seed.priority, due, deleted,
	); err != nil {
		t.Fatalf("insert ticket %d: %v", seed.number, err)
	}
}

func TestRepositoryCounts(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	// Empty org returns all zeroes rather than NULL.
	empty, err := repo.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("empty counts: %v", err)
	}
	if empty != (Counts{}) {
		t.Fatalf("empty counts: %+v", empty)
	}

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name) VALUES ($1, $2, 'Customer') RETURNING id`,
		orgID, "dash-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	var assigneeID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', 'Agent') RETURNING id`,
		"dash-agent-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&assigneeID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, assigneeID)
	})

	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 1, status: "new", priority: "low", dueMinutes: -60,
	})
	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 2, status: "open", priority: "normal", assignee: &assigneeID, dueMinutes: 60,
	})
	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 3, status: "pending", priority: "high",
	})
	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 4, status: "solved", priority: "urgent", assignee: &assigneeID, dueMinutes: -120,
	})
	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 5, status: "closed", priority: "low", assignee: &assigneeID,
	})
	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 6, status: "new", priority: "urgent", deleted: true,
	})

	got, err := repo.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}

	want := Counts{
		Total:      5,
		Open:       3,
		Unassigned: 2,
		Overdue:    1,
		ByStatus:   StatusCounts{New: 1, Open: 1, Pending: 1, Solved: 1, Closed: 1},
		ByPriority: PriorityCounts{Low: 2, Normal: 1, High: 1, Urgent: 1},
	}
	if got != want {
		t.Fatalf("counts:\n got %+v\nwant %+v", got, want)
	}
}

func TestRepositoryCountsAreTenantScoped(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	ctx := context.Background()

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name) VALUES ($1, $2, 'Customer') RETURNING id`,
		orgA, "dash-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	insertTicket(t, pool, orgA, customerID, ticketSeed{
		number: 1, status: "new", priority: "urgent",
	})

	countsA, err := repo.Counts(ctx, orgA)
	if err != nil {
		t.Fatalf("counts A: %v", err)
	}
	if countsA.Total != 1 || countsA.Open != 1 || countsA.ByPriority.Urgent != 1 {
		t.Fatalf("org A counts: %+v", countsA)
	}

	countsB, err := repo.Counts(ctx, orgB)
	if err != nil {
		t.Fatalf("counts B: %v", err)
	}
	if countsB != (Counts{}) {
		t.Fatalf("org B saw org A tickets: %+v", countsB)
	}
}
