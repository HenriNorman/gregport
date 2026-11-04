package sla

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract used by the service.
type Store interface {
	List(ctx context.Context, orgID uuid.UUID) ([]Policy, error)
	Get(ctx context.Context, orgID, policyID uuid.UUID) (Policy, error)
	Create(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error)
	Update(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error)
	Delete(ctx context.Context, orgID, policyID uuid.UUID) error
}

// Repository persists SLA policies.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const policyColumns = `id, org_id, name, priority, first_response_minutes, resolution_minutes, created_at`

// List returns every policy for orgID in creation order.
func (r *Repository) List(ctx context.Context, orgID uuid.UUID) ([]Policy, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+policyColumns+`
		 FROM sla_policies
		 WHERE org_id = $1
		 ORDER BY created_at, id`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	policies := make([]Policy, 0)
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

// Get returns one policy scoped to orgID.
func (r *Repository) Get(ctx context.Context, orgID, policyID uuid.UUID) (Policy, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+policyColumns+`
		 FROM sla_policies
		 WHERE org_id = $1 AND id = $2`,
		orgID, policyID,
	)
	policy, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	return policy, err
}

// Create inserts a new policy. A duplicate (org_id, priority) becomes
// ErrPolicyExists.
func (r *Repository) Create(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	row := r.pool.QueryRow(ctx,
		`INSERT INTO sla_policies
		   (org_id, name, priority, first_response_minutes, resolution_minutes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+policyColumns,
		orgID, policy.Name, policy.Priority, policy.FirstResponseMinutes, policy.ResolutionMinutes,
	)
	created, err := scanPolicy(row)
	if err != nil {
		return Policy{}, mapWriteError(err)
	}
	return created, nil
}

// Update replaces an existing policy scoped to orgID.
func (r *Repository) Update(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE sla_policies
		 SET name = $3, priority = $4, first_response_minutes = $5, resolution_minutes = $6
		 WHERE org_id = $1 AND id = $2
		 RETURNING `+policyColumns,
		orgID, policy.ID, policy.Name, policy.Priority, policy.FirstResponseMinutes, policy.ResolutionMinutes,
	)
	updated, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	if err != nil {
		return Policy{}, mapWriteError(err)
	}
	return updated, nil
}

// Delete removes a policy scoped to orgID. A policy still referenced by a
// ticket returns ErrPolicyInUse instead of failing the foreign key.
func (r *Repository) Delete(ctx context.Context, orgID, policyID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM sla_policies WHERE org_id = $1 AND id = $2`,
		orgID, policyID,
	)
	if err != nil {
		return mapWriteError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPolicy(row rowScanner) (Policy, error) {
	var policy Policy
	err := row.Scan(
		&policy.ID,
		&policy.OrgID,
		&policy.Name,
		&policy.Priority,
		&policy.FirstResponseMinutes,
		&policy.ResolutionMinutes,
		&policy.CreatedAt,
	)
	return policy, err
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrPolicyExists
		case "23503":
			return ErrPolicyInUse
		}
	}
	return err
}
