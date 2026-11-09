package exports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract the export service and task handler use.
type Store interface {
	CreateJob(ctx context.Context, orgID, requestedBy uuid.UUID, filters []byte) (Job, error)
	GetJob(ctx context.Context, orgID, id uuid.UUID) (Job, error)
	MarkRunning(ctx context.Context, orgID, id uuid.UUID) (Job, bool, error)
	MarkDone(ctx context.Context, orgID, id uuid.UUID, storageKey string, rows int64) error
	MarkFailed(ctx context.Context, orgID, id uuid.UUID, reason string) error
	LoadRecipient(ctx context.Context, orgID, userID uuid.UUID) (email, orgName string, err error)
	ListTicketRows(ctx context.Context, orgID uuid.UUID, f Filter, after time.Time, afterID uuid.UUID, limit int) ([]TicketRow, error)
}

// Repository persists export jobs and streams ticket rows.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a PostgreSQL-backed export repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const jobColumns = `id, org_id, requested_by, status, COALESCE(storage_key, ''),
	COALESCE(filters, '{}'::jsonb), COALESCE(error, ''), row_count, created_at, completed_at`

// CreateJob inserts a pending export job and returns it.
func (r *Repository) CreateJob(ctx context.Context, orgID, requestedBy uuid.UUID, filters []byte) (Job, error) {
	row := r.pool.QueryRow(ctx,
		`INSERT INTO export_jobs (org_id, requested_by, status, filters)
		 VALUES ($1, $2, 'pending', $3)
		 RETURNING `+jobColumns,
		orgID, requestedBy, filters,
	)
	return scanJob(row)
}

// GetJob returns a job scoped to orgID. Another org's job is reported as
// missing, never forbidden.
func (r *Repository) GetJob(ctx context.Context, orgID, id uuid.UUID) (Job, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM export_jobs WHERE org_id = $1 AND id = $2`,
		orgID, id,
	)
	return scanJob(row)
}

// MarkRunning claims a job for this attempt. A done job is returned unchanged
// with ok=false so a duplicate task is a safe no-op.
func (r *Repository) MarkRunning(ctx context.Context, orgID, id uuid.UUID) (Job, bool, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE export_jobs
		 SET status = 'running', error = NULL
		 WHERE org_id = $1 AND id = $2 AND status <> 'done'
		 RETURNING `+jobColumns,
		orgID, id,
	)
	job, err := scanJob(row)
	if errors.Is(err, ErrExportNotFound) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

// MarkDone records the finished object key and row count.
func (r *Repository) MarkDone(ctx context.Context, orgID, id uuid.UUID, storageKey string, rows int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE export_jobs
		 SET status = 'done', storage_key = $3, row_count = $4,
		     error = NULL, completed_at = now()
		 WHERE org_id = $1 AND id = $2`,
		orgID, id, storageKey, rows,
	)
	if err != nil {
		return fmt.Errorf("exports: mark done: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrExportNotFound
	}
	return nil
}

// MarkFailed records a retryable failure. The next attempt may claim the job
// again because MarkRunning accepts any non-done status.
func (r *Repository) MarkFailed(ctx context.Context, orgID, id uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE export_jobs SET status = 'failed', error = $3 WHERE org_id = $1 AND id = $2`,
		orgID, id, reason,
	)
	if err != nil {
		return fmt.Errorf("exports: mark failed: %w", err)
	}
	return nil
}

// LoadRecipient resolves the requesting user's email and the org name for the
// completion email.
func (r *Repository) LoadRecipient(ctx context.Context, orgID, userID uuid.UUID) (string, string, error) {
	var email, orgName string
	err := r.pool.QueryRow(ctx,
		`SELECT u.email, o.name
		 FROM users u
		 JOIN memberships m ON m.user_id = u.id AND m.org_id = $2
		 JOIN organizations o ON o.id = m.org_id
		 WHERE u.id = $1 AND u.deleted_at IS NULL`,
		userID, orgID,
	).Scan(&email, &orgName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("exports: requester %s not found: %w", userID, ErrExportNotFound)
		}
		return "", "", fmt.Errorf("exports: load recipient: %w", err)
	}
	return email, orgName, nil
}

// ListTicketRows returns one keyset page of tickets, newest first. The cursor
// is (created_at, id): two tickets created in the same transaction still have
// a stable order because id breaks the tie.
func (r *Repository) ListTicketRows(ctx context.Context, orgID uuid.UUID, f Filter, after time.Time, afterID uuid.UUID, limit int) ([]TicketRow, error) {
	if limit <= 0 || limit > MaxBatchSize {
		limit = MaxBatchSize
	}

	args := []any{orgID}
	where := "WHERE t.org_id = $1 AND t.deleted_at IS NULL"
	add := func(clause string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}

	if f.Status != nil {
		add("t.status", *f.Status)
	}
	if f.Priority != nil {
		add("t.priority", *f.Priority)
	}
	if f.AssigneeID != nil {
		add("t.assignee_id", *f.AssigneeID)
	}
	if f.RequesterID != nil {
		add("t.requester_id", *f.RequesterID)
	}
	if f.CreatedFrom != nil {
		args = append(args, *f.CreatedFrom)
		where += fmt.Sprintf(" AND t.created_at >= $%d", len(args))
	}
	if f.CreatedTo != nil {
		args = append(args, *f.CreatedTo)
		where += fmt.Sprintf(" AND t.created_at <= $%d", len(args))
	}
	if !after.IsZero() {
		args = append(args, after, afterID)
		where += fmt.Sprintf(" AND (t.created_at, t.id) < ($%d, $%d)", len(args)-1, len(args))
	}

	args = append(args, limit)
	query := fmt.Sprintf(
		`SELECT t.id, t.number, t.subject, t.status, t.priority, t.channel,
		        c.email, COALESCE(c.name, ''), COALESCE(u.email, ''),
		        t.created_at, t.updated_at, t.resolved_at, t.closed_at
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 LEFT JOIN users u ON u.id = t.assignee_id
		 %s
		 ORDER BY t.created_at DESC, t.id DESC
		 LIMIT $%d`,
		where, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("exports: list ticket rows: %w", err)
	}
	defer rows.Close()

	out := make([]TicketRow, 0, limit)
	for rows.Next() {
		var row TicketRow
		var resolvedAt, closedAt sql.NullTime
		if err := rows.Scan(
			&row.ID, &row.Number, &row.Subject, &row.Status, &row.Priority, &row.Channel,
			&row.RequesterEmail, &row.RequesterName, &row.AssigneeEmail,
			&row.CreatedAt, &row.UpdatedAt, &resolvedAt, &closedAt,
		); err != nil {
			return nil, fmt.Errorf("exports: scan ticket row: %w", err)
		}
		if resolvedAt.Valid {
			row.ResolvedAt = &resolvedAt.Time
		}
		if closedAt.Valid {
			row.ClosedAt = &closedAt.Time
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("exports: rows: %w", err)
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var j Job
	var completedAt sql.NullTime
	err := row.Scan(
		&j.ID, &j.OrgID, &j.RequestedBy, &j.Status, &j.StorageKey,
		&j.Filters, &j.Error, &j.RowCount, &j.CreatedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrExportNotFound
		}
		return Job{}, fmt.Errorf("exports: scan job: %w", err)
	}
	if completedAt.Valid {
		j.CompletedAt = &completedAt.Time
	}
	return j, nil
}
