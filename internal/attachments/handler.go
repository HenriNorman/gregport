package attachments

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type attachmentService interface {
	Presign(ctx context.Context, orgID uuid.UUID, ticketNumber int64, req PresignRequest) (PresignResponse, error)
	Confirm(ctx context.Context, orgID, id uuid.UUID) (Attachment, error)
	Download(ctx context.Context, orgID, id uuid.UUID) (DownloadResponse, error)
}

// Handler serves the attachment endpoints.
type Handler struct {
	svc attachmentService
}

// NewHandler creates an attachment HTTP handler.
func NewHandler(svc attachmentService) *Handler {
	return &Handler{svc: svc}
}

// Presign validates file metadata and returns a short-lived upload URL.
//
//	@Summary		Presign an attachment upload
//	@Description	Validates the declared filename, MIME type and size, creates a pending attachment and returns a presigned PUT URL valid for a few minutes. The client uploads directly to storage; the object key is server-generated and tenant-prefixed. Files over the configured limit and disallowed types are rejected before any URL is issued.
//	@Tags			attachments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path		int				true	"Ticket number"
//	@Param			body	body		PresignRequest	true	"Declared file metadata"
//	@Success		201		{object}	PresignResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON"
//	@Failure		401		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError	"Ticket not found in this org"
//	@Failure		422		{object}	httpx.APIError	"Oversize file, disallowed type or invalid filename"
//	@Router			/v1/tickets/{number}/attachments/presign [post]
func (h *Handler) Presign(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || number <= 0 {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_ticket_number", "ticket number is invalid"))
		return
	}
	var req PresignRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Presign(r.Context(), p.OrgID, number, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, res)
}

// Confirm verifies that the uploaded object landed and records its size.
//
//	@Summary		Confirm an attachment upload
//	@Description	Issues a HEAD request against the stored object, checks the size matches the declared size and moves the attachment to uploaded. A missing object or a size mismatch leaves the attachment pending. Confirming twice is idempotent.
//	@Tags			attachments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Attachment UUID"
//	@Success		200	{object}	AttachmentDTO
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Failure		409	{object}	httpx.APIError	"Uploaded object missing or attachment quarantined"
//	@Failure		422	{object}	httpx.APIError	"Uploaded size does not match"
//	@Router			/v1/attachments/{id}/confirm [post]
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	id, err := parseAttachmentID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	att, err := h.svc.Confirm(r.Context(), p.OrgID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, ToDTO(att))
}

// Download returns a short-lived signed URL for an uploaded attachment.
//
//	@Summary		Get an attachment download URL
//	@Description	Authorizes the attachment against the caller's org and returns a presigned GET URL valid for one to two minutes. Non-image files are served with Content-Disposition: attachment. Attachments in another org return 404.
//	@Tags			attachments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Attachment UUID"
//	@Success		200	{object}	DownloadResponse
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Failure		409	{object}	httpx.APIError	"Attachment not uploaded or quarantined"
//	@Router			/v1/attachments/{id}/download [get]
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}
	id, err := parseAttachmentID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Download(r.Context(), p.OrgID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, res)
}

func parseAttachmentID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid_attachment_id", "attachment id is invalid")
	}
	return id, nil
}
