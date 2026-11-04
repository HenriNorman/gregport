package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract used by the idempotency middleware.
type Store interface {
	Acquire(ctx context.Context, orgID uuid.UUID, key, requestHash string, ttl time.Duration) (AcquireResult, error)
	Complete(ctx context.Context, orgID uuid.UUID, key, requestHash string, status int, body []byte) error
	Release(ctx context.Context, orgID uuid.UUID, key, requestHash string) error
	PurgeExpired(ctx context.Context, before time.Time) (int64, error)
}

// Repository persists idempotency records in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// acquireSQL inserts a pending record, or replaces an expired one, in one
// statement. A non-expired conflict returns no row and is inspected by the
// caller. The unique (org_id, key) index makes concurrent inserts serialise on
// the conflicting row.
const acquireSQL = `
INSERT INTO idempotency_keys (org_id, key, request_hash, expires_at)
VALUES ($1, $2, $3, now() + ($4::bigint * interval '1 microsecond'))
ON CONFLICT (org_id, key) DO UPDATE
SET request_hash    = EXCLUDED.request_hash,
    response_status = NULL,
    response_body   = NULL,
    created_at      = now(),
    expires_at      = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= now()
RETURNING request_hash, response_status, response_body, created_at, expires_at`

const selectSQL = `
SELECT request_hash, response_status, response_body, created_at, expires_at
FROM idempotency_keys
WHERE org_id = $1 AND key = $2`

// Acquire claims a key for requestHash. It returns OutcomeReplay with the
// captured response when the same request already completed, OutcomePending
// while another request owns the key, and OutcomeConflict when the key was
// used for a different request. Expired records are replaced atomically.
func (r *Repository) Acquire(
	ctx context.Context,
	orgID uuid.UUID,
	key, requestHash string,
	ttl time.Duration,
) (AcquireResult, error) {
	key, requestHash, err := validateKey(orgID, key, requestHash)
	if err != nil {
		return AcquireResult{}, err
	}
	if ttl < time.Millisecond {
		return AcquireResult{}, fmt.Errorf("%w: ttl must be at least 1ms", ErrInvalidInput)
	}

	// A purge can remove the row between a failed upsert and the select. One
	// retry is enough to converge; a second miss is treated as a hard error
	// rather than silently double-acquiring.
	for attempt := 0; attempt < 2; attempt++ {
		record, err := scanRecord(r.pool.QueryRow(ctx, acquireSQL,
			orgID, key, requestHash, ttl.Microseconds()))
		if err == nil {
			return AcquireResult{Outcome: OutcomeAcquired, Record: record}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return AcquireResult{}, fmt.Errorf("acquire idempotency key: %w", err)
		}

		record, err = scanRecord(r.pool.QueryRow(ctx, selectSQL, orgID, key))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return AcquireResult{}, fmt.Errorf("read idempotency key: %w", err)
		}

		switch {
		case record.RequestHash != requestHash:
			return AcquireResult{Outcome: OutcomeConflict, Record: record}, nil
		case !record.Completed():
			return AcquireResult{Outcome: OutcomePending, Record: record}, nil
		default:
			return AcquireResult{Outcome: OutcomeReplay, Record: record}, nil
		}
	}

	return AcquireResult{}, errors.New("idempotency: key disappeared while acquiring")
}

const completeSQL = `
UPDATE idempotency_keys
SET response_status = $4,
    response_body   = $5
WHERE org_id = $1
  AND key = $2
  AND request_hash = $3
  AND response_status IS NULL
  AND expires_at > now()`

// Complete stores the response for a key this caller acquired. It fails with
// ErrNotPending when the record is missing, expired, already completed, or
// owned by another request hash.
func (r *Repository) Complete(
	ctx context.Context,
	orgID uuid.UUID,
	key, requestHash string,
	status int,
	body []byte,
) error {
	key, requestHash, err := validateKey(orgID, key, requestHash)
	if err != nil {
		return err
	}
	if status < 100 || status > 599 {
		return fmt.Errorf("%w: response status must be 100-599", ErrInvalidInput)
	}
	if len(body) > 0 && !json.Valid(body) {
		return fmt.Errorf("%w: response body must be JSON", ErrInvalidInput)
	}
	if len(body) == 0 {
		body = nil
	}

	tag, err := r.pool.Exec(ctx, completeSQL, orgID, key, requestHash, status, body)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotPending
	}
	return nil
}

const releaseSQL = `
DELETE FROM idempotency_keys
WHERE org_id = $1
  AND key = $2
  AND request_hash = $3
  AND response_status IS NULL`

// Release drops a pending record so the client may retry after a failed
// request. It is intentionally idempotent: releasing an absent or completed
// record succeeds without deleting anything.
func (r *Repository) Release(ctx context.Context, orgID uuid.UUID, key, requestHash string) error {
	key, requestHash, err := validateKey(orgID, key, requestHash)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, releaseSQL, orgID, key, requestHash); err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

// PurgeExpired deletes records whose expiry is at or before the supplied time.
func (r *Repository) PurgeExpired(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("%w: purge time is required", ErrInvalidInput)
	}
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

func validateKey(orgID uuid.UUID, key, requestHash string) (string, string, error) {
	if orgID == uuid.Nil {
		return "", "", fmt.Errorf("%w: org id is required", ErrInvalidInput)
	}

	key = strings.TrimSpace(key)
	if key == "" || len(key) > MaxKeyLength {
		return "", "", fmt.Errorf("%w: key must be 1-%d bytes", ErrInvalidInput, MaxKeyLength)
	}

	requestHash = strings.TrimSpace(requestHash)
	if requestHash == "" || len(requestHash) > MaxRequestHashLength {
		return "", "", fmt.Errorf("%w: request hash must be 1-%d bytes", ErrInvalidInput, MaxRequestHashLength)
	}

	return key, requestHash, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var (
		record Record
		status *int
	)
	if err := row.Scan(
		&record.RequestHash,
		&status,
		&record.ResponseBody,
		&record.CreatedAt,
		&record.ExpiresAt,
	); err != nil {
		return Record{}, err
	}
	if status != nil {
		record.ResponseStatus = *status
	}
	return record, nil
}
