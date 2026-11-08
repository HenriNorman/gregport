package inboundemail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Handler serves the inbound email webhook. It is intentionally unauthenticated
// by session/token: the provider proves itself with a signed request, and the
// message body is the signed payload.
type Handler struct {
	svc inboundService
}

type inboundService interface {
	Process(ctx context.Context, rawBody []byte, timestamp, signature string) error
}

// NewHandler creates the inbound email handler.
func NewHandler(svc inboundService) *Handler {
	return &Handler{svc: svc}
}

// Submit receives one inbound email from the provider.
//
//	@Summary		Receive an inbound email
//	@Description	Provider webhook for inbound email. The raw body is the signed payload; authenticity is proven via X-Inbound-Email-Timestamp and X-Inbound-Email-Signature (HMAC-SHA256). A duplicate Message-ID is idempotent.
//	@Tags			inbound-email
//	@Produce		plain
//	@Param			X-Inbound-Email-Timestamp	header	string	true	"Unix seconds when the provider signed the message"
//	@Param			X-Inbound-Email-Signature	header	string	true	"HMAC-SHA256 signature hex encoded"
//	@Success		202		"Accepted"
//	@Failure		400		{object}	httpx.APIError	"Malformed or unverifiable message"
//	@Failure		401		{object}	httpx.APIError	"Invalid or expired signature"
//	@Failure		422		{object}	httpx.APIError	"Invalid input"
//	@Failure		429		{object}	httpx.APIError	"Rate limited"
//	@Failure		500		{object}	httpx.APIError	"Internal error"
//	@Router			/v1/inbound/email [post]
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_body", "cannot read request body"))
		return
	}

	timestamp := r.Header.Get(HeaderTimestamp)
	signature := r.Header.Get(HeaderSignature)

	if err := h.svc.Process(r.Context(), rawBody, timestamp, signature); err != nil {
		switch {
		case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrExpiredSignature):
			httpx.WriteError(w, r, httpx.Unauthorized("invalid_signature", err.Error()))
		case errors.Is(err, ErrDuplicateMessageID):
			w.WriteHeader(http.StatusAccepted)
		case errors.Is(err, ErrOrgNotFound), errors.Is(err, ErrTicketNotFound):
			httpx.WriteError(w, r, httpx.Unprocessable("unroutable", err.Error()))
		case errors.Is(err, ErrInvalidInput):
			httpx.WriteError(w, r, httpx.Unprocessable("invalid_input", err.Error()))
		default:
			httpx.WriteError(w, r, fmt.Errorf("process inbound email: %w", err))
		}
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// Routes registers the inbound email routes on a public chi router.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/v1/inbound/email", h.Submit)
}
