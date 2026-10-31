package tickets

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// --- DTOs ---

type CreateTicketRequest struct {
	RequesterEmail string `json:"requester_email" example:"h@acme.com"`
	RequesterName  string `json:"requester_name"  example:"Henri"`
	Subject        string `json:"subject"         example:"Cannot log in"`
	Body           string `json:"body"            example:"I get an error when I try to log in."`
	Priority       string `json:"priority"        example:"normal" enums:"low,normal,high,urgent"`
}

type UpdateTicketRequest struct {
	Status   *string `json:"status,omitempty"   example:"open"   enums:"new,open,pending,solved,closed"`
	Priority *string `json:"priority,omitempty" example:"high"   enums:"low,normal,high,urgent"`
	Subject  *string `json:"subject,omitempty"  example:"Updated subject"`
}

type CreateCommentRequest struct {
	Body       string `json:"body"        example:"We're looking into this."`
	IsInternal bool   `json:"is_internal" example:"false"`
}

type RequesterDTO struct {
	Email string `json:"email" example:"h@acme.com"`
	Name  string `json:"name"  example:"Henri"`
}

type TicketDTO struct {
	Number    int64        `json:"number"    example:"1042"`
	ID        string       `json:"id"        example:"550e8400-e29b-41d4-a716-446655440000"`
	Subject   string       `json:"subject"   example:"Cannot log in"`
	Status    string       `json:"status"    example:"open"   enums:"new,open,pending,solved,closed"`
	Priority  string       `json:"priority"  example:"normal" enums:"low,normal,high,urgent"`
	Requester RequesterDTO `json:"requester"`
	Version   int          `json:"version"   example:"3"`
	CreatedAt time.Time    `json:"created_at" example:"2026-01-15T10:00:00Z"`
	UpdatedAt time.Time    `json:"updated_at" example:"2026-01-15T10:00:00Z"`
}

type CommentDTO struct {
	ID         string    `json:"id"          example:"550e8400-e29b-41d4-a716-446655440000"`
	Body       string    `json:"body"        example:"We're looking into this."`
	IsInternal bool      `json:"is_internal" example:"false"`
	AuthorType string    `json:"author_type" example:"user" enums:"user,customer,system"`
	CreatedAt  time.Time `json:"created_at"  example:"2026-01-15T10:00:00Z"`
}

type TicketListResponse struct {
	Tickets    []TicketDTO `json:"tickets"`
	NextCursor string      `json:"next_cursor,omitempty" example:"eyJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"`
}

// --- Teams ---

type CreateTeamRequest struct {
	Name string `json:"name" example:"Support"`
}

type UpdateTeamRequest struct {
	Name string `json:"name" example:"Support Tier 1"`
}

type TeamDTO struct {
	ID        string    `json:"id"         example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string    `json:"name"       example:"Support"`
	CreatedAt time.Time `json:"created_at" example:"2026-01-15T10:00:00Z"`
}

type TeamListResponse struct {
	Teams []TeamDTO `json:"teams"`
}

// --- Assignment ---

type AssignTicketRequest struct {
	AssigneeID *string `json:"assignee_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440000"`
	TeamID     *string `json:"team_id,omitempty"     example:"550e8400-e29b-41d4-a716-446655440000"`
	Auto       bool    `json:"auto,omitempty"        example:"false"`
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

func toTeamDTO(t Team) TeamDTO {
	return TeamDTO{
		ID:        t.ID.String(),
		Name:      t.Name,
		CreatedAt: t.CreatedAt,
	}
}

// --- Tags ---

type CreateTagRequest struct {
	Name string `json:"name" example:"billing"`
}

type TagDTO struct {
	ID   string `json:"id"   example:"550e8400-e29b-41d4-a716-446655440000"`
	Name string `json:"name" example:"billing"`
}

type TagListResponse struct {
	Tags []TagDTO `json:"tags"`
}

// --- Events ---

type TicketEventDTO struct {
	ID        string          `json:"id"         example:"550e8400-e29b-41d4-a716-446655440000"`
	EventType string          `json:"event_type" example:"ticket.status_changed"`
	ActorType string          `json:"actor_type" example:"user" enums:"user,customer,api_key,system"`
	ActorID   *string         `json:"actor_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440000"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at" example:"2026-01-15T10:00:00Z"`
}

