package auth

import (
	"context"
	"testing"
	"time"
	
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
)

func cleanupRefreshTokens(t *testing.T, pool *pgxpool.Pool, userIDs ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, uid := range userIDs {
		_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, uid)
	}
}

func setupLoginUser(t *testing.T, repo *Repository) (userID, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	org, user, _, err := repo.CreateAccount(ctx,
		Organization{Name: "Token Test Org", Slug: "token-test-" + uuid.NewString()[:8]},
		User{Email: "tok-" + uuid.NewString()[:8] + "@test.local", PasswordHash: "hash", Name: "T"},
	)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return user.ID, org.ID
}

func TestRotateRefreshToken_HappyPath(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	userID, orgID := setupLoginUser(t, repo)
	defer cleanupRefreshTokens(t, pool, userID)
	family := uuid.New()
	raw1 := "raw-token-1-" + uuid.NewString()
	hash1 := HashRefreshToken(raw1)

	if err := repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: userID, OrgID: orgID, FamilyID: family,
		TokenHash: hash1, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	u, o, f, err := repo.RotateRefreshToken(ctx, hash1)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if u != userID || o != orgID || f != family {
		t.Fatalf("mismatch: %v %v %v", u, o, f)
	}
}

func TestRotateRefreshToken_ReuseRevokesFamily(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	userID, orgID := setupLoginUser(t, repo)
	defer cleanupRefreshTokens(t, pool, userID)
	family := uuid.New()
	raw1 := "raw-token-1-" + uuid.NewString()
	raw2 := "raw-token-2-" + uuid.NewString()
	hash1 := HashRefreshToken(raw1)
	hash2 := HashRefreshToken(raw2)

	if err := repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: userID, OrgID: orgID, FamilyID: family,
		TokenHash: hash1, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create 1: %v", err)
	}

	_, _, _, err := repo.RotateRefreshToken(ctx, hash1)
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}

	if err := repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: userID, OrgID: orgID, FamilyID: family,
		TokenHash: hash2, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create 2: %v", err)
	}

	_, _, _, err = repo.RotateRefreshToken(ctx, hash1)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken on replay, got %v", err)
	}

	old, err := repo.FindRefreshTokenByHash(ctx, hash1)
	if err != nil {
		t.Fatalf("find old: %v", err)
	}
	if old.RevokedAt == nil {
		t.Fatal("expected old token revoked")
	}

	if err := repo.RevokeFamily(ctx, old.FamilyID); err != nil {
		t.Fatalf("revoke family: %v", err)
	}

	_, _, _, err = repo.RotateRefreshToken(ctx, hash2)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken after family revoke, got %v", err)
	}
}

func TestRotateRefreshToken_DeletedOrgRejected(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	userID, orgID := setupLoginUser(t, repo)
	defer cleanupRefreshTokens(t, pool, userID)
	family := uuid.New()
	raw := "raw-token-1-" + uuid.NewString()
	hash := HashRefreshToken(raw)

	if err := repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: userID, OrgID: orgID, FamilyID: family,
		TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, orgID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	_, _, _, err := repo.RotateRefreshToken(ctx, hash)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for deleted org, got %v", err)
	}
}

func TestRotateRefreshToken_ExpiredRejected(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	userID, orgID := setupLoginUser(t, repo)
	defer cleanupRefreshTokens(t, pool, userID)
	family := uuid.New()
	raw := "raw-token-1-" + uuid.NewString()
	hash := HashRefreshToken(raw)

	if err := repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: userID, OrgID: orgID, FamilyID: family,
		TokenHash: hash, ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, _, _, err := repo.RotateRefreshToken(ctx, hash)
	if err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for expired, got %v", err)
	}
}

func TestFindLoginCandidate_ExcludesDeletedOrgs(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	userID, orgID := setupLoginUser(t, repo)
	defer cleanupRefreshTokens(t, pool, userID)
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		t.Fatalf("email: %v", err)
	}

	c, err := repo.FindLoginCandidate(ctx, email)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(c.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(c.Memberships))
	}

	if _, err := pool.Exec(ctx, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, orgID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	c, err = repo.FindLoginCandidate(ctx, email)
	if err != nil {
		t.Fatalf("find after delete: %v", err)
	}
	if len(c.Memberships) != 0 {
		t.Fatalf("expected 0 memberships, got %d", len(c.Memberships))
	}
}
