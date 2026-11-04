package organizations

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
		`INSERT INTO organizations (name, slug) VALUES ('Settings Org', $1) RETURNING id`,
		"settings-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

func TestRepositorySettingsRoundTrip(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	settings, updatedAt, err := repo.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("get defaults: %v", err)
	}
	if settings != (Settings{}) {
		t.Fatalf("expected empty defaults from storage, got %+v", settings)
	}
	if updatedAt.Before(before) {
		t.Fatalf("updated at looks wrong: %s", updatedAt)
	}

	want := Settings{
		Timezone:        "Africa/Nairobi",
		DefaultPriority: "high",
		SupportEmail:    "support@acme.com",
	}
	saved, savedAt, err := repo.UpdateSettings(ctx, orgID, want)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if saved != want {
		t.Fatalf("saved: %+v, want %+v", saved, want)
	}
	if savedAt.Before(updatedAt) {
		t.Fatalf("updated_at went backwards: %s -> %s", updatedAt, savedAt)
	}

	reloaded, reloadedAt, err := repo.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded != want {
		t.Fatalf("reloaded: %+v, want %+v", reloaded, want)
	}
	if !reloadedAt.Equal(savedAt) {
		t.Fatalf("updated_at mismatch: %s vs %s", reloadedAt, savedAt)
	}
}

func TestRepositorySettingsAreTenantScoped(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	ctx := context.Background()

	if _, _, err := repo.UpdateSettings(ctx, orgA, Settings{
		Timezone:        "Europe/London",
		DefaultPriority: "urgent",
		SupportEmail:    "a@acme.com",
	}); err != nil {
		t.Fatalf("update A: %v", err)
	}

	settingsB, _, err := repo.GetSettings(ctx, orgB)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if settingsB != (Settings{}) {
		t.Fatalf("org B saw org A settings: %+v", settingsB)
	}

	if _, _, err := repo.UpdateSettings(ctx, orgB, Settings{Timezone: "UTC"}); err != nil {
		t.Fatalf("update B: %v", err)
	}

	settingsA, _, err := repo.GetSettings(ctx, orgA)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if settingsA.Timezone != "Europe/London" || settingsA.DefaultPriority != "urgent" || settingsA.SupportEmail != "a@acme.com" {
		t.Fatalf("org A settings changed by org B: %+v", settingsA)
	}
}

func TestRepositoryMissingOrgReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	if _, _, err := repo.GetSettings(ctx, uuid.New()); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("get: expected ErrOrgNotFound, got %v", err)
	}
	if _, _, err := repo.UpdateSettings(ctx, uuid.New(), Settings{Timezone: "UTC"}); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("update: expected ErrOrgNotFound, got %v", err)
	}
}

func TestRepositorySoftDeletedOrgReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, orgID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if _, _, err := repo.GetSettings(ctx, orgID); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("get: expected ErrOrgNotFound, got %v", err)
	}
	if _, _, err := repo.UpdateSettings(ctx, orgID, Settings{Timezone: "UTC"}); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("update: expected ErrOrgNotFound, got %v", err)
	}
}
