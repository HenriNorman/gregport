package attachments

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

// Store is the persistence contract the attachment service and task handlers
// use.
type Store interface {
	GetTicketIDByNumber(ctx context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error)
	InsertAttachment(ctx context.Context, a Attachment) (Attachment, error)
	GetAttachment(ctx context.Context, orgID, id uuid.UUID) (Attachment, error)
	MarkUploaded(ctx context.Context, orgID, id uuid.UUID) (Attachment, error)
	MarkQuarantined(ctx context.Context, orgID, id uuid.UUID, reason string) error
	SetThumbnail(ctx context.Context, orgID, id uuid.UUID, key string) error
	SetChecksum(ctx context.Context, orgID, id uuid.UUID, checksum string) error
	ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Attachment, error)
	DeleteAttachments(ctx context.Context, ids []uuid.UUID) (int64, error)
	ListKeysForTicket(ctx context.Context, orgID, ticketID uuid.UUID) ([]string, error)
}

// Repository persists attachments.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a PostgreSQL-backed attachment repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const attachmentColumns = `id, org_id, ticket_id, comment_id, storage_key, filename,
	mime_type, size_bytes, upload_status, COALESCE(thumbnail_key, ''),
	COALESCE(checksum, ''), COALESCE(quarantine_reason, ''), created_at,
	confirmed_at, expires_at`

// GetTicketIDByNumber resolves a ticket number inside one org.
func (r *Repository) GetTicketIDByNumber(ctx context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT id FROM tickets
		 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
		orgID, number,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTicketNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("attachments: resolve ticket: %w", err)
	}
	return id, nil
}

// InsertAttachment creates a pending attachment row.
func (r *Repository) InsertAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	var comment any
	if a.CommentID != nil {
		comment = *a.CommentID
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO attachments
			(org_id, ticket_id, comment_id, storage_key, filename, mime_type,
			 size_bytes, upload_status, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+attachmentColumns,
		a.OrgID, a.TicketID, comment, a.StorageKey, a.Filename, a.MIMEType,
		a.SizeBytes, StatusPending, a.ExpiresAt,
	)
	return scanAttachment(row)
}

// GetAttachment returns an attachment scoped to orgID. Attachments on deleted
// tickets are treated as missing.
func (r *Repository) GetAttachment(ctx context.Context, orgID, id uuid.UUID) (Attachment, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+prefixColumns("a")+`
		 FROM attachments a
		 JOIN tickets t ON t.id = a.ticket_id
		 WHERE a.org_id = $1 AND a.id = $2
		   AND t.org_id = $1 AND t.deleted_at IS NULL`,
		orgID, id,
	)
	return scanAttachment(row)
}

// MarkUploaded moves a pending attachment to uploaded. The update is
// idempotent: an already-uploaded attachment is returned unchanged.
func (r *Repository) MarkUploaded(ctx context.Context, orgID, id uuid.UUID) (Attachment, error) {
	row := r.pool.QueryRow(ctx,
		`UPDATE attachments
		 SET upload_status = 'uploaded', confirmed_at = now(), expires_at = NULL,
		     updated_at = now()
		 WHERE org_id = $1 AND id = $2 AND upload_status IN ('pending', 'uploaded')
		 RETURNING `+attachmentColumns,
		orgID, id,
	)
	return scanAttachment(row)
}

// MarkQuarantined records why an upload was rejected.
func (r *Repository) MarkQuarantined(ctx context.Context, orgID, id uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE attachments
		 SET upload_status = 'quarantined', quarantine_reason = $3, updated_at = now()
		 WHERE org_id = $1 AND id = $2`,
		orgID, id, reason,
	)
	return err
}

// SetThumbnail stores the generated thumbnail object key.
func (r *Repository) SetThumbnail(ctx context.Context, orgID, id uuid.UUID, key string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE attachments SET thumbnail_key = $3, updated_at = now()
		 WHERE org_id = $1 AND id = $2`,
		orgID, id, key,
	)
	return err
}

// SetChecksum stores the ETag returned by storage.
func (r *Repository) SetChecksum(ctx context.Context, orgID, id uuid.UUID, checksum string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE attachments SET checksum = $3, updated_at = now()
		 WHERE org_id = $1 AND id = $2`,
		orgID, id, checksum,
	)
	return err
}

// ListExpiredPending returns pending uploads whose grace period elapsed.
func (r *Repository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Attachment, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+attachmentColumns+`
		 FROM attachments
		 WHERE upload_status = 'pending' AND expires_at IS NOT NULL AND expires_at < $1
		 ORDER BY expires_at
		 LIMIT $2`,
		before, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("attachments: list expired pending: %w", err)
	}
	defer rows.Close()
	return scanAttachments(rows)
}

// DeleteAttachments removes rows by id.
func (r *Repository) DeleteAttachments(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM attachments WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("attachments: delete attachments: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListKeysForTicket returns every storage key (original and thumbnail) for a
// ticket so retention can remove the objects before hard-deleting rows.
func (r *Repository) ListKeysForTicket(ctx context.Context, orgID, ticketID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT storage_key, COALESCE(thumbnail_key, '')
		 FROM attachments
		 WHERE org_id = $1 AND ticket_id = $2`,
		orgID, ticketID,
	)
	if err != nil {
		return nil, fmt.Errorf("attachments: list ticket keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key, thumb string
		if err := rows.Scan(&key, &thumb); err != nil {
			return nil, err
		}
		keys = append(keys, key)
		if thumb != "" {
			keys = append(keys, thumb)
		}
	}
	return keys, rows.Err()
}

func prefixColumns(alias string) string {
	return alias + `.id, ` + alias + `.org_id, ` + alias + `.ticket_id, ` + alias + `.comment_id, ` +
		alias + `.storage_key, ` + alias + `.filename, ` + alias + `.mime_type, ` + alias + `.size_bytes, ` +
		alias + `.upload_status, COALESCE(` + alias + `.thumbnail_key, ''), COALESCE(` + alias + `.checksum, ''), ` +
		`COALESCE(` + alias + `.quarantine_reason, ''), ` + alias + `.created_at, ` + alias + `.confirmed_at, ` + alias + `.expires_at`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAttachment(row rowScanner) (Attachment, error) {
	var a Attachment
	var commentID sql.NullString
	var confirmedAt, expiresAt sql.NullTime
	err := row.Scan(
		&a.ID, &a.OrgID, &a.TicketID, &commentID, &a.StorageKey, &a.Filename,
		&a.MIMEType, &a.SizeBytes, &a.UploadStatus, &a.ThumbnailKey,
		&a.Checksum, &a.QuarantineReason, &a.CreatedAt, &confirmedAt, &expiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Attachment{}, ErrAttachmentNotFound
		}
		return Attachment{}, fmt.Errorf("attachments: scan: %w", err)
	}
	if commentID.Valid {
		if id, perr := uuid.Parse(commentID.String); perr == nil {
			a.CommentID = &id
		}
	}
	if confirmedAt.Valid {
		a.ConfirmedAt = &confirmedAt.Time
	}
	if expiresAt.Valid {
		a.ExpiresAt = &expiresAt.Time
	}
	return a, nil
}

func scanAttachments(rows pgx.Rows) ([]Attachment, error) {
	out := make([]Attachment, 0)
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
