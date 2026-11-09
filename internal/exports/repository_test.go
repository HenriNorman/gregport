package exports

import (
	"context"
	"errors"
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

type repoFixture struct {
	orgID      uuid.UUID
	otherOrgID uuid.UUID
	userID     uuid.UUID
	customerID uuid.UUID
	otherCust  uuid.UUID
	assigneeID uuid.UUID
}

func seedRepoFixture(t *testing.T, pool *pgxpool.Pool) repoFixture {
	t.Helper()
	ctx := context.Background()
	var f repoFixture

	f.orgID = uuid.New()
	f.otherOrgID = uuid.New()
	for _, org := range []uuid.UUID{f.orgID, f.otherOrgID} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			org, "Export Test", "export-"+org.String()[:8],
		); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}

	f.userID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name)
		 VALUES ($1, $2, 'x', 'Export User')`,
		f.userID, "export-"+f.userID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'admin')`,
		f.orgID, f.userID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	f.assigneeID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name)
		 VALUES ($1, $2, 'x', 'Export Agent')`,
		f.assigneeID, "agent-"+f.assigneeID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed assignee: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'agent')`,
		f.orgID, f.assigneeID,
	); err != nil {
		t.Fatalf("seed assignee membership: %v", err)
	}

	f.customerID = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email, name)
		 VALUES ($1, $2, $3, 'Customer A')`,
		f.customerID, f.orgID, "customer-a-"+f.customerID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	f.otherCust = uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email, name)
		 VALUES ($1, $2, $3, 'Customer B')`,
		f.otherCust, f.otherOrgID, "customer-b-"+f.otherCust.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed other customer: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	statuses := []string{"new", "open", "open", "pending", "closed"}
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO tickets
				(org_id, number, requester_id, assignee_id, subject, status, priority, channel, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6::ticket_status, 'normal', 'api', $7, $7)`,
			f.orgID, int64(100+i), f.customerID, f.assigneeID,
			"Ticket "+string(rune('A'+i)), statuses[i], base.Add(time.Duration(i)*time.Minute),
		); err != nil {
			t.Fatalf("seed ticket %d: %v", i, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets
			(org_id, number, requester_id, subject, status, priority, channel)
		 VALUES ($1, 200, $2, 'Other org ticket', 'open', 'normal', 'api')`,
		f.otherOrgID, f.otherCust,
	); err != nil {
		t.Fatalf("seed other ticket: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM export_jobs WHERE org_id = ANY($1)`, []uuid.UUID{f.orgID, f.otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = ANY($1)`, []uuid.UUID{f.orgID, f.otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = ANY($1)`, []uuid.UUID{f.orgID, f.otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = ANY($1)`, []uuid.UUID{f.orgID, f.otherOrgID})
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{f.userID, f.assigneeID})
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{f.orgID, f.otherOrgID})
	})
	return f
}

func TestRepositoryKeysetPaginationAndTenantScope(t *testing.T) {
	pool := testPool(t)
	f := seedRepoFixture(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	var (
		all     []TicketRow
		after   time.Time
		afterID uuid.UUID
	)
	for page := 0; page < 10; page++ {
		rows, err := repo.ListTicketRows(ctx, f.orgID, Filter{}, after, afterID, 2)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		all = append(all, rows...)
		last := rows[len(rows)-1]
		after, afterID = last.CreatedAt, last.ID
	}
	if len(all) != 5 {
		t.Fatalf("rows=%d want 5", len(all))
	}
	seen := make(map[int64]bool)
	for _, r := range all {
		if seen[r.Number] {
			t.Fatalf("duplicate ticket %d across pages", r.Number)
		}
		seen[r.Number] = true
	}
	if all[0].Number != 104 || all[4].Number != 100 {
		t.Fatalf("order=%d..%d want 104..100", all[0].Number, all[4].Number)
	}

	// Status filter.
	open := "open"
	filtered, err := repo.ListTicketRows(ctx, f.orgID, Filter{Status: &open}, time.Time{}, uuid.Nil, 100)
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("open rows=%d want 2", len(filtered))
	}

	// Assignee filter.
	assigned, err := repo.ListTicketRows(ctx, f.orgID, Filter{AssigneeID: &f.assigneeID}, time.Time{}, uuid.Nil, 100)
	if err != nil {
		t.Fatalf("assigned: %v", err)
	}
	if len(assigned) != 5 {
		t.Fatalf("assigned rows=%d want 5", len(assigned))
	}

	// The other org only sees its own ticket.
	other, err := repo.ListTicketRows(ctx, f.otherOrgID, Filter{}, time.Time{}, uuid.Nil, 100)
	if err != nil {
		t.Fatalf("other org: %v", err)
	}
	if len(other) != 1 || other[0].Number != 200 {
		t.Fatalf("other rows=%+v", other)
	}
}

func TestRepositoryJobScopingAndRecipient(t *testing.T) {
	pool := testPool(t)
	f := seedRepoFixture(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	job, err := repo.CreateJob(ctx, f.orgID, f.userID, []byte(`{"status":"open"}`))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.Status != StatusPending {
		t.Fatalf("status=%s", job.Status)
	}

	got, err := repo.GetJob(ctx, f.orgID, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.RequestedBy != f.userID {
		t.Fatalf("requested_by=%s", got.RequestedBy)
	}

	if _, err := repo.GetJob(ctx, f.otherOrgID, job.ID); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("foreign get err=%v", err)
	}
	if _, ok, err := repo.MarkRunning(ctx, f.otherOrgID, job.ID); err != nil || ok {
		t.Fatalf("foreign mark running ok=%v err=%v", ok, err)
	}

	running, ok, err := repo.MarkRunning(ctx, f.orgID, job.ID)
	if err != nil || !ok {
		t.Fatalf("mark running ok=%v err=%v", ok, err)
	}
	if running.Status != StatusRunning {
		t.Fatalf("status=%s", running.Status)
	}

	if err := repo.MarkDone(ctx, f.orgID, job.ID, "org/x/exports/y.csv", 7); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	done, err := repo.GetJob(ctx, f.orgID, job.ID)
	if err != nil {
		t.Fatalf("get done: %v", err)
	}
	if done.Status != StatusDone || done.RowCount == nil || *done.RowCount != 7 || done.CompletedAt == nil {
		t.Fatalf("done=%+v", done)
	}
	if _, ok, _ := repo.MarkRunning(ctx, f.orgID, job.ID); ok {
		t.Fatal("done job was reclaimed")
	}

	email, orgName, err := repo.LoadRecipient(ctx, f.orgID, f.userID)
	if err != nil {
		t.Fatalf("recipient: %v", err)
	}
	if email == "" || orgName != "Export Test" {
		t.Fatalf("email=%q org=%q", email, orgName)
	}
	if _, _, err := repo.LoadRecipient(ctx, f.otherOrgID, f.userID); err == nil {
		t.Fatal("recipient resolved across orgs")
	}
}
