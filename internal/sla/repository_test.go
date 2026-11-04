package sla

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

func seedOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	var orgID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('SLA Org', $1) RETURNING id`,
		"sla-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM sla_policies WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

func TestRepositoryPolicyCRUD(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	policies, err := repo.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(policies) != 0 {
		t.Fatalf("expected no policies, got %+v", policies)
	}

	created, err := repo.Create(ctx, orgID, Policy{
		Name:                 "Low",
		Priority:             "low",
		FirstResponseMinutes: 60,
		ResolutionMinutes:    480,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == uuid.Nil || created.OrgID != orgID {
		t.Fatalf("created: %+v", created)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("created_at was not set")
	}

	got, err := repo.Get(ctx, orgID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != created {
		t.Fatalf("get: %+v, want %+v", got, created)
	}

	updated, err := repo.Update(ctx, orgID, Policy{
		ID:                   created.ID,
		Name:                 "Low (updated)",
		Priority:             "low",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    240,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Low (updated)" || updated.FirstResponseMinutes != 30 || updated.ResolutionMinutes != 240 {
		t.Fatalf("updated: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("created_at changed: %s -> %s", created.CreatedAt, updated.CreatedAt)
	}

	policies, err = repo.List(ctx, orgID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("list: %+v err=%v", policies, err)
	}
	if policies[0].ID != created.ID {
		t.Fatalf("list returned %+v", policies)
	}

	if err := repo.Delete(ctx, orgID, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, orgID, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err := repo.Delete(ctx, orgID, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestRepositoryPoliciesAreTenantScoped(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	ctx := context.Background()

	policyA, err := repo.Create(ctx, orgA, Policy{
		Name: "Low A", Priority: "low", FirstResponseMinutes: 10, ResolutionMinutes: 20,
	})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	policyB, err := repo.Create(ctx, orgB, Policy{
		Name: "Low B", Priority: "low", FirstResponseMinutes: 10, ResolutionMinutes: 20,
	})
	if err != nil {
		t.Fatalf("create B (same priority in another org): %v", err)
	}

	listA, err := repo.List(ctx, orgA)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != policyA.ID {
		t.Fatalf("org A list: %+v", listA)
	}

	listB, err := repo.List(ctx, orgB)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(listB) != 1 || listB[0].ID != policyB.ID {
		t.Fatalf("org B list: %+v", listB)
	}

	if _, err := repo.Get(ctx, orgB, policyA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get: expected ErrNotFound, got %v", err)
	}
	if _, err := repo.Update(ctx, orgB, Policy{
		ID: policyA.ID, Name: "hijacked", Priority: "low",
		FirstResponseMinutes: 10, ResolutionMinutes: 20,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org update: expected ErrNotFound, got %v", err)
	}
	if err := repo.Delete(ctx, orgB, policyA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org delete: expected ErrNotFound, got %v", err)
	}

	reloaded, err := repo.Get(ctx, orgA, policyA.ID)
	if err != nil {
		t.Fatalf("org A policy disappeared: %v", err)
	}
	if reloaded.Name != "Low A" {
		t.Fatalf("org A policy changed: %+v", reloaded)
	}
}

func TestRepositoryRejectsDuplicatePriority(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	first, err := repo.Create(ctx, orgID, Policy{
		Name: "Low", Priority: "low", FirstResponseMinutes: 10, ResolutionMinutes: 20,
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	if _, err := repo.Create(ctx, orgID, Policy{
		Name: "Low again", Priority: "low", FirstResponseMinutes: 30, ResolutionMinutes: 60,
	}); !errors.Is(err, ErrPolicyExists) {
		t.Fatalf("expected ErrPolicyExists, got %v", err)
	}

	second, err := repo.Create(ctx, orgID, Policy{
		Name: "Normal", Priority: "normal", FirstResponseMinutes: 30, ResolutionMinutes: 60,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	if _, err := repo.Update(ctx, orgID, Policy{
		ID: second.ID, Name: second.Name, Priority: "low",
		FirstResponseMinutes: 30, ResolutionMinutes: 60,
	}); !errors.Is(err, ErrPolicyExists) {
		t.Fatalf("update conflict: expected ErrPolicyExists, got %v", err)
	}

	// The failed update must not have changed the row.
	reloaded, err := repo.Get(ctx, orgID, second.ID)
	if err != nil {
		t.Fatalf("reload second: %v", err)
	}
	if reloaded.Priority != "normal" {
		t.Fatalf("priority changed after failed update: %q", reloaded.Priority)
	}
	if first.ID == second.ID {
		t.Fatal("duplicate ids")
	}
}

func TestRepositoryDeleteInUsePolicy(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	policy, err := repo.Create(ctx, orgID, Policy{
		Name: "Low", Priority: "low", FirstResponseMinutes: 10, ResolutionMinutes: 20,
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name) VALUES ($1, $2, 'Customer') RETURNING id`,
		orgID, "customer-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, sla_policy_id, subject, priority)
		 VALUES ($1, 1, $2, $3, 'SLA ticket', 'low')`,
		orgID, customerID, policy.ID,
	); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	if err := repo.Delete(ctx, orgID, policy.ID); !errors.Is(err, ErrPolicyInUse) {
		t.Fatalf("expected ErrPolicyInUse, got %v", err)
	}
	if _, err := repo.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("policy was deleted despite being in use: %v", err)
	}
}
