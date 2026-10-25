package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
	"github.com/HenriNorman/gregport/internal/platform/database"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateAccount(ctx context.Context, org Organization, user User) (Organization, User, Membership, error) {
	var (
		outOrg   Organization
		outUser  User
		outMember Membership
	)

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO organizations (name, slug)
			 VALUES ($1, $2)
			 RETURNING id, name, slug, plan, created_at, updated_at`,
			org.Name, org.Slug,
		).Scan(&outOrg.ID, &outOrg.Name, &outOrg.Slug, &outOrg.Plan, &outOrg.CreatedAt, &outOrg.UpdatedAt); err != nil {
			return translatePgError(err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO users (email, password_hash, name)
			 VALUES ($1, $2, $3)
			 RETURNING id, email, password_hash, name, created_at, updated_at`,
			user.Email, user.PasswordHash, user.Name,
		).Scan(&outUser.ID, &outUser.Email, &outUser.PasswordHash, &outUser.Name, &outUser.CreatedAt, &outUser.UpdatedAt); err != nil {
			return translatePgError(err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO memberships (org_id, user_id, role)
			 VALUES ($1, $2, 'owner')
			 RETURNING id, org_id, user_id, role, created_at`,
			outOrg.ID, outUser.ID,
		).Scan(&outMember.ID, &outMember.OrgID, &outMember.UserID, &outMember.Role, &outMember.CreatedAt); err != nil {
			return translatePgError(err)
		}

		return nil
	})
	if err != nil {
		return Organization{}, User{}, Membership{}, err
	}

	return outOrg, outUser, outMember, nil
}

func translatePgError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "organizations_slug_key":
			return ErrSlugTaken
		case "users_email_key":
			return ErrEmailTaken
		}
	}
	return fmt.Errorf("db: %w", err)
}

func (r *Repository) FindLoginCandidate(ctx context.Context, email string) (LoginCandidate, error) {
	var c LoginCandidate
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, name
		 FROM users
		 WHERE email = $1 AND deleted_at IS NULL`,
		email,
	).Scan(&c.UserID, &c.Email, &c.PasswordHash, &c.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LoginCandidate{}, ErrInvalidCredentials
		}
		return LoginCandidate{}, fmt.Errorf("find user: %w", err)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT m.org_id, o.slug, o.name, m.role
		 FROM memberships m
		 JOIN organizations o ON o.id = m.org_id
		 WHERE m.user_id = $1 AND o.deleted_at IS NULL
		 ORDER BY o.name`,
		c.UserID,
	)
	if err != nil {
		return LoginCandidate{}, fmt.Errorf("find memberships: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mi MembershipInfo
		if err := rows.Scan(&mi.OrgID, &mi.OrgSlug, &mi.OrgName, &mi.Role); err != nil {
			return LoginCandidate{}, fmt.Errorf("scan membership: %w", err)
		}
		c.Memberships = append(c.Memberships, mi)
	}
	if err := rows.Err(); err != nil {
		return LoginCandidate{}, fmt.Errorf("memberships rows: %w", err)
	}

	return c, nil
}

func (r *Repository) CreateRefreshToken(ctx context.Context, t RefreshToken) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO refresh_tokens
		   (user_id, org_id, family_id, token_hash, user_agent, ip, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.UserID, t.OrgID, t.FamilyID, t.TokenHash, t.UserAgent, t.IP, t.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

func (r *Repository) RotateRefreshToken(ctx context.Context, hash string) (userID, orgID, familyID uuid.UUID, err error) {
	err = r.pool.QueryRow(ctx,
		`UPDATE refresh_tokens rt SET revoked_at = now()
		 WHERE rt.token_hash = $1
		   AND rt.revoked_at IS NULL
		   AND rt.expires_at > now()
		   AND EXISTS (
		     SELECT 1 FROM organizations o
		     WHERE o.id = rt.org_id AND o.deleted_at IS NULL
		   )
		 RETURNING rt.user_id, rt.org_id, rt.family_id`,
		hash,
	).Scan(&userID, &orgID, &familyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, uuid.Nil, ErrInvalidToken
		}
		return uuid.Nil, uuid.Nil, uuid.Nil, fmt.Errorf("rotate: %w", err)
	}
	return userID, orgID, familyID, nil
}

func (r *Repository) FindRefreshTokenByHash(ctx context.Context, hash string) (RefreshToken, error) {
	var t RefreshToken
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, org_id, family_id, token_hash, user_agent, ip, expires_at, revoked_at, created_at
		 FROM refresh_tokens WHERE token_hash = $1`,
		hash,
	).Scan(&t.ID, &t.UserID, &t.OrgID, &t.FamilyID, &t.TokenHash,
		&t.UserAgent, &t.IP, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RefreshToken{}, ErrInvalidToken
		}
		return RefreshToken{}, fmt.Errorf("find refresh: %w", err)
	}
	return t, nil
}

func (r *Repository) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID,
	)
	if err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}

func (r *Repository) RevokeByHash(ctx context.Context, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)
		   AND revoked_at IS NULL`,
		hash,
	)
	if err != nil {
		return fmt.Errorf("revoke by hash: %w", err)
	}
	return nil
}
