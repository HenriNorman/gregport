package exports

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type exportService interface {
	Create(ctx context.Context, orgID, requestedBy uuid.UUID, req CreateRequest) (DTO, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (DTO, error)
}

// Handler serves the export endpoints.
type Handler struct {
	svc exportService
}

// NewHandler creates an export HTTP handler.
func NewHandler(svc exportService) *Handler {
	return &Handler{svc: svc}
}

// Create starts an asynchronous ticket CSV export.
//
//	@Summary		Create a ticket export
//	@Description	Starts an asynchronous CSV export of tickets in the caller's org. Optional filters narrow the export by status, priority, assignee, requester and creation time. The worker streams matching rows into private object storage with a keyset cursor, so a 100,000-row export uses flat memory. A completion email with a 24-hour signed link is sent to the requester.
//	@Tags			exports
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateRequest	true	"Optional export filters"
//	@Success		202		{object}	DTO
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		422		{object}	httpx.APIError	"Invalid filter"
//	@Router			/v1/exports [post]
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	var req CreateRequest
	// An empty body means "export everything"; only decode when one is sent.
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	dto, err := h.svc.Create(r.Context(), p.OrgID, p.ActorID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusAccepted, dto)
}

// Get returns the status of one export and a download link when ready.
//
//	@Summary		Get an export
//	@Description	Returns one export job scoped to the caller's org. While pending or running there is no download URL. A finished export carries a presigned URL valid for 24 hours. Another org's export returns 404.
//	@Tags			exports
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Export UUID"
//	@Success		200	{object}	DTO
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/exports/{id} [get]
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_export_id", "export id is invalid"))
		return
	}
	dto, err := h.svc.Get(r.Context(), p.OrgID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, dto)
}
