package dashboard

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract used by the service.
type Store interface {
	Counts(ctx context.Context, orgID uuid.UUID) (Counts, error)
}

// Repository computes dashboard counts from PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const countsQuery = `
SELECT
  count(*),
  count(*) FILTER (WHERE status IN ('new', 'open', 'pending')),
  count(*) FILTER (WHERE status IN ('new', 'open', 'pending') AND assignee_id IS NULL),
  count(*) FILTER (WHERE status NOT IN ('solved', 'closed')
                     AND resolution_due_at IS NOT NULL
                     AND resolution_due_at < now()),
  count(*) FILTER (WHERE status = 'new'),
  count(*) FILTER (WHERE status = 'open'),
  count(*) FILTER (WHERE status = 'pending'),
  count(*) FILTER (WHERE status = 'solved'),
  count(*) FILTER (WHERE status = 'closed'),
  count(*) FILTER (WHERE priority = 'low'),
  count(*) FILTER (WHERE priority = 'normal'),
  count(*) FILTER (WHERE priority = 'high'),
  count(*) FILTER (WHERE priority = 'urgent')
FROM tickets
WHERE org_id = $1 AND deleted_at IS NULL`

// Counts returns the current dashboard summary for orgID. Soft-deleted
// tickets are excluded from every number.
func (r *Repository) Counts(ctx context.Context, orgID uuid.UUID) (Counts, error) {
	var c Counts
	err := r.pool.QueryRow(ctx, countsQuery, orgID).Scan(
		&c.Total,
		&c.Open,
		&c.Unassigned,
		&c.Overdue,
		&c.ByStatus.New,
		&c.ByStatus.Open,
		&c.ByStatus.Pending,
		&c.ByStatus.Solved,
		&c.ByStatus.Closed,
		&c.ByPriority.Low,
		&c.ByPriority.Normal,
		&c.ByPriority.High,
		&c.ByPriority.Urgent,
	)
	if err != nil {
		return Counts{}, err
	}
	return c, nil
}
