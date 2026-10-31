package auth

import (
	"errors"
	"net"
	"net/http"

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

// --- DTOs ---

type SignupRequest struct {
	OrgName  string `json:"org_name" example:"Acme"`
	Name     string `json:"name"     example:"Henri"`
	Email    string `json:"email"    example:"h@acme.com"`
	Password string `json:"password" example:"correct-horse-battery"`
}

type UserDTO struct {
	ID    string `json:"id"    example:"550e8400-e29b-41d4-a716-446655440000"`
	Email string `json:"email" example:"h@acme.com"`
	Name  string `json:"name"  example:"Henri"`
}

type OrgDTO struct {
	ID   string `json:"id"   example:"550e8400-e29b-41d4-a716-446655440000"`
	Name string `json:"name" example:"Acme"`
	Slug string `json:"slug" example:"acme"`
}

type SignupResponse struct {
	User         UserDTO `json:"user"`
	Organization OrgDTO  `json:"organization"`
	Role         string  `json:"role" example:"owner" enums:"owner,admin,agent"`
}

type LoginRequest struct {
	Email    string `json:"email"              example:"h@acme.com"`
	Password string `json:"password"           example:"correct-horse-battery"`
	OrgSlug  string `json:"org_slug,omitempty" example:"acme"`
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"  example:"eyJhbGciOiJIUzI1NiIs..."`
	RefreshToken string    `json:"refresh_token" example:"YToOokzD0OzvHq8-Nl5zm8tqOFgK8woyEqQbDwgEBxY"`
	ExpiresIn    int       `json:"expires_in"    example:"900"`
	UserID       uuid.UUID `json:"user_id"       example:"550e8400-e29b-41d4-a716-446655440000"`
	OrgID        uuid.UUID `json:"org_id"        example:"550e8400-e29b-41d4-a716-446655440000"`
	Role         string    `json:"role"          example:"owner" enums:"owner,admin,agent"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" example:"YToOokzD0OzvHq8-Nl5zm8tqOFgK8woyEqQbDwgEBxY"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" example:"YToOokzD0OzvHq8-Nl5zm8tqOFgK8woyEqQbDwgEBxY"`
}

type MeResponse struct {
	UserID string `json:"user_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	OrgID  string `json:"org_id"  example:"550e8400-e29b-41d4-a716-446655440000"`
	Role   string `json:"role"    example:"owner" enums:"owner,admin,agent"`
}

// --- Handlers ---

// Signup creates a new organization, user, and owner membership atomically.
//
//	@Summary		Create an organization, user, and owner membership
//	@Description	Creates the three rows in a single transaction. Email is normalized (lowercased, trimmed). Passwords are bcrypt-hashed and never returned.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		SignupRequest	true	"Signup payload"
//	@Success		201		{object}	SignupResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		409		{object}	httpx.APIError	"Email or organization slug already taken"
//	@Failure		422		{object}	httpx.APIError	"Validation failed"
//	@Router			/v1/auth/signup [post]
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

// Login authenticates a user and returns an access token and a refresh token.
//
//	@Summary		Log in and receive an access token and a refresh token
//	@Description	Returns identical 401 responses for unknown email and wrong password. When the user belongs to multiple orgs, `org_slug` is required.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		LoginRequest	true	"Login payload"
//	@Success		200		{object}	TokenResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError	"Invalid credentials"
//	@Failure		422		{object}	httpx.APIError	"org_slug required for multi-org users"
//	@Router			/v1/auth/login [post]
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

// Refresh rotates a refresh token and issues a new token pair.
//
//	@Summary		Rotate the refresh token and issue a new token pair
//	@Description	Replaying a spent refresh token revokes the whole family — the correct response to probable theft. The legitimate user is logged out too.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		RefreshRequest	true	"Refresh token"
//	@Success		200		{object}	TokenResponse
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Failure		401		{object}	httpx.APIError	"Unknown, expired, or already-used refresh token"
//	@Router			/v1/auth/refresh [post]
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

// Logout revokes the refresh token's whole family.
//
//	@Summary		Revoke the refresh token's whole family
//	@Description	Idempotent — logging out an already-revoked token is a no-op, not an error.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body	LogoutRequest	true	"Refresh token"
//	@Success		204		"No Content"
//	@Failure		400		{object}	httpx.APIError	"Malformed JSON or unknown field"
//	@Router			/v1/auth/logout [post]
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req LogoutRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("invalid_json", err.Error()))
		return
	}

	_ = h.svc.Logout(r.Context(), req.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the authenticated principal.
//
//	@Summary		Return the authenticated principal
//	@Description	Reads `user_id`, `org_id`, and `role` from the verified access token. Proves the auth middleware works.
//	@Tags			me
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MeResponse
//	@Failure		401	{object}	httpx.APIError	"Missing, malformed, expired, or invalid token"
//	@Router			/v1/me [get]
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

// --- Error mapping ---

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
