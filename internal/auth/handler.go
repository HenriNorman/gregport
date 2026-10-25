package auth

import (
	"errors"
	"net/http"
	"net"
	
	"github.com/google/uuid"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}


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

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	OrgSlug  string `json:"org_slug,omitempty"`
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`   // seconds
	UserID       uuid.UUID `json:"user_id"`
	OrgID        uuid.UUID `json:"org_id"`
	Role         string    `json:"role"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type MeResponse struct {
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`
	Role   string `json:"role"`
}

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

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	pair, err := h.svc.Login(r.Context(), LoginInput{
		Email:     req.Email,
		Password:  req.Password,
		OrgSlug:   req.OrgSlug,
		UserAgent: r.UserAgent(),
		IP:        clientIP(r),
	})
	if err != nil {
		httpx.WriteError(w, mapLoginError(err))
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, TokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int(pair.ExpiresIn.Seconds()),
		UserID:       pair.Principal.UserID,
		OrgID:        pair.Principal.OrgID,
		Role:         pair.Principal.Role,
	})
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken, r.UserAgent(), clientIP(r))
	if err != nil {
		httpx.WriteError(w, mapLoginError(err))
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, TokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int(pair.ExpiresIn.Seconds()),
		UserID:       pair.Principal.UserID,
		OrgID:        pair.Principal.OrgID,
		Role:         pair.Principal.Role,
	})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req LogoutRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	_ = h.svc.Logout(r.Context(), req.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, &httpx.APIError{
			Status: http.StatusUnauthorized, Code: "unauthorized", Message: "authentication required",
		})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, MeResponse{
		UserID: p.UserID.String(),
		OrgID:  p.OrgID.String(),
		Role:   p.Role,
	})
}

func mapLoginError(err error) *httpx.APIError {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return &httpx.APIError{
			Status: http.StatusUnauthorized, Code: "invalid_credentials",
			Message: "invalid email or password",
		}
	case errors.Is(err, ErrOrgRequired):
		return httpx.Unprocessable("org_required", "org_slug is required when the user belongs to multiple organizations")
	case errors.Is(err, ErrInvalidToken):
		return &httpx.APIError{
			Status: http.StatusUnauthorized, Code: "invalid_token",
			Message: "invalid or expired token",
		}
	default:
		return httpx.InternalError(err)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
