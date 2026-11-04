package sla

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type policyService interface {
	List(ctx context.Context, orgID uuid.UUID) ([]Policy, error)
	Get(ctx context.Context, orgID, policyID uuid.UUID) (Policy, error)
	Create(ctx context.Context, orgID uuid.UUID, req CreatePolicyRequest) (Policy, error)
	Update(ctx context.Context, orgID, policyID uuid.UUID, req UpdatePolicyRequest) (Policy, error)
	Delete(ctx context.Context, orgID, policyID uuid.UUID) error
}

// Handler serves the SLA policy endpoints.
type Handler struct {
	svc policyService
}

// NewHandler creates an SLA policy handler.
func NewHandler(svc policyService) *Handler {
	return &Handler{svc: svc}
}

// ListPolicies returns every SLA policy in the caller's org.
//
//	@Summary		List SLA policies
//	@Description	Any authenticated member may list policies. Results are scoped to the caller's organization and returned in creation order.
//	@Tags			sla
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	PolicyListResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/sla-policies [get]
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	policies, err := h.svc.List(r.Context(), p.OrgID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	dtos := make([]PolicyDTO, 0, len(policies))
	for _, policy := range policies {
		dtos = append(dtos, toPolicyDTO(policy))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, PolicyListResponse{Policies: dtos})
}

// GetPolicy returns one SLA policy.
//
//	@Summary		Get an SLA policy
//	@Description	Any authenticated member may read a policy. A policy belonging to another org returns 404.
//	@Tags			sla
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Policy UUID"
//	@Success		200	{object}	PolicyDTO
//	@Failure		400	{object}	httpx.APIError	"id is not a UUID"
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/sla-policies/{id} [get]
func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	policyID, err := parsePolicyID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	policy, err := h.svc.Get(r.Context(), p.OrgID, policyID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toPolicyDTO(policy))
}

// CreatePolicy creates an SLA policy.
//
//	@Summary		Create an SLA policy
//	@Description	Requires role `owner` or `admin`. An org may have at most one policy per priority. Resolution time must be greater than or equal to the first-response time.
//	@Tags			sla
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreatePolicyRequest	true	"Policy payload"
//	@Success		201		{object}	PolicyDTO
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		409		{object}	httpx.APIError	"A policy for this priority already exists"
//	@Failure		422		{object}	httpx.APIError	"Validation failed"
//	@Router			/v1/sla-policies [post]
func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	var req CreatePolicyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	policy, err := h.svc.Create(r.Context(), p.OrgID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, toPolicyDTO(policy))
}

// UpdatePolicy applies a partial update to an SLA policy.
//
//	@Summary		Update an SLA policy
//	@Description	Requires role `owner` or `admin`. Omitted fields keep their current value. Changing the priority to one already used by another policy returns 409.
//	@Tags			sla
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Policy UUID"
//	@Param			body	body		UpdatePolicyRequest	true	"Fields to change"
//	@Success		200		{object}	PolicyDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError
//	@Failure		409		{object}	httpx.APIError	"Priority already used by another policy"
//	@Failure		422		{object}	httpx.APIError
//	@Router			/v1/sla-policies/{id} [patch]
func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	policyID, err := parsePolicyID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	var req UpdatePolicyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	policy, err := h.svc.Update(r.Context(), p.OrgID, policyID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toPolicyDTO(policy))
}

// DeletePolicy removes an SLA policy.
//
//	@Summary		Delete an SLA policy
//	@Description	Requires role `owner` or `admin`. A policy still attached to a ticket returns 409 instead of being deleted.
//	@Tags			sla
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Policy UUID"
//	@Success		204
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		403	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Failure		409	{object}	httpx.APIError	"Policy is referenced by a ticket"
//	@Router			/v1/sla-policies/{id} [delete]
func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	policyID, err := parsePolicyID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), p.OrgID, policyID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parsePolicyID(r *http.Request) (uuid.UUID, error) {
	policyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid_id", "sla policy id must be a UUID")
	}
	return policyID, nil
}

func toPolicyDTO(policy Policy) PolicyDTO {
	return PolicyDTO{
		ID:                   policy.ID,
		Name:                 policy.Name,
		Priority:             policy.Priority,
		FirstResponseMinutes: policy.FirstResponseMinutes,
		ResolutionMinutes:    policy.ResolutionMinutes,
		CreatedAt:            policy.CreatedAt.UTC(),
	}
}

func unauthorized() *httpx.APIError {
	return httpx.Unauthorized("unauthorized", "authentication required")
}
