package organizations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract used by the service. Tests substitute an
// in-memory implementation.
type Store interface {
	GetSettings(ctx context.Context, orgID uuid.UUID) (Settings, time.Time, error)
	UpdateSettings(ctx context.Context, orgID uuid.UUID, settings Settings) (Settings, time.Time, error)
}

// Repository persists organization settings.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// GetSettings reads the settings document for orgID.
func (r *Repository) GetSettings(ctx context.Context, orgID uuid.UUID) (Settings, time.Time, error) {
	var (
		raw       []byte
		updatedAt time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT settings, updated_at
		 FROM organizations
		 WHERE id = $1 AND deleted_at IS NULL`,
		orgID,
	).Scan(&raw, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, time.Time{}, ErrOrgNotFound
	}
	if err != nil {
		return Settings{}, time.Time{}, err
	}

	var settings Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Settings{}, time.Time{}, err
	}
	return settings, updatedAt, nil
}

// UpdateSettings replaces the settings document and returns the persisted
// value. The caller validates the document before calling this method.
func (r *Repository) UpdateSettings(ctx context.Context, orgID uuid.UUID, settings Settings) (Settings, time.Time, error) {
	payload, err := json.Marshal(settings)
	if err != nil {
		return Settings{}, time.Time{}, err
	}

	var (
		raw       []byte
		updatedAt time.Time
	)
	err = r.pool.QueryRow(ctx,
		`UPDATE organizations
		 SET settings = $2, updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING settings, updated_at`,
		orgID, payload,
	).Scan(&raw, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, time.Time{}, ErrOrgNotFound
	}
	if err != nil {
		return Settings{}, time.Time{}, err
	}

	var saved Settings
	if err := json.Unmarshal(raw, &saved); err != nil {
		return Settings{}, time.Time{}, err
	}
	return saved, updatedAt, nil
}