type TicketEventListResponse struct {
	Events     []TicketEventDTO `json:"events"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// --- Merge ---

type MergeTicketRequest struct {
	IntoNumber int64 `json:"into_number" example:"1043"`
}

// --- Macros ---

type CreateMacroRequest struct {
	Name    string          `json:"name"    example:"Mark pending"`
	Actions MacroActionsDTO `json:"actions"`
}

type UpdateMacroRequest struct {
	Name    string          `json:"name"    example:"Mark pending"`
	Actions MacroActionsDTO `json:"actions"`
}

type MacroActionsDTO struct {
	SetStatus   string   `json:"set_status,omitempty"   example:"pending" enums:"new,open,pending,solved,closed"`
	SetPriority string   `json:"set_priority,omitempty" example:"high"    enums:"low,normal,high,urgent"`
	AddTags     []string `json:"add_tags,omitempty"     example:"billing,refund"`
	Comment     string   `json:"comment,omitempty"      example:"Waiting on the customer."`
}

type MacroDTO struct {
	ID        string          `json:"id"         example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string          `json:"name"       example:"Mark pending"`
	Actions   MacroActionsDTO `json:"actions"`
	CreatedAt time.Time       `json:"created_at" example:"2026-01-15T10:00:00Z"`
	UpdatedAt time.Time       `json:"updated_at" example:"2026-01-15T10:00:00Z"`
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

// --- Tickets ---

// Create creates a ticket and its first customer comment.
//
//	@Summary		Create a ticket and its first customer comment
//	@Description	Any member of the org may create a ticket. The requester is upserted by (org_id, requester_email). The ticket number is assigned per org inside the same transaction as the ticket insert, so a failed create leaves no gap in the sequence.
//	@Tags			tickets
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateTicketRequest	true	"Ticket payload"
//	@Success		201		{object}	TicketDTO
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError	"Missing, malformed, expired, or invalid token"
//	@Failure		422		{object}	httpx.APIError	"Validation failed"
//	@Router			/v1/tickets [post]
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

// List returns a page of tickets for the caller's org.
//
//	@Summary		List tickets (keyset pagination)
//	@Description	Any member of the org may list tickets. Results are scoped to the caller's org. Order is created_at DESC, id DESC; the cursor is opaque and stable under concurrent inserts.
//	@Tags			tickets
//	@Produce		json
//	@Security		BearerAuth
//	@Param			limit			query		int		false	"Page size (default 25, max 100)"
//	@Param			cursor			query		string	false	"Opaque cursor from a previous next_cursor"
//	@Param			status			query		string	false	"Filter by status"	Enums(new,open,pending,solved,closed)
//	@Param			priority		query		string	false	"Filter by priority"	Enums(low,normal,high,urgent)
//	@Param			assignee_id		query		string	false	"Filter by assignee UUID"
//	@Param			requester_id	query		string	false	"Filter by requester UUID"
//	@Success		200				{object}	TicketListResponse
//	@Failure		401				{object}	httpx.APIError
//	@Failure		422				{object}	httpx.APIError
//	@Router			/v1/tickets [get]
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

// Get fetches a ticket by its per-org number.
//
//	@Summary		Fetch a ticket by its per-org number
//	@Description	Any member of the org may read a ticket. A ticket in another org returns 404, never 403.
//	@Tags			tickets
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path		int	true	"Per-org ticket number (e.g. 1042)"
//	@Success		200		{object}	TicketDTO
//	@Header			200		{string}	ETag	"Quoted version, use as If-Match on PATCH"
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError	"Not found, or belongs to another org"
//	@Router			/v1/tickets/{number} [get]
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

// Update changes a ticket's status, priority, or subject.
//
//	@Summary		Update status, priority, or subject
//	@Description	Any member of the org may update a ticket. Status changes are checked against the state machine (new -> open -> pending -> solved -> closed, with solved -> open as reopen; closed is terminal). A stale If-Match returns 412; a missing one returns 428. A ticket in another org returns 404, never 403.
//	@Tags			tickets
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number		path		int					true	"Per-org ticket number"
//	@Param			If-Match	header		string				true	"The ticket's current ETag. Missing -> 428, stale -> 412."
//	@Param			body		body		UpdateTicketRequest	true	"Fields to update"
//	@Success		200			{object}	TicketDTO
//	@Header			200			{string}	ETag	"New quoted version"
//	@Failure		400			{object}	httpx.APIError	"Malformed If-Match, malformed JSON, or unknown field"
//	@Failure		401			{object}	httpx.APIError
//	@Failure		404			{object}	httpx.APIError	"Not found, or belongs to another org"
//	@Failure		412			{object}	httpx.APIError	"If-Match version is stale"
//	@Failure		422			{object}	httpx.APIError	"Invalid transition or invalid input"
//	@Failure		428			{object}	httpx.APIError	"If-Match header is missing"
//	@Router			/v1/tickets/{number} [patch]
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

// Delete soft deletes a ticket.
//
//	@Summary		Soft delete a ticket (admin and owner only)
//	@Description	Requires role owner or admin. Agents receive 403. A ticket in another org returns 404, never 403.
//	@Tags			tickets
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path	int	true	"Per-org ticket number"
//	@Success		204		"No Content"
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError	"Role is not owner or admin"
//	@Failure		404		{object}	httpx.APIError	"Not found, or belongs to another org"
//	@Router			/v1/tickets/{number} [delete]
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

// CreateComment adds a comment to a ticket.
//
//	@Summary		Add a comment to a ticket
//	@Description	Any member of the org may add a public comment. Setting is_internal=true (an agent-only internal note) requires role owner, admin, or agent. A ticket in another org returns 404, never 403.
//	@Tags			comments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path		int						true	"Per-org ticket number"
//	@Param			body	body		CreateCommentRequest	true	"Comment payload"
//	@Success		201		{object}	CommentDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError	"Internal note without an agent role or above"
//	@Failure		404		{object}	httpx.APIError	"Not found, or belongs to another org"
//	@Failure		422		{object}	httpx.APIError
//	@Router			/v1/tickets/{number}/comments [post]
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

// ListComments returns a ticket's comments.
//
//	@Summary		List comments on a ticket
//	@Description	Any member of the org may read comments. A ticket in another org returns 404, never 403. Internal notes are excluded unless include_internal=true.
//	@Tags			comments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number				path	int		true	"Per-org ticket number"
//	@Param			include_internal	query	boolean	false	"Include agent-only internal notes"
//	@Success		200					{object}	map[string][]CommentDTO
//	@Failure		400					{object}	httpx.APIError
//	@Failure		401					{object}	httpx.APIError
//	@Failure		404					{object}	httpx.APIError	"Not found, or belongs to another org"
//	@Router			/v1/tickets/{number}/comments [get]
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

// --- Teams ---

// CreateTeam creates a team in the caller's org.
//
//	@Summary		Create a team
//	@Description	Requires role owner or admin. Team names are unique per org.
//	@Tags			teams
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateTeamRequest	true	"Team payload"
//	@Success		201		{object}	TeamDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError	"Role is not owner or admin"
//	@Failure		409		{object}	httpx.APIError	"Team name already exists in this org"
//	@Failure		422		{object}	httpx.APIError
//	@Router			/v1/teams [post]
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

// ListTeams lists teams in the caller's org.
//
//	@Summary		List teams in the org
//	@Description	Any member of the org may list teams.
//	@Tags			teams
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	TeamListResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/teams [get]
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

// UpdateTeam renames a team.
//
//	@Summary		Rename a team
//	@Description	Requires role owner or admin.
//	@Tags			teams
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Team UUID"
//	@Param			body	body		UpdateTeamRequest	true	"Team payload"
//	@Success		200		{object}	TeamDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError
//	@Failure		409		{object}	httpx.APIError	"Team name already exists in this org"
//	@Failure		422		{object}	httpx.APIError
//	@Router			/v1/teams/{id} [patch]
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

// DeleteTeam deletes a team.
//
//	@Summary		Delete a team
//	@Description	Requires role owner or admin.
//	@Tags			teams
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Team UUID"
//	@Success		204	"No Content"
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		403	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/teams/{id} [delete]
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

// AddTeamMember adds a user to a team.
//
//	@Summary		Add a member to a team
//	@Description	Requires role owner or admin. The user must already be a member of this org; a user from another org returns 422, never success.
//	@Tags			teams
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path	string	true	"Team UUID"
//	@Param			userId	path	string	true	"User UUID"
//	@Success		204		"No Content"
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError	"Team not found"
//	@Failure		422		{object}	httpx.APIError	"User is not a member of this org"
//	@Router			/v1/teams/{id}/members/{userId} [put]
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

// RemoveTeamMember removes a user from a team.
//
//	@Summary		Remove a member from a team
//	@Description	Requires role owner or admin.
//	@Tags			teams
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path	string	true	"Team UUID"
//	@Param			userId	path	string	true	"User UUID"
//	@Success		204		"No Content"
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		403		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError
//	@Router			/v1/teams/{id}/members/{userId} [delete]
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

// Assign assigns a ticket to a user or a team.
//
//	@Summary		Assign a ticket to a user or a team
//	@Description	Any member of the org may assign a ticket. Body must contain exactly one of assignee_id or team_id. Round-robin (auto: true) is not yet available. The assignee must be an org member with role owner, admin, or agent. A ticket in another org returns 404, never 403.
//	@Tags			tickets
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path		int					true	"Per-org ticket number"
//	@Param			body	body		AssignTicketRequest	true	"Assignment payload"
//	@Success		200		{object}	TicketDTO
//	@Failure		400		{object}	httpx.APIError
//	@Failure		401		{object}	httpx.APIError
//	@Failure		404		{object}	httpx.APIError	"Ticket or team not found, or belongs to another org"
//	@Failure		422		{object}	httpx.APIError	"Invalid input, or assignee is not an agent-or-higher org member"
//	@Router			/v1/tickets/{number}/assignment [post]
func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
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

	var req AssignTicketRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	tk, err := h.svc.AssignTicket(r.Context(), p, number, req)
	if err != nil {
		httpx.WriteError(w, mapTicketError(err))
		return
	}

	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(tk.Version)))
	_ = httpx.WriteJSON(w, http.StatusOK, toTicketDTO(tk))
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

func parseUUIDParam(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
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
			Status:  http.StatusNotFound,
			Code:    "team_not_found",
			Message: "team not found",
		}
	case errors.Is(err, ErrTeamNameTaken):
		return httpx.Conflict("team_name_taken", "a team with that name already exists")
	case errors.Is(err, ErrMemberNotInOrg):
		return httpx.Unprocessable("member_not_in_org", "user is not a member of this organization")
	case errors.Is(err, ErrAssigneeNotAgent):
		return httpx.Unprocessable("assignee_not_agent", "assignee must be an org member with agent role or higher")
	default:
		return httpx.InternalError(err)
	}
}
