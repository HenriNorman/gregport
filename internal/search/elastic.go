package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TicketStore hydrates canonical ticket rows after Elasticsearch returns ids.
// The index is derived data; Postgres is always the source of truth.
type TicketStore interface {
	TicketsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TicketHit, error)
}

// ElasticBackend is the primary search implementation. It queries the alias,
// never a concrete index, so a reindex can swap indexes underneath it.
type ElasticBackend struct {
	client *Client
	store  TicketStore
}

// NewElasticBackend wires the Elasticsearch backend to its Postgres hydrator.
func NewElasticBackend(client *Client, store TicketStore) *ElasticBackend {
	return &ElasticBackend{client: client, store: store}
}

// Search runs an org-scoped query. The org id is always a required filter
// clause built here, not by the caller, so an unscoped query cannot be issued
// through this backend.
func (b *ElasticBackend) Search(ctx context.Context, orgID uuid.UUID, f Filter) (Result, error) {
	if orgID == uuid.Nil {
		return Result{}, ErrMissingOrg
	}
	queryText := strings.TrimSpace(f.Query)
	if len(queryText) > MaxQueryBytes {
		return Result{}, errQueryTooLong
	}
	if f.Status != nil && !ValidStatus(*f.Status) {
		return Result{}, errInvalidStatus
	}
	f.Limit = NormalizeLimit(f.Limit)

	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return Result{}, ErrInvalidCursor
	}

	body, err := b.buildQuery(orgID, f, queryText, cur)
	if err != nil {
		return Result{}, err
	}
	raw, err := b.search(ctx, body)
	if err != nil {
		return Result{}, err
	}

	// The extra hit only tells us whether another page exists. The cursor is
	// based on the last hit we keep, which is always deterministic.
	var next string
	if len(raw) > f.Limit {
		raw = raw[:f.Limit]
		last := raw[len(raw)-1]
		next = encodeCursor(last.rank, last.createdAt, last.id, last.number)
	}

	ids := make([]uuid.UUID, 0, len(raw))
	for _, h := range raw {
		ids = append(ids, h.id)
	}
	canonical, err := b.store.TicketsByIDs(ctx, orgID, ids)
	if err != nil {
		return Result{}, err
	}

	hits := make([]TicketHit, 0, len(raw))
	for _, h := range raw {
		row, ok := canonical[h.id]
		if !ok {
			// Deleted after indexing, or hydrated from a foreign org. The
			// index is eventually consistent; Postgres wins.
			continue
		}
		row.Rank = h.rank
		row.SubjectHighlight = h.subjectHighlight
		row.CommentHighlight = h.commentHighlight
		hits = append(hits, row)
	}
	return Result{Hits: hits, NextCursor: next}, nil
}

// esSearchResponse is the subset of the search response this package needs.
type esSearchResponse struct {
	Hits struct {
		Hits []struct {
			ID        string              `json:"_id"`
			Score     *float64            `json:"_score"`
			Sort      []json.RawMessage   `json:"sort"`
			Highlight map[string][]string `json:"highlight"`
		} `json:"hits"`
	} `json:"hits"`
}

type rawHit struct {
	id               uuid.UUID
	number           int64
	rank             float64
	createdAt        time.Time
	subjectHighlight string
	commentHighlight string
}

func (b *ElasticBackend) search(ctx context.Context, body []byte) ([]rawHit, error) {
	res, err := b.client.ES().Search(
		b.client.ES().Search.WithContext(ctx),
		b.client.ES().Search.WithIndex(b.client.Alias()),
		b.client.ES().Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: search request: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	if err := ResponseError(res); err != nil {
		return nil, err
	}

	var payload esSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("search: decode elasticsearch response: %w", err)
	}

	out := make([]rawHit, 0, len(payload.Hits.Hits))
	for _, h := range payload.Hits.Hits {
		id, err := uuid.Parse(h.ID)
		if err != nil {
			return nil, fmt.Errorf("search: elasticsearch returned non-uuid document id %q", h.ID)
		}
		hit := rawHit{id: id, rank: 0}
		if h.Score != nil {
			hit.rank = *h.Score
		}
		if err := parseSortValues(h.Sort, &hit); err != nil {
			return nil, err
		}
		hit.subjectHighlight = joinHighlight(h.Highlight["subject"])
		hit.commentHighlight = joinHighlight(h.Highlight["comments_text"])
		out = append(out, hit)
	}
	return out, nil
}

