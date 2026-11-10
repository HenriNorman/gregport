package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Repository is the Postgres full-text search backend. It is the baseline and
// the fallback when Elasticsearch is unavailable.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates the Postgres search repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Search runs a full-text query scoped to one organisation. The org id is a
// required argument and is always bound as $1; there is no code path that can
// build a search without it.
func (r *Repository) Search(ctx context.Context, orgID uuid.UUID, f Filter) (Result, error) {
	if orgID == uuid.Nil {
		return Result{}, ErrMissingOrg
	}
	queryText := strings.TrimSpace(f.Query)
	if len(queryText) > MaxQueryBytes {
		return Result{}, httpx.Unprocessable("invalid_search_query", "search query is too long")
	}
	if f.Status != nil && !ValidStatus(*f.Status) {
		return Result{}, httpx.Unprocessable("invalid_search_filter", "status is not a valid ticket status")
	}
	f.Limit = NormalizeLimit(f.Limit)

	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return Result{}, ErrInvalidCursor
	}

	var (
		args  []any
		sql   string
		limit = f.Limit + 1
	)

	if queryText != "" {
		args = []any{orgID, queryText}
		filterClause := buildFilterClause(&args, f)
		cursorClause, limitIdx := buildCursorClause(&args, f.Cursor, cur, limit)
		sql = fmt.Sprintf(ftsQuery,
			headlineOptions(0, 50),
			headlineOptions(2, 30),
			filterClause,
			cursorClause,
			limitIdx,
		)
	} else {
		args = []any{orgID}
		filterClause := buildFilterClause(&args, f)
		cursorClause, limitIdx := buildCursorClause(&args, f.Cursor, cur, limit)
		sql = fmt.Sprintf(listQuery, filterClause, cursorClause, limitIdx)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return Result{}, fmt.Errorf("search tickets: %w", err)
	}
	defer rows.Close()

	out := make([]TicketHit, 0, f.Limit)
	for rows.Next() {
		var h TicketHit
		if err := rows.Scan(
			&h.ID, &h.OrgID, &h.Number, &h.Subject, &h.Status, &h.Priority,
			&h.AssigneeID, &h.RequesterEmail, &h.CreatedAt, &h.UpdatedAt,
			&h.Rank, &h.SubjectHighlight, &h.CommentHighlight,
		); err != nil {
			return Result{}, fmt.Errorf("scan search hit: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("search rows: %w", err)
	}

	var next string
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.Rank, last.CreatedAt, last.ID)
	}

	return Result{Hits: out, NextCursor: next}, nil
}

// buildFilterClause appends optional facet parameters and returns the SQL
// fragment that filters on them. It always references t.* so it composes with
// both query paths.
func buildFilterClause(args *[]any, f Filter) string {
	var b strings.Builder
	add := func(v any) int {
		*args = append(*args, v)
		return len(*args)
	}
	if f.Status != nil {
		b.WriteString(fmt.Sprintf(" AND t.status = $%d", add(*f.Status)))
	}
	if f.AssigneeID != nil {
		b.WriteString(fmt.Sprintf(" AND t.assignee_id = $%d", add(*f.AssigneeID)))
	}
	if f.Tag != nil {
		b.WriteString(fmt.Sprintf(
			" AND EXISTS (SELECT 1 FROM ticket_tags tt JOIN tags tg ON tg.id = tt.tag_id"+
				" WHERE tt.ticket_id = t.id AND tg.org_id = $1 AND tg.name = $%d)",
			add(*f.Tag),
		))
	}
	return b.String()
}

// buildCursorClause appends the cursor parameters and returns the predicate
// plus the placeholder index for the LIMIT value.
func buildCursorClause(args *[]any, raw string, cur cursor, limit int) (string, int) {
	clause := "true"
	if raw != "" {
		start := len(*args) + 1
		*args = append(*args, true, cur.Rank, cur.CreatedAt, cur.ID)
		clause = fmt.Sprintf(
			"($%d = false OR (rank, created_at, id) < ($%d, $%d, $%d))",
			start, start+1, start+2, start+3,
		)
	}
	limitIdx := len(*args) + 1
	*args = append(*args, limit)
	return clause, limitIdx
}

// ftsQuery selects candidate tickets from the subject index and the comment
// index, merges their best rank, then highlights the winning comment. The
// candidate CTEs keep the expensive per-ticket work proportional to matches,
// not to the size of the organisation.
const ftsQuery = `
WITH q AS (
    SELECT websearch_to_tsquery('english', $2) AS query
),
subject_hits AS (
    SELECT t.id, ts_rank(t.search_vector, q.query)::double precision AS rank
    FROM tickets t, q
    WHERE t.org_id = $1
      AND t.deleted_at IS NULL
      AND t.search_vector @@ q.query
),
comment_hits AS (
    SELECT cm.ticket_id AS id,
           max(ts_rank(cm.search_vector, q.query))::double precision AS rank
    FROM comments cm
    JOIN tickets t ON t.id = cm.ticket_id
    CROSS JOIN q
    WHERE t.org_id = $1
      AND t.deleted_at IS NULL
      AND cm.deleted_at IS NULL
      AND cm.search_vector @@ q.query
    GROUP BY cm.ticket_id
),
merged AS (
    SELECT id, max(rank) AS rank
    FROM (
        SELECT id, rank FROM subject_hits
        UNION ALL
        SELECT id, rank FROM comment_hits
    ) u
    GROUP BY id
),
hits AS (
    SELECT t.id, t.org_id, t.number, t.subject, t.status, t.priority,
           t.assignee_id, c.email AS requester_email,
           t.created_at, t.updated_at, m.rank,
           ts_headline('english', t.subject, q.query, '%s') AS subject_highlight,
           COALESCE((
               SELECT ts_headline('english', cm.body, q.query, '%s')
               FROM comments cm
               WHERE cm.ticket_id = t.id
                 AND cm.deleted_at IS NULL
                 AND cm.search_vector @@ q.query
               ORDER BY ts_rank(cm.search_vector, q.query) DESC
               LIMIT 1
           ), '') AS comment_highlight
    FROM merged m
    JOIN tickets t ON t.id = m.id
    JOIN customers c ON c.id = t.requester_id
    CROSS JOIN q
    WHERE true %s
)
SELECT id, org_id, number, subject, status, priority, assignee_id,
       requester_email, created_at, updated_at, rank,
       subject_highlight, comment_highlight
FROM hits
WHERE %s
ORDER BY rank DESC, created_at DESC, id DESC
LIMIT $%d
`

// listQuery is the empty-query path: a faceted listing scoped to the org,
// ordered by recency. It uses the same cursor shape with rank zero so
// pagination code is shared with the full-text path.
const listQuery = `
WITH hits AS (
    SELECT t.id, t.org_id, t.number, t.subject, t.status, t.priority,
           t.assignee_id, c.email AS requester_email,
           t.created_at, t.updated_at, 0::double precision AS rank,
           ''::text AS subject_highlight, ''::text AS comment_highlight
    FROM tickets t
    JOIN customers c ON c.id = t.requester_id
    WHERE t.org_id = $1 AND t.deleted_at IS NULL %s
)
SELECT id, org_id, number, subject, status, priority, assignee_id,
       requester_email, created_at, updated_at, rank,
       subject_highlight, comment_highlight
FROM hits
WHERE %s
ORDER BY rank DESC, created_at DESC, id DESC
LIMIT $%d
`
