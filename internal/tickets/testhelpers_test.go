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

func seedMembership(t *testing.T, repo *Repository, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var userID uuid.UUID
	if err := repo.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name)
		 VALUES ($1, 'x', 'Seed User') RETURNING id`,
		"seed-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := repo.pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'agent')`,
		orgID, userID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = repo.pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, userID)
		_, _ = repo.pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}
