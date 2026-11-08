package ratings

import (
	"errors"
	"net/http"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Handler serves the public rating endpoint.
type Handler struct {
	svc *Service
}

// NewHandler builds the public rating handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Submit records a satisfaction rating from a signed link.
//
//	@Summary		Submit a satisfaction rating
//	@Description	Public endpoint used by the solved-ticket email link. The token is an HMAC-signed, expiring token tied to one ticket and customer, so no session is required. Each ticket accepts exactly one rating. The endpoint is tightly rate limited per client IP.
//	@Tags			ratings
//	@Accept			json
//	@Produce		json
//	@Param			body	body		CreateRequest	true	"Signed rating token and score"
//	@Success		201		{object}	RatingResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON"
//	@Failure		409		{object}	httpx.APIError	"Ticket has already been rated"
//	@Failure		422		{object}	httpx.APIError	"Invalid or expired token, or invalid score"
//	@Failure		429		{object}	httpx.APIError	"Rate limited"
//	@Router			/v1/public/ratings [post]
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	rating, err := h.svc.Submit(r.Context(), req)
	switch {
	case err == nil:
		_ = httpx.WriteJSON(w, http.StatusCreated, rating)
	case errors.Is(err, ErrInvalidScore):
		httpx.WriteError(w, r, httpx.Unprocessable("invalid_score", "score must be between 1 and 5"))
	case errors.Is(err, ErrInvalidComment):
		httpx.WriteError(w, r, httpx.Unprocessable("invalid_comment", "comment is too long"))
	case errors.Is(err, ErrAlreadyRated):
		httpx.WriteError(w, r, httpx.Conflict("already_rated", "this ticket has already been rated"))
	case errors.Is(err, ErrExpiredToken):
		httpx.WriteError(w, r, httpx.Unprocessable("expired_token", "rating link has expired"))
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrRatingNotAvailable):
		// A tampered token and a token for a missing ticket look the same, so
		// an attacker cannot probe ticket ids.
		httpx.WriteError(w, r, httpx.Unprocessable("invalid_token", "rating link is not valid"))
	default:
		httpx.WriteError(w, r, err)
	}
}
