package dashboard

import (
	"context"

	"github.com/google/uuid"
)

// Service contains dashboard rules. It exists so the HTTP layer stays
// independent of PostgreSQL and the future cached implementation can be
// swapped in one place.
type Service struct {
	repo Store
}

// NewService creates a dashboard service.
func NewService(repo Store) *Service {
	return &Service{repo: repo}
}

// Get returns the current counts for orgID.
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (Counts, error) {
	return s.repo.Counts(ctx, orgID)
}
