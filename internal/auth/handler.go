package auth

import (
	"errors"
	"net/http"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// --- DTOs ---

type SignupRequest struct {
	OrgName  string `json:"org_name"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type OrgDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type SignupResponse struct {
	User         UserDTO `json:"user"`
	Organization OrgDTO  `json:"organization"`
	Role         string  `json:"role"`
}

// --- Handler ---

func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	var req SignupRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	org, user, member, err := h.svc.Signup(r.Context(), SignupInput{
		OrgName:  req.OrgName,
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httpx.WriteError(w, mapSignupError(err))
		return
	}

	resp := SignupResponse{
		User:         UserDTO{ID: user.ID.String(), Email: user.Email, Name: user.Name},
		Organization: OrgDTO{ID: org.ID.String(), Name: org.Name, Slug: org.Slug},
		Role:         member.Role,
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, resp)
}

func mapSignupError(err error) *httpx.APIError {
	switch {
	case errors.Is(err, ErrInvalidEmail):
		return httpx.Unprocessable("invalid_email", "email format is invalid")
	case errors.Is(err, ErrWeakPassword):
		return httpx.Unprocessable("weak_password", "password must be between 8 and 72 characters")
	case errors.Is(err, ErrEmptyName):
		return httpx.Unprocessable("empty_name", "name cannot be empty")
	case errors.Is(err, ErrInvalidInput):
		return httpx.Unprocessable("invalid_input", "org_name is required")
	case errors.Is(err, ErrEmailTaken):
		return httpx.Conflict("email_taken", "email is already registered")
	case errors.Is(err, ErrSlugTaken):
		return httpx.Conflict("slug_taken", "organization slug is already taken")
	default:
		return httpx.InternalError(err)
	}
}
