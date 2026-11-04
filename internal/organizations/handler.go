package organizations

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// settingsService is the handler's dependency. Keeping it small makes the
// HTTP layer testable without a database.
type settingsService interface {
	Get(ctx context.Context, orgID uuid.UUID) (SettingsResponse, error)
	Update(ctx context.Context, orgID uuid.UUID, req UpdateSettingsRequest) (SettingsResponse, error)
}

// Handler serves the organization settings endpoints.
type Handler struct {
	svc settingsService
}

// NewHandler creates a settings handler.
func NewHandler(svc settingsService) *Handler {
	return &Handler{svc: svc}
}

// GetSettings returns the caller's organization settings.
//
//	@Summary		Get organization settings
//	@Description	Any authenticated member may read settings. Fields missing from the stored document fall back to `UTC` for the timezone and `normal` for the default priority.
//	@Tags			organizations
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	SettingsResponse
//	@Failure		401	{object}	httpx.APIError	"Missing or invalid credentials"
//	@Failure		404	{object}	httpx.APIError	"Organization not found"
//	@Router			/v1/organization/settings [get]
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	resp, err := h.svc.Get(r.Context(), p.OrgID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

// UpdateSettings applies a partial update to the caller's organization settings.
//
//	@Summary		Update organization settings
//	@Description	Requires role `owner` or `admin`. Omitted fields keep their current value. The timezone must be a valid IANA name, the default priority must be one of `low`, `normal`, `high`, `urgent`, and the support email must be a bare address. Send an empty string to clear the support email.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		UpdateSettingsRequest	true	"Fields to change"
//	@Success		200		{object}	SettingsResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError	"Missing or invalid credentials"
//	@Failure		403		{object}	httpx.APIError	"Insufficient permissions"
//	@Failure		404		{object}	httpx.APIError	"Organization not found"
//	@Failure		422		{object}	httpx.APIError	"Invalid timezone, priority, or support email"
//	@Router			/v1/organization/settings [patch]
func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	var req UpdateSettingsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	resp, err := h.svc.Update(r.Context(), p.OrgID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

func unauthorized() *httpx.APIError {
	return httpx.Unauthorized("unauthorized", "authentication required")
}
