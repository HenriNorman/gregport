package organizations

import (
	"context"
	"net/mail"
	"strings"
	"time"

	// Embed the IANA database so timezone validation also works in the
	// minimal Alpine runtime image.
	_ "time/tzdata"

	"github.com/google/uuid"
)

// Service contains organization settings rules.
type Service struct {
	repo Store
}

// NewService creates a settings service.
func NewService(repo Store) *Service {
	return &Service{repo: repo}
}

// Get returns the effective settings for an organization.
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (SettingsResponse, error) {
	settings, updatedAt, err := s.repo.GetSettings(ctx, orgID)
	if err != nil {
		return SettingsResponse{}, err
	}
	return SettingsResponse{Settings: normalize(settings), UpdatedAt: updatedAt}, nil
}

// Update applies a partial patch and persists the merged document.
func (s *Service) Update(ctx context.Context, orgID uuid.UUID, req UpdateSettingsRequest) (SettingsResponse, error) {
	current, _, err := s.repo.GetSettings(ctx, orgID)
	if err != nil {
		return SettingsResponse{}, err
	}
	current = normalize(current)

	if req.Timezone != nil {
		current.Timezone = strings.TrimSpace(*req.Timezone)
	}
	if req.DefaultPriority != nil {
		current.DefaultPriority = strings.ToLower(strings.TrimSpace(*req.DefaultPriority))
	}
	if req.SupportEmail != nil {
		current.SupportEmail = strings.ToLower(strings.TrimSpace(*req.SupportEmail))
	}

	if err := validate(current); err != nil {
		return SettingsResponse{}, ErrInvalidInput
	}

	saved, updatedAt, err := s.repo.UpdateSettings(ctx, orgID, current)
	if err != nil {
		return SettingsResponse{}, err
	}
	return SettingsResponse{Settings: normalize(saved), UpdatedAt: updatedAt}, nil
}

func normalize(settings Settings) Settings {
	if settings.Timezone == "" {
		settings.Timezone = "UTC"
	}
	if settings.DefaultPriority == "" {
		settings.DefaultPriority = "normal"
	}
	return settings
}

func validate(settings Settings) error {
	if settings.Timezone == "" || len(settings.Timezone) > 64 {
		return ErrInvalidInput
	}
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		return ErrInvalidInput
	}
	if !validPriority(settings.DefaultPriority) {
		return ErrInvalidInput
	}
	if len(settings.SupportEmail) > 254 {
		return ErrInvalidInput
	}
	if settings.SupportEmail != "" {
		addr, err := mail.ParseAddress(settings.SupportEmail)
		if err != nil || !strings.EqualFold(addr.Address, settings.SupportEmail) {
			return ErrInvalidInput
		}
	}
	return nil
}

func validPriority(priority string) bool {
	switch priority {
	case "low", "normal", "high", "urgent":
		return true
	default:
		return false
	}
}
