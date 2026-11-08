package webhooks

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type webhookService interface {
	List(ctx context.Context, orgID uuid.UUID) ([]Subscription, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Subscription, error)
	Create(ctx context.Context, orgID uuid.UUID, req CreateSubscriptionRequest) (Subscription, error)
	Update(ctx context.Context, orgID, id uuid.UUID, req UpdateSubscriptionRequest) (Subscription, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	ListDeliveries(ctx context.Context, orgID, subscriptionID uuid.UUID, limit int, cursor string) ([]Delivery, string, error)
	Test(ctx context.Context, orgID, id uuid.UUID) (Delivery, error)
	Redeliver(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, error)
}

// Handler serves the webhook endpoints.
type Handler struct {
	svc webhookService
}

// NewHandler creates a webhook HTTP handler.
func NewHandler(svc webhookService) *Handler {
	return &Handler{svc: svc}
}

// List returns every webhook subscription in the caller's org.
//
//	@Summary		List webhook subscriptions
//	@Description	Any authenticated member may list subscriptions. The HMAC secret is never returned by this endpoint.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	SubscriptionListResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/webhooks [get]
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	subs, err := h.svc.List(r.Context(), p.OrgID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dtos := make([]SubscriptionDTO, 0, len(subs))
	for _, sub := range subs {
		dtos = append(dtos, toSubscriptionDTO(sub, false))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, SubscriptionListResponse{Webhooks: dtos})
}

// Get returns one webhook subscription.
//
//	@Summary		Get a webhook subscription
//	@Description	Any authenticated member may read a subscription. A subscription in another org returns 404.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Webhook UUID"
//	@Success		200	{object}	SubscriptionDTO
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/webhooks/{id} [get]
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	id, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sub, err := h.svc.Get(r.Context(), p.OrgID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toSubscriptionDTO(sub, false))
}

// Create registers a webhook subscription.
//
//	@Summary		Create a webhook subscription
//	@Description	Requires role `owner` or `admin`. The URL must be an absolute http(s) URL whose host resolves only to public addresses; loopback, private, link-local and metadata addresses are rejected. The response contains the HMAC secret exactly once.
//	@Tags			webhooks
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateSubscriptionRequest	true	"Subscription payload"
//	@Success		201		{object}	SubscriptionDTO
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		422		{object}	httpx.APIError	"Invalid URL or events"
//	@Router			/v1/webhooks [post]
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	var req CreateSubscriptionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sub, err := h.svc.Create(r.Context(), p.OrgID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, toSubscriptionDTO(sub, true))
}

// Update applies a partial update to a subscription.
//
//	@Summary		Update a webhook subscription
//	@Description	Requires role `owner` or `admin`. Omitted fields keep their value. Setting `is_active` to true re-enables a subscription disabled after repeated failures and resets its failure counter.
//	@Tags			webhooks
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Webhook UUID"
//	@Param			body	body		UpdateSubscriptionRequest	true	"Fields to change"
//	@Success		200		{object}	SubscriptionDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError
//	@Failure		422		{object}	httpx.APIError
//	@Router			/v1/webhooks/{id} [patch]
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	id, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req UpdateSubscriptionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sub, err := h.svc.Update(r.Context(), p.OrgID, id, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toSubscriptionDTO(sub, false))
}

// Delete removes a subscription and its delivery history.
//
//	@Summary		Delete a webhook subscription
//	@Description	Requires role `owner` or `admin`. Delivery-log rows for the subscription are removed with it.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Webhook UUID"
//	@Success		204
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		403	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/webhooks/{id} [delete]
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	id, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), p.OrgID, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Test sends a synchronous ping.
//
//	@Summary		Test a webhook subscription
//	@Description	Requires role `owner` or `admin`. Sends one `webhook.ping` envelope immediately and records it in the delivery log. A failed receiver is reported in the delivery status rather than as an HTTP error. Disabled subscriptions return 409.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Webhook UUID"
//	@Success		200	{object}	DeliveryDTO
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		403	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Failure		409	{object}	httpx.APIError	"Subscription is disabled"
//	@Router			/v1/webhooks/{id}/test [post]
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	id, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	delivery, err := h.svc.Test(r.Context(), p.OrgID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toDeliveryDTO(delivery))
}

