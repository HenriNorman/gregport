package dashboard

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type countsService interface {
	Get(ctx context.Context, orgID uuid.UUID) (Counts, error)
}

// Handler serves the dashboard endpoints.
type Handler struct {
	svc countsService
}

// NewHandler creates a dashboard handler.
func NewHandler(svc countsService) *Handler {
	return &Handler{svc: svc}
}

// GetCounts returns the caller's organization ticket counts.
//
//	@Summary		Get dashboard ticket counts
//	@Description	Any authenticated member may read counts. Soft-deleted tickets are excluded. `open` is the sum of `new`, `open`, and `pending`; `unassigned` counts open tickets without an assignee; `overdue` counts unresolved tickets whose resolution due date has passed.
//	@Tags			dashboard
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	Counts
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/dashboard/counts [get]
func (h *Handler) GetCounts(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	counts, err := h.svc.Get(r.Context(), p.OrgID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, counts)
}

func unauthorized() *httpx.APIError {
	return httpx.Unauthorized("unauthorized", "authentication required")
}
