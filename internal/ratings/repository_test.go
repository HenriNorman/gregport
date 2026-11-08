package ratings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/dashboard"
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

type fixture struct {
	orgID      uuid.UUID
	customerID uuid.UUID
	ticketID   uuid.UUID
}

func seedFixture(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()

	var f fixture
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Rating Org', $1) RETURNING id`,
		"rating-"+uuid.NewString()[:8],
	).Scan(&f.orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name)
		 VALUES ($1, $2, 'Ada Customer') RETURNING id`,
		f.orgID, uuid.NewString()+"@example.test",
	).Scan(&f.customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject, status)
		 VALUES ($1, $2, $3, 'Cannot log in', 'solved') RETURNING id`,
		f.orgID, 1_000_000+time.Now().UnixNano()%100_000, f.customerID,
	).Scan(&f.ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ticket_ratings WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tickets WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM customers WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, f.orgID)
	})
	return f
}

func TestRepositoryLoadRatableTicket(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	f := seedFixture(t, pool)
	ctx := context.Background()

	ticket, err := repo.LoadRatableTicket(ctx, f.ticketID, f.customerID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ticket.ID != f.ticketID || ticket.OrgID != f.orgID ||
		ticket.CustomerID != f.customerID || ticket.Status != "solved" {
		t.Fatalf("ticket: %+v", ticket)
	}

	// A token whose customer does not own the ticket is indistinguishable
	// from a missing ticket, so it cannot probe ticket ids.
	if _, err := repo.LoadRatableTicket(ctx, f.ticketID, uuid.New()); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("foreign customer err=%v", err)
	}
	if _, err := repo.LoadRatableTicket(ctx, uuid.New(), f.customerID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("missing ticket err=%v", err)
	}

	// A soft-deleted ticket cannot be rated.
	if _, err := pool.Exec(ctx, `UPDATE tickets SET deleted_at = now() WHERE id = $1`, f.ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadRatableTicket(ctx, f.ticketID, f.customerID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("deleted ticket err=%v", err)
	}
}

func TestRepositoryTenantIsolation(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	a := seedFixture(t, pool)
	b := seedFixture(t, pool)

	// Customer B cannot load tenant A's ticket even with the exact ticket id.
	if _, err := repo.LoadRatableTicket(ctx, a.ticketID, b.customerID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("cross-tenant load err=%v", err)
	}

	// Ratings are stored under the ticket's org, never a caller-supplied one.
	r, err := repo.InsertRating(ctx, a.orgID, a.ticketID, a.customerID, 5, "great")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if r.OrgID != a.orgID {
		t.Fatalf("org: %s", r.OrgID)
	}

	var countB int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_ratings WHERE org_id = $1`, b.orgID).Scan(&countB); err != nil {
		t.Fatal(err)
	}
	if countB != 0 {
		t.Fatalf("tenant B saw %d ratings", countB)
	}
}

func TestRepositoryInsertRatingIsUniquePerTicket(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	f := seedFixture(t, pool)
	ctx := context.Background()

	first, err := repo.InsertRating(ctx, f.orgID, f.ticketID, f.customerID, 5, "great")
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first.Score != 5 || first.Comment != "great" {
		t.Fatalf("rating: %+v", first)
	}

	if _, err := repo.InsertRating(ctx, f.orgID, f.ticketID, f.customerID, 1, "second"); !errors.Is(err, ErrAlreadyRated) {
		t.Fatalf("second insert err=%v, want ErrAlreadyRated", err)
	}

	var count int
	var score int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(score) FROM ticket_ratings WHERE ticket_id = $1`, f.ticketID,
	).Scan(&count, &score); err != nil {
		t.Fatal(err)
	}
	if count != 1 || score != 5 {
		t.Fatalf("rows=%d score=%d, want one unchanged rating", count, score)
	}
}

func TestRepositoryAverageScore(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	a := seedFixture(t, pool)
	b := seedFixture(t, pool)

	avg, count, err := repo.AverageScore(ctx, a.orgID)
	if err != nil {
		t.Fatalf("empty average: %v", err)
	}
	if avg != 0 || count != 0 {
		t.Fatalf("empty average=%v count=%d", avg, count)
	}

	tickets := []uuid.UUID{a.ticketID}
	for i := 0; i < 2; i++ {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO tickets (org_id, number, requester_id, subject, status)
			 VALUES ($1, $2, $3, 'Another ticket', 'solved') RETURNING id`,
			a.orgID, 2_000_000+int64(i)+time.Now().UnixNano()%100_000, a.customerID,
		).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, id)
	}
	for i, score := range []int{5, 4, 3} {
		if _, err := repo.InsertRating(ctx, a.orgID, tickets[i], a.customerID, score, ""); err != nil {
			t.Fatalf("insert %d: %v", score, err)
		}
	}

	avg, count, err = repo.AverageScore(ctx, a.orgID)
	if err != nil {
		t.Fatalf("average: %v", err)
	}
	if count != 3 || avg != 4 {
		t.Fatalf("average=%v count=%d, want 4 / 3", avg, count)
	}

	// Tenant B has its own average and is unaffected by tenant A.
	if avg, count, err := repo.AverageScore(ctx, b.orgID); err != nil || avg != 0 || count != 0 {
		t.Fatalf("tenant B average=%v count=%d err=%v", avg, count, err)
	}
	if _, err := repo.InsertRating(ctx, b.orgID, b.ticketID, b.customerID, 1, ""); err != nil {
		t.Fatal(err)
	}
	avgA, countA, err := repo.AverageScore(ctx, a.orgID)
	if err != nil || avgA != 4 || countA != 3 {
		t.Fatalf("tenant A average changed: %v / %d / %v", avgA, countA, err)
	}
}

func TestDashboardReportsAverageRating(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	dash := dashboard.NewRepository(pool)
	ctx := context.Background()

	f := seedFixture(t, pool)

	counts, err := dash.Counts(ctx, f.orgID)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if counts.AverageRating != 0 || counts.RatingCount != 0 {
		t.Fatalf("initial rating counts: %+v", counts)
	}

	if _, err := repo.InsertRating(ctx, f.orgID, f.ticketID, f.customerID, 5, ""); err != nil {
		t.Fatal(err)
	}

	var secondTicket uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject, status)
		 VALUES ($1, $2, $3, 'Second', 'solved') RETURNING id`,
		f.orgID, 3_000_000+time.Now().UnixNano()%100_000, f.customerID,
	).Scan(&secondTicket); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertRating(ctx, f.orgID, secondTicket, f.customerID, 2, ""); err != nil {
		t.Fatal(err)
	}

	counts, err = dash.Counts(ctx, f.orgID)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if counts.RatingCount != 2 || counts.AverageRating != 3.5 {
		t.Fatalf("rating counts: %+v", counts)
	}
	if counts.Total != 2 {
		t.Fatalf("ticket total: %d", counts.Total)
	}

	// Another tenant's ratings never leak into this org's dashboard.
	other := seedFixture(t, pool)
	if _, err := repo.InsertRating(ctx, other.orgID, other.ticketID, other.customerID, 1, ""); err != nil {
		t.Fatal(err)
	}
	counts, err = dash.Counts(ctx, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.RatingCount != 2 || counts.AverageRating != 3.5 {
		t.Fatalf("cross-tenant rating leak: %+v", counts)
	}
}
