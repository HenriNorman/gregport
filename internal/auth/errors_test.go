package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

func TestDomainErrorsCarryHTTPMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    *httpx.APIError
		status int
		code   string
	}{
		{"email taken", ErrEmailTaken, http.StatusConflict, "email_taken"},
		{"slug taken", ErrSlugTaken, http.StatusConflict, "slug_taken"},
		{"invalid input", ErrInvalidInput, http.StatusUnprocessableEntity, "invalid_input"},
		{"weak password", ErrWeakPassword, http.StatusUnprocessableEntity, "weak_password"},
		{"invalid email", ErrInvalidEmail, http.StatusUnprocessableEntity, "invalid_email"},
		{"empty name", ErrEmptyName, http.StatusUnprocessableEntity, "empty_name"},
		{"invalid credentials", ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
		{"org required", ErrOrgRequired, http.StatusUnprocessableEntity, "org_required"},
		{"invalid token", ErrInvalidToken, http.StatusUnauthorized, "invalid_token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Status != tt.status {
				t.Errorf("status: got %d, want %d", tt.err.Status, tt.status)
			}
			if tt.err.Code != tt.code {
				t.Errorf("code: got %q, want %q", tt.err.Code, tt.code)
			}
			if tt.err.Message == "" {
				t.Error("message must not be empty")
			}

			wrapped := fmt.Errorf("service: %w", tt.err)
			if !errors.Is(wrapped, tt.err) {
				t.Error("errors.Is must still match wrapped domain errors")
			}
		})
	}
}
