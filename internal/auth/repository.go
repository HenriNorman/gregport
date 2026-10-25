package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
