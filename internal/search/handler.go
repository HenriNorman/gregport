package search

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type searchService interface {
	Search(ctx context.Context, p requestctx.Principal, f Filter) (Result, error)
}

// Handler serves GET /v1/search.
type Handler struct {
	svc searchService
}

// NewHandler creates the search HTTP handler.
func NewHandler(svc searchService) *Handler {
	return &Handler{svc: svc}
}

// Search returns full-text results for the caller's org.
//
//	@Summary		Search tickets and comments
//	@Description	Full-text search over ticket subjects and comment bodies, scoped to the caller's org. Optional facets filter by status, tag name and assignee UUID. Each hit carries HTML-safe highlight snippets with matched terms wrapped in <mark>. Results are ordered by relevance, then recency, and paginate with an opaque cursor. Search is eventually consistent after writes in the Elasticsearch backend (about one second); the Postgres fallback is synchronous. Another org's tickets are never returned.
//	@Tags			search
//	@Produce		json
//	@Security		BearerAuth
//	@Param			q			query		string	false	"Free-text query"
//	@Param			status		query		string	false	"Filter by status"	Enums(new,open,pending,solved,closed)
//	@Param			tag			query		string	false	"Filter by tag name"
//	@Param			assignee	query		string	false	"Filter by assignee UUID"
//	@Param			cursor		query		string	false	"Opaque cursor from a previous next_cursor"
//	@Param			limit		query		int		false	"Page size (default 25, max 100)"
//	@Success		200			{object}	SearchResponse
//	@Failure		401			{object}	httpx.APIError
//	@Failure		422			{object}	httpx.APIError	"Invalid filter, cursor or query"
//	@Failure		503			{object}	httpx.APIError	"Search backend unavailable"
//	@Router			/v1/search [get]
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}

	q := r.URL.Query()
	f := Filter{
		Query:  q.Get("q"),
		Cursor: q.Get("cursor"),
		Limit:  parseLimit(q.Get("limit")),
	}
	if s := q.Get("status"); s != "" {
		if !ValidStatus(s) {
			httpx.WriteError(w, r, invalidField("status", "status is not a valid ticket status"))
			return
		}
		f.Status = &s
	}
	if s := q.Get("tag"); s != "" {
		f.Tag = &s
	}
	if s := q.Get("assignee"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.WriteError(w, r, invalidField("assignee", "assignee must be a UUID"))
			return
		}
		f.AssigneeID = &id
	}

	res, err := h.svc.Search(r.Context(), p, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	dtos := make([]HitDTO, 0, len(res.Hits))
	for _, h := range res.Hits {
		dtos = append(dtos, AsDTO(h))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, SearchResponse{
		Results:    dtos,
		NextCursor: res.NextCursor,
	})
}

func invalidField(field, message string) *httpx.APIError {
	return &httpx.APIError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "invalid_search_filter",
		Message: "search filter is invalid",
		Details: []httpx.FieldError{{Field: field, Message: message}},
	}
}

func parseLimit(s string) int {
	if s == "" {
		return DefaultLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return DefaultLimit
	}
	return NormalizeLimit(n)
}
