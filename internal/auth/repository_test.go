package auth

import (
	"context"
	"testing"
	"time"

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

func cleanup(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, email)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = 'test-org'`)
}

func TestCreateAccount_HappyPath(t *testing.T) {
	pool := testPool(t)
	defer cleanup(t, pool, "signup-happy@test.local")

	repo := NewRepository(pool)
	org, user, member, err := repo.CreateAccount(context.Background(),
		Organization{Name: "Test Org", Slug: "test-org"},
		User{Email: "signup-happy@test.local", PasswordHash: "hash", Name: "Henri"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if org.ID == [16]byte{} {
		t.Fatal("org id not populated")
	}
	if member.Role != "owner" {
		t.Fatalf("expected role owner, got %s", member.Role)
	}
	if user.Email != "signup-happy@test.local" {
		t.Fatalf("wrong email: %s", user.Email)
	}
}

func TestCreateAccount_DuplicateEmail_RollsBackOrg(t *testing.T) {
	pool := testPool(t)
	defer cleanup(t, pool, "signup-dup@test.local")

	repo := NewRepository(pool)
	_, _, _, err := repo.CreateAccount(context.Background(),
		Organization{Name: "Test Org", Slug: "test-org"},
		User{Email: "signup-dup@test.local", PasswordHash: "hash", Name: "First"},
	)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, _, _, err = repo.CreateAccount(context.Background(),
		Organization{Name: "Other Org", Slug: "test-org-2"},
		User{Email: "signup-dup@test.local", PasswordHash: "hash", Name: "Second"},
	)
	if err != ErrEmailTaken {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}

	// prove no orphan org was created
	var count int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM organizations WHERE slug = 'test-org-2'`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 orphan orgs, got %d", count)
	}
}
