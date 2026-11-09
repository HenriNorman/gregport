package customers

import (
	"context"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// Service erases customer data.
type Service struct {
	repo Store
}

// NewService wires the customer service.
func NewService(repo Store) *Service {
	return &Service{repo: repo}
}

// Erase replaces a customer's email with an org-scoped pseudonym and clears
// their name and external id. The customer row stays so ticket history remains
// intact.
func (s *Service) Erase(ctx context.Context, orgID, id uuid.UUID, actor requestctx.Principal) (Customer, error) {
	return s.repo.Erase(ctx, orgID, id, AnonymizedEmail(orgID, id), actor)
}