// ListDeliveries returns the delivery log for a subscription.
//
//	@Summary		List webhook deliveries
//	@Description	Any authenticated member may read the delivery log. Results are newest first and keyset-paginated with `limit` (default 50, maximum 200) and `cursor`.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Webhook UUID"
//	@Param			limit	query		int		false	"Page size (1-200)"
//	@Param			cursor	query		string	false	"Opaque next_cursor from a previous page"
//	@Success		200		{object}	DeliveryListResponse
//	@Failure		400		{object}	httpx.APIError	"Invalid id, limit or cursor"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError
//	@Router			/v1/webhooks/{id}/deliveries [get]
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	id, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	deliveries, next, err := h.svc.ListDeliveries(r.Context(), p.OrgID, id, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dtos := make([]DeliveryDTO, 0, len(deliveries))
	for _, d := range deliveries {
		dtos = append(dtos, toDeliveryDTO(d))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, DeliveryListResponse{Deliveries: dtos, NextCursor: next})
}

// Redeliver performs one immediate manual attempt.
//
//	@Summary		Redeliver a webhook delivery
//	@Description	Requires role `owner` or `admin`. Resets the delivery-log row and attempts it once immediately. Manual redelivery does not count toward the subscription circuit breaker and does not schedule an automatic retry.
//	@Tags			webhooks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string	true	"Webhook UUID"
//	@Param			deliveryId	path		string	true	"Delivery UUID"
//	@Success		200			{object}	DeliveryDTO
//	@Failure		400			{object}	httpx.APIError
//	@Failure		401			{object}	httpx.APIError
//	@Failure		403			{object}	httpx.APIError
//	@Failure		404			{object}	httpx.APIError
//	@Router			/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver [post]
func (h *Handler) Redeliver(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}
	subscriptionID, err := parseWebhookID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	deliveryID, err := uuid.Parse(chi.URLParam(r, "deliveryId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_delivery_id", "delivery id must be a UUID"))
		return
	}
	delivery, err := h.svc.Redeliver(r.Context(), p.OrgID, subscriptionID, deliveryID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toDeliveryDTO(delivery))
}

func parseWebhookID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid_id", "webhook id must be a UUID")
	}
	return id, nil
}

func parseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 200 {
		return 0, httpx.BadRequest("invalid_limit", "limit must be between 1 and 200")
	}
	return limit, nil
}

func toSubscriptionDTO(sub Subscription, includeSecret bool) SubscriptionDTO {
	dto := SubscriptionDTO{
		ID:                  sub.ID,
		URL:                 sub.URL,
		Events:              sub.Events,
		IsActive:            sub.IsActive,
		ConsecutiveFailures: sub.ConsecutiveFailures,
		DisabledAt:          sub.DisabledAt,
		CreatedAt:           sub.CreatedAt.UTC(),
		UpdatedAt:           sub.UpdatedAt.UTC(),
	}
	if includeSecret {
		dto.Secret = sub.Secret
	}
	return dto
}

func toDeliveryDTO(d Delivery) DeliveryDTO {
	dto := DeliveryDTO{
		ID:             d.ID,
		EventID:        d.EventID,
		EventType:      d.EventType,
		Status:         d.Status,
		Attempts:       d.Attempts,
		ResponseStatus: d.ResponseStatus,
		NextRetryAt:    d.NextRetryAt,
		DeliveredAt:    d.DeliveredAt,
		DurationMS:     d.DurationMS,
		CreatedAt:      d.CreatedAt.UTC(),
	}
	if d.ResponseBody != nil {
		dto.ResponseBody = *d.ResponseBody
	}
	if d.LastError != nil {
		dto.LastError = *d.LastError
	}
	return dto
}

func unauthorized() *httpx.APIError {
	return httpx.Unauthorized("unauthorized", "authentication required")
}
