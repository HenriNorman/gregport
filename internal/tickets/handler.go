package tickets

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"encoding/json"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// --- DTOs ---

type CreateTicketRequest struct {
	RequesterEmail string `json:"requester_email"`
	RequesterName  string `json:"requester_name"`
	Subject        string `json:"subject"`
	Body           string `json:"body"`
	Priority       string `json:"priority"`
}

type UpdateTicketRequest struct {
	Status   *string `json:"status,omitempty"`
	Priority *string `json:"priority,omitempty"`
	Subject  *string `json:"subject,omitempty"`
}

type CreateCommentRequest struct {
	Body       string `json:"body"`
	IsInternal bool   `json:"is_internal"`
}

type RequesterDTO struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type TicketDTO struct {
	Number    int64        `json:"number"`
	ID        string       `json:"id"`
	Subject   string       `json:"subject"`
	Status    string       `json:"status"`
	Priority  string       `json:"priority"`
	Requester RequesterDTO `json:"requester"`
	Version   int          `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type CommentDTO struct {
	ID         string    `json:"id"`
	Body       string    `json:"body"`
	IsInternal bool      `json:"is_internal"`
	AuthorType string    `json:"author_type"`
	CreatedAt  time.Time `json:"created_at"`
}

type TicketListResponse struct {
	Tickets    []TicketDTO `json:"tickets"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// --- Teams ---

type CreateTeamRequest struct {
	Name string `json:"name"`
}

type UpdateTeamRequest struct {
	Name string `json:"name"`
}

type TeamDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type TeamListResponse struct {
	Teams []TeamDTO `json:"teams"`
}

// --- Assignment ---

type AssignTicketRequest struct {
	AssigneeID *string `json:"assignee_id,omitempty"`
	TeamID     *string `json:"team_id,omitempty"`
	Auto       bool    `json:"auto,omitempty"`
}

// --- Tags ---

type CreateTagRequest struct {
	Name string `json:"name"`
}

type TagDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TagListResponse struct {
	Tags []TagDTO `json:"tags"`
}

// --- Events ---

type TicketEventDTO struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	ActorType string          `json:"actor_type"`
	ActorID   *string         `json:"actor_id,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

type TicketEventListResponse struct {
	Events     []TicketEventDTO `json:"events"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// --- Merge ---

type MergeTicketRequest struct {
	IntoNumber int64 `json:"into_number"`
}

// --- Macros ---

type CreateMacroRequest struct {
	Name    string          `json:"name"`
	Actions MacroActionsDTO `json:"actions"`
}

type UpdateMacroRequest struct {
	Name    string          `json:"name"`
	Actions MacroActionsDTO `json:"actions"`
}

type MacroActionsDTO struct {
	SetStatus   string   `json:"set_status,omitempty"`
	SetPriority string   `json:"set_priority,omitempty"`
	AddTags     []string `json:"add_tags,omitempty"`
	Comment     string   `json:"comment,omitempty"`
}

type MacroDTO struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Actions   MacroActionsDTO `json:"actions"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type MacroListResponse struct {
	Macros []MacroDTO `json:"macros"`
}

// --- Handler ---

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	var req CreateTicketRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	tk, err := h.svc.CreateTicket(r.Context(), p, req)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	_ = httpx.WriteJSON(w, http.StatusCreated, toTicketDTO(tk))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	f := ListFilter{
		Limit:  parseLimit(r.URL.Query().Get("limit")),
		Cursor: r.URL.Query().Get("cursor"),
	}
	if s := r.URL.Query().Get("status"); s != "" {
		f.Status = &s
	}
	if s := r.URL.Query().Get("priority"); s != "" {
		f.Priority = &s
	}
	if s := r.URL.Query().Get("assignee_id"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			f.AssigneeID = &id
		}
	}
	if s := r.URL.Query().Get("requester_id"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			f.RequesterID = &id
		}
	}

	res, err := h.svc.ListTickets(r.Context(), p, f)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	dtos := make([]TicketDTO, 0, len(res.Tickets))
	for _, tk := range res.Tickets {
		dtos = append(dtos, toTicketDTO(tk))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, TicketListResponse{
		Tickets:    dtos,
		NextCursor: res.NextCursor,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	number, err := parseNumber(r)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	tk, err := h.svc.GetTicket(r.Context(), p, number)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(tk.Version)))
	_ = httpx.WriteJSON(w, http.StatusOK, toTicketDTO(tk))
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	number, err := parseNumber(r)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	expectedVersion, err := parseIfMatch(r)
	if err != nil {
		if errors.Is(err, ErrPreconditionRequired) {
			httpx.WriteError(w, &httpx.APIError{
				Status:  http.StatusPreconditionRequired,
				Code:    "precondition_required",
				Message: "If-Match header is required",
			})
			return
		}
		httpx.WriteError(w, httpx.BadRequest("invalid_if_match", "If-Match must be a quoted integer version"))
		return
	}

	var req UpdateTicketRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	tk, err := h.svc.UpdateTicket(r.Context(), p, number, expectedVersion, req)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(tk.Version)))
	_ = httpx.WriteJSON(w, http.StatusOK, toTicketDTO(tk))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	number, err := parseNumber(r)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	if err := h.svc.DeleteTicket(r.Context(), p, number); err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CreateComment(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	number, err := parseNumber(r)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	var req CreateCommentRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	c, err := h.svc.CreateComment(r.Context(), p, number, req)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	_ = httpx.WriteJSON(w, http.StatusCreated, toCommentDTO(c))
}

func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}

	number, err := parseNumber(r)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	includeInternal := r.URL.Query().Get("include_internal") == "true"

	cs, err := h.svc.ListComments(r.Context(), p, number, includeInternal)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	dtos := make([]CommentDTO, 0, len(cs))
	for _, c := range cs {
		dtos = append(dtos, toCommentDTO(c))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{"comments": dtos})
}

// --- helpers ---

func parseNumber(r *http.Request) (int64, error) {
	s := chi.URLParam(r, "number")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("bad number")
	}
	return n, nil
}

func parseLimit(s string) int {
	if s == "" {
		return 25
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 25
	}
	if n > 100 {
		return 100
	}
	return n
}

func parseIfMatch(r *http.Request) (int, error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return 0, ErrPreconditionRequired
	}
	unquoted, err := strconv.Unquote(h)
	if err != nil {
		return 0, errors.New("not quoted")
	}
	n, err := strconv.Atoi(unquoted)
	if err != nil || n < 1 {
		return 0, errors.New("not an integer")
	}
	return n, nil
}

func toTicketDTO(t Ticket) TicketDTO {
	return TicketDTO{
		Number:   t.Number,
		ID:       t.ID.String(),
		Subject:  t.Subject,
		Status:   t.Status,
		Priority: t.Priority,
		Requester: RequesterDTO{
			Email: t.RequesterEmail,
			Name:  t.RequesterName,
		},
		Version:   t.Version,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
}

func toCommentDTO(c Comment) CommentDTO {
	return CommentDTO{
		ID:         c.ID.String(),
		Body:       c.Body,
		IsInternal: c.IsInternal,
		AuthorType: c.AuthorType,
		CreatedAt:  c.CreatedAt,
	}
}

func unauthorized() *httpx.APIError {
	return &httpx.APIError{
		Status:  http.StatusUnauthorized,
		Code:    "unauthorized",
		Message: "authentication required",
	}
}

func mapTicketError(err error) *httpx.APIError {
	switch {
	case errors.Is(err, ErrTicketNotFound):
		return &httpx.APIError{
			Status:  http.StatusNotFound,
			Code:    "not_found",
			Message: "ticket not found",
		}
	case errors.Is(err, ErrVersionMismatch):
		return &httpx.APIError{
			Status:  http.StatusPreconditionFailed,
			Code:    "precondition_failed",
			Message: "ticket was modified; re-fetch and retry with the new ETag",
		}
	case errors.Is(err, ErrInvalidTransition):
		return httpx.Unprocessable("invalid_transition", "that status change is not allowed")
	case errors.Is(err, ErrInvalidInput):
		return httpx.Unprocessable("invalid_input", "invalid input")
	case errors.Is(err, ErrForbidden):
		return &httpx.APIError{
			Status:  http.StatusForbidden,
			Code:    "forbidden",
			Message: "insufficient permissions",
		}
	case errors.Is(err, ErrTeamNotFound):
		return &httpx.APIError{
			Status: http.StatusNotFound, Code: "team_not_found",
			Message: "team not found",
		}
	case errors.Is(err, ErrTeamNameTaken):
		return httpx.Conflict("team_name_taken", "a team with that name already exists")
	case errors.Is(err, ErrMemberNotInOrg):
		return httpx.Unprocessable("member_not_in_org", "user is not a member of this organization")
	default:
		return httpx.InternalError(err)
	}
}

func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	var req CreateTeamRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}
	t, err := h.svc.CreateTeam(r.Context(), p, req.Name)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, toTeamDTO(t))
}

func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	teams, err := h.svc.ListTeams(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	dtos := make([]TeamDTO, 0, len(teams))
	for _, t := range teams {
		dtos = append(dtos, toTeamDTO(t))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, TeamListResponse{Teams: dtos})
}

func (h *Handler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	teamID, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_id", "team id must be a UUID"))
		return
	}
	var req UpdateTeamRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}
	t, err := h.svc.UpdateTeam(r.Context(), p, teamID, req.Name)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toTeamDTO(t))
}

func (h *Handler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	teamID, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_id", "team id must be a UUID"))
		return
	}
	if err := h.svc.DeleteTeam(r.Context(), p, teamID); err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	teamID, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_id", "team id must be a UUID"))
		return
	}
	userID, err := parseUUIDParam(r, "userId")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_user_id", "user id must be a UUID"))
		return
	}
	if err := h.svc.AddTeamMember(r.Context(), p, teamID, userID); err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, unauthorized())
		return
	}
	teamID, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_id", "team id must be a UUID"))
		return
	}
	userID, err := parseUUIDParam(r, "userId")
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_user_id", "user id must be a UUID"))
		return
	}
	if err := h.svc.RemoveTeamMember(r.Context(), p, teamID, userID); err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseUUIDParam(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}

func toTeamDTO(t Team) TeamDTO {
	return TeamDTO{
		ID:        t.ID.String(),
		Name:      t.Name,
		CreatedAt: t.CreatedAt,
	}
}
