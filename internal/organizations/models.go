package organizations

import (
	"time"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Settings is the typed organization settings document stored in
// organizations.settings (jsonb). Unknown fields from older rows are ignored.
type Settings struct {
	Timezone        string `json:"timezone"         example:"UTC"`
	DefaultPriority string `json:"default_priority" example:"normal" enums:"low,normal,high,urgent"`
	SupportEmail    string `json:"support_email"    example:"support@acme.com"`
}

// SettingsResponse is returned by both settings endpoints.
type SettingsResponse struct {
	Settings  Settings  `json:"settings"`
	UpdatedAt time.Time `json:"updated_at" example:"2026-01-15T10:00:00Z"`
}

// UpdateSettingsRequest is a partial update. Omitted fields keep their value.
type UpdateSettingsRequest struct {
	Timezone        *string `json:"timezone,omitempty"         example:"Europe/Nairobi"`
	DefaultPriority *string `json:"default_priority,omitempty" example:"high" enums:"low,normal,high,urgent"`
	SupportEmail    *string `json:"support_email,omitempty"    example:"support@acme.com"`
}

var (
	ErrOrgNotFound  = httpx.NotFound("org_not_found", "organization not found")
	ErrInvalidInput = httpx.Unprocessable("invalid_settings", "organization settings are invalid")
)
