package customers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type customerService interface {
	Erase(ctx context.Context, orgID, id uuid.UUID, actor requestctx.Principal) (Customer, error)
}

// Handler serves the customer erasure endpoint.
type Handler struct {
	svc customerService
}

// NewHandler creates a customer HTTP handler.
func NewHandler(svc customerService) *Handler {
	return &Handler{svc: svc}
}

// Erase anonymizes a customer while keeping their ticket history.
//
//	@Summary		Erase customer data
//	@Description	Anonymizes one customer in the caller's org: the email is replaced with a deterministic, org-scoped SHA-256 pseudonym, and the name and external id are cleared. Tickets, comments and events are retained so support history stays intact. The operation is idempotent: erasing an already-erased customer returns 204 without changing the pseudonym. A customer in another org returns 404.
//	@Tags			customers
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Customer UUID"
//	@Success		204
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		403	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError	"Customer not found in this org"
//	@Router			/v1/customers/{id}/data [delete]
func (h *Handler) Erase(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_customer_id", "customer id is invalid"))
		return
	}
	if _, err := h.svc.Erase(r.Context(), p.OrgID, id, p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
