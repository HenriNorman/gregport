package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxCommentsIndexBytes caps the concatenated comment text stored in one
// document. Comment bodies are unbounded, and an index document that grows
// without limit would make refreshes and highlights expensive.
const MaxCommentsIndexBytes = 64 << 10

// maxCommentBodyBytes bounds each comment before concatenation, so one huge
// comment cannot dominate the read.
const maxCommentBodyBytes = 4096

// maxCommentsRead bounds the number of comments scanned per document.
const maxCommentsRead = 200

// TicketDocument is the derived document indexed for one ticket. It is not a
// second source of truth: every field is copied from Postgres when an event
// is processed, and the version is the ticket row's version.
type TicketDocument struct {
	ID             uuid.UUID  `json:"-"`
	OrgID          uuid.UUID  `json:"org_id"`
	Number         int64      `json:"number"`
	Subject        string     `json:"subject"`
	CommentsText   string     `json:"comments_text"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	Tags           []string   `json:"tags"`
	AssigneeID     *uuid.UUID `json:"assignee_id,omitempty"`
	RequesterEmail string     `json:"requester_email"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Version        int64      `json:"version"`
}

// DocumentState is the current Postgres state for one ticket.
type DocumentState struct {
	Document TicketDocument
	Version  int64
	Found    bool
	Deleted  bool
}

// DocumentLoader reads the authoritative ticket state for indexing.
type DocumentLoader interface {
	LoadDocument(ctx context.Context, orgID, ticketID uuid.UUID) (DocumentState, error)
}

// DocumentRepository loads index documents from Postgres. Every query is
// scoped by org_id as well as ticket id, so a bad event payload cannot read
// another tenant's ticket.
type DocumentRepository struct {
	pool *pgxpool.Pool
}

// NewDocumentRepository creates the Postgres document source.
func NewDocumentRepository(pool *pgxpool.Pool) *DocumentRepository {
	return &DocumentRepository{pool: pool}
}

// LoadDocument returns the active document, or marks it deleted/missing. A
// deleted row keeps its version so the index job can delete with an external
// version and win against stale in-flight writes.
func (r *DocumentRepository) LoadDocument(ctx context.Context, orgID, ticketID uuid.UUID) (DocumentState, error) {
	if orgID == uuid.Nil || ticketID == uuid.Nil {
		return DocumentState{}, ErrMissingOrg
	}

	var (
		doc       TicketDocument
		deletedAt *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT t.id, t.org_id, t.number, t.subject, t.status, t.priority,
		       t.assignee_id, c.email AS requester_email,
		       t.created_at, t.updated_at, t.version, t.deleted_at
		FROM tickets t
		JOIN customers c ON c.id = t.requester_id
		WHERE t.org_id = $1 AND t.id = $2`,
		orgID, ticketID,
	).Scan(
		&doc.ID, &doc.OrgID, &doc.Number, &doc.Subject, &doc.Status, &doc.Priority,
		&doc.AssigneeID, &doc.RequesterEmail, &doc.CreatedAt, &doc.UpdatedAt,
		&doc.Version, &deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DocumentState{Found: false}, nil
	}
	if err != nil {
		return DocumentState{}, fmt.Errorf("load index document: %w", err)
	}

	state := DocumentState{Document: doc, Version: doc.Version, Found: true}
	if deletedAt != nil {
		state.Deleted = true
		return state, nil
	}

	comments, err := r.commentsText(ctx, orgID, ticketID)
	if err != nil {
		return DocumentState{}, err
	}
	doc.CommentsText = comments

	tags, err := r.tags(ctx, orgID, ticketID)
	if err != nil {
		return DocumentState{}, err
	}
	doc.Tags = tags

	state.Document = doc
	return state, nil
}

func (r *DocumentRepository) commentsText(ctx context.Context, orgID, ticketID uuid.UUID) (string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT left(body, $3)
		FROM comments
		WHERE ticket_id = $1 AND org_id = $2 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $4`,
		ticketID, orgID, maxCommentBodyBytes, maxCommentsRead,
	)
	if err != nil {
		return "", fmt.Errorf("load comments for index: %w", err)
	}
	defer rows.Close()

	var b strings.Builder
	b.Grow(MaxCommentsIndexBytes)
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return "", fmt.Errorf("scan comment for index: %w", err)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		appendCapped(&b, body)
		if b.Len() >= MaxCommentsIndexBytes {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("comments for index: %w", err)
	}
	return b.String(), nil
}

func (r *DocumentRepository) tags(ctx context.Context, orgID, ticketID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tg.name
		FROM ticket_tags tt
		JOIN tags tg ON tg.id = tt.tag_id
		WHERE tt.ticket_id = $1 AND tg.org_id = $2
		ORDER BY tg.name`,
		ticketID, orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("load tags for index: %w", err)
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan tag for index: %w", err)
		}
		tags = append(tags, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags for index: %w", err)
	}
	return tags, nil
}

// appendCapped appends as much of s as fits in the remaining budget without
// splitting a UTF-8 sequence.
func appendCapped(b *strings.Builder, s string) {
	remaining := MaxCommentsIndexBytes - b.Len()
	if remaining <= 0 {
		return
	}
	if len(s) > remaining {
		s = s[:remaining]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	b.WriteString(s)
}

// compile-time check that the Postgres loader satisfies the job dependency.
var _ DocumentLoader = (*DocumentRepository)(nil)
