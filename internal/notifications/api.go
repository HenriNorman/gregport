package notifications

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// MarkAllReadResponse is the response for POST /v1/notifications/read-all.
type MarkAllReadResponse struct {
	Updated int64 `json:"updated" example:"12"`
}

// Handler serves the notification and notification-preference API.
type Handler struct {
	svc *Service
}

// NewHandler builds an HTTP handler for the notification API.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func unauthorized() *httpx.APIError {
	return httpx.Unauthorized("unauthorized", "authentication required")
}

func invalidNotificationID() *httpx.APIError {
	return httpx.BadRequest("invalid_id", "notification id must be a UUID")
}

func notificationNotFound() *httpx.APIError {
	return httpx.NotFound("notification_not_found", "notification not found")
}

// List returns the caller's notifications, newest first.
//
//	@Summary		List notifications
//	@Description	Returns the authenticated user's in-app notifications for the current org, newest first. Use unread=true to include only unread notifications. The cursor is opaque; pass next_cursor to fetch the next page.
//	@Tags			notifications
//	@Produce		json
//	@Security		BearerAuth
//	@Param			unread	query	boolean	false	"Only unread notifications"
//	@Param			limit	query	int		false	"Page size (default 25, max 100)"
//	@Param			cursor	query	string	false	"Opaque cursor from a previous next_cursor"
//	@Success		200		{object}	NotificationListResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed cursor"
//	@Failure		401		{object}	httpx.APIError
//	@Router			/v1/notifications [get]
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	q := r.URL.Query()
	items, next, err := h.svc.List(
		r.Context(), p.OrgID, p.ActorID,
		q.Get("unread") == "true",
		parseLimit(q.Get("limit")),
		q.Get("cursor"),
	)
	if err != nil {
		if errors.Is(err, ErrInvalidCursor) {
			httpx.WriteError(w, r, httpx.BadRequest("invalid_cursor", "cursor is invalid"))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, NotificationListResponse{
		Notifications: items,
		NextCursor:    next,
	})
}

// MarkRead marks one notification as read.
//
//	@Summary		Mark a notification read
//	@Description	Marks one of the caller's notifications as read. The operation is idempotent; an already-read notification still returns 204. A notification belonging to another user or org returns 404.
//	@Tags			notifications
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Notification UUID"
//	@Success		204
//	@Failure		400	{object}	httpx.APIError	"Malformed notification id"
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError	"Not found, or belongs to another user/org"
//	@Router			/v1/notifications/{id}/read [post]
func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, invalidNotificationID())
		return
	}

	if err := h.svc.MarkRead(r.Context(), p.OrgID, p.ActorID, id); err != nil {
		if errors.Is(err, ErrNotificationNotFound) {
			httpx.WriteError(w, r, notificationNotFound())
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkAllRead marks every unread notification for the caller as read.
//
//	@Summary		Mark all notifications read
//	@Description	Marks every unread in-app notification for the authenticated user in the current org as read. Returns how many rows changed.
//	@Tags			notifications
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MarkAllReadResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/notifications/read-all [post]
func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	n, err := h.svc.MarkAllRead(r.Context(), p.OrgID, p.ActorID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, MarkAllReadResponse{Updated: n})
}

// GetPreferences returns the caller's notification preferences.
//
//	@Summary		Get notification preferences
//	@Description	Returns the full event-type x channel preference matrix for the authenticated user. Preferences default to enabled; the response always includes every combination.
//	@Tags			notification-preferences
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	PreferenceListResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/notification-preferences [get]
func (h *Handler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	prefs, err := h.svc.Preferences(r.Context(), p.ActorID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, PreferenceListResponse{Preferences: prefs})
}

// UpdatePreferences replaces the caller's notification preferences.
//
//	@Summary		Update notification preferences
//	@Description	Replaces the authenticated user's notification preferences. Omitted entries revert to the default (enabled), so send the full matrix. Unknown event types or channels and duplicate entries are rejected.
//	@Tags			notification-preferences
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body	UpdatePreferencesRequest	true	"Notification preferences"
//	@Success		200		{object}	PreferenceListResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		422		{object}	httpx.APIError	"Unknown event type/channel or duplicate entry"
//	@Router			/v1/notification-preferences [put]
func (h *Handler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, unauthorized())
		return
	}

	var req UpdatePreferencesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	prefs, err := h.svc.UpdatePreferences(r.Context(), p.ActorID, req)
	if err != nil {
		var ae *httpx.APIError
		if errors.As(err, &ae) {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteError(w, r, httpx.Unprocessable("validation_failed", err.Error()))
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, PreferenceListResponse{Preferences: prefs})
}

func parseLimit(s string) int {
	const (
		defaultLimit = 25
		maxLimit     = 100
	)
	if s == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}
