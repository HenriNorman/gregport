package auth

import "github.com/HenriNorman/gregport/internal/platform/httpx"

// Domain sentinels carry their HTTP status, code and public message.
var (
	ErrEmailTaken         = httpx.Conflict("email_taken", "email is already registered")
	ErrSlugTaken          = httpx.Conflict("slug_taken", "organization slug is already taken")
	ErrInvalidInput       = httpx.Unprocessable("invalid_input", "org_name is required")
	ErrWeakPassword       = httpx.Unprocessable("weak_password", "password must be between 8 and 72 characters")
	ErrInvalidEmail       = httpx.Unprocessable("invalid_email", "email format is invalid")
	ErrEmptyName          = httpx.Unprocessable("empty_name", "name cannot be empty")
	ErrInvalidCredentials = httpx.Unauthorized("invalid_credentials", "invalid email or password")
	ErrOrgRequired        = httpx.Unprocessable("org_required", "org_slug is required when the user belongs to multiple organizations")
)