// parseSortValues reads the tuple returned by the sort clause. Both tuples
// end with the per-org ticket number; the score is only present for queries.
func parseSortValues(raw []json.RawMessage, hit *rawHit) error {
	if len(raw) == 0 {
		return fmt.Errorf("search: elasticsearch hit is missing sort values")
	}
	if len(raw) == 3 {
		if err := json.Unmarshal(raw[0], &hit.rank); err != nil {
			return fmt.Errorf("search: decode score sort: %w", err)
		}
		raw = raw[1:]
	}
	var millis int64
	if err := json.Unmarshal(raw[0], &millis); err != nil {
		return fmt.Errorf("search: decode created_at sort: %w", err)
	}
	hit.createdAt = time.UnixMilli(millis).UTC()
	if err := json.Unmarshal(raw[1], &hit.number); err != nil {
		return fmt.Errorf("search: decode number sort: %w", err)
	}
	return nil
}

func joinHighlight(fragments []string) string {
	if len(fragments) == 0 {
		return ""
	}
	return strings.Join(fragments, " … ")
}

func (b *ElasticBackend) buildQuery(orgID uuid.UUID, f Filter, queryText string, cur cursor) ([]byte, error) {
	limit := f.Limit + 1
	filters := []any{
		map[string]any{"term": map[string]any{"org_id": orgID.String()}},
	}
	if f.Status != nil {
		filters = append(filters, map[string]any{"term": map[string]any{"status": *f.Status}})
	}
	if f.Tag != nil {
		filters = append(filters, map[string]any{"term": map[string]any{"tags": *f.Tag}})
	}
	if f.AssigneeID != nil {
		filters = append(filters, map[string]any{"term": map[string]any{"assignee_id": f.AssigneeID.String()}})
	}

	boolQuery := map[string]any{"filter": filters}
	body := map[string]any{
		"size":             limit,
		"track_total_hits": false,
		"_source":          false,
	}

	var sort []any
	if queryText != "" {
		boolQuery["must"] = []any{
			map[string]any{
				"multi_match": map[string]any{
					"query":    queryText,
					"fields":   []string{"subject^3", "comments_text"},
					"type":     "best_fields",
					"operator": "and",
				},
			},
		}
		body["highlight"] = map[string]any{
			"pre_tags":  []string{hlStart},
			"post_tags": []string{hlEnd},
			"fields": map[string]any{
				"subject":       map[string]any{"number_of_fragments": 0},
				"comments_text": map[string]any{"fragment_size": 160, "number_of_fragments": 2},
			},
		}
		sort = []any{
			map[string]any{"_score": map[string]any{"order": "desc"}},
			map[string]any{"created_at": map[string]any{"order": "desc"}},
			map[string]any{"number": map[string]any{"order": "desc"}},
		}
	} else {
		// Empty query lists the org's tickets by recency. Rank stays zero so
		// the cursor shape is shared with the relevance path.
		sort = []any{
			map[string]any{"created_at": map[string]any{"order": "desc"}},
			map[string]any{"number": map[string]any{"order": "desc"}},
		}
	}
	body["query"] = map[string]any{"bool": boolQuery}
	body["sort"] = sort

	if f.Cursor != "" {
		if cur.Number == 0 {
			return nil, ErrInvalidCursor
		}
		if queryText != "" {
			body["search_after"] = []any{cur.Rank, cur.CreatedAt.UnixMilli(), cur.Number}
		} else {
			body["search_after"] = []any{cur.CreatedAt.UnixMilli(), cur.Number}
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("search: build elasticsearch query: %w", err)
	}
	return raw, nil
}

// Compile-time check: the Elasticsearch backend is one of the swappable
// backends the service can use.
var _ Backend = (*ElasticBackend)(nil)

// FallbackBackend tries the primary (Elasticsearch) backend first and falls
// back to the secondary (Postgres) backend when the cluster is unavailable or
// the index does not exist yet. Writes always go to Elasticsearch through the
// sync jobs; reads are always served, even when search is down.
type FallbackBackend struct {
	primary  Backend
	fallback Backend
}

// NewFallbackBackend wraps a primary backend with a secondary fallback. The
// primary is queried first; only ErrSearchUnavailable and ErrIndexNotFound
// trigger a fallback. All other errors are returned as-is.
func NewFallbackBackend(primary, fallback Backend) *FallbackBackend {
	return &FallbackBackend{primary: primary, fallback: fallback}
}

// Search queries the primary backend, falling back to the secondary when the
// cluster is down or the index is missing. The org filter is mandatory in both
// backends, so tenant isolation is never compromised by a fallback.
func (b *FallbackBackend) Search(ctx context.Context, orgID uuid.UUID, f Filter) (Result, error) {
	res, err := b.primary.Search(ctx, orgID, f)
	if err == nil {
		return res, nil
	}
	if errors.Is(err, ErrSearchUnavailable) || errors.Is(err, ErrIndexNotFound) {
		return b.fallback.Search(ctx, orgID, f)
	}
	return Result{}, err
}
