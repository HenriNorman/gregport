package sla

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

const (
	maxPolicyNameLength = 100
	// maxMinutes is one year. It keeps the future due-date arithmetic
	// comfortably inside a timestamp while still allowing long SLAs.
	maxMinutes = 365 * 24 * 60
)

// Service contains SLA policy rules.
type Service struct {
	repo Store
}

// NewService creates a policy service.
func NewService(repo Store) *Service {
	return &Service{repo: repo}
}

// List returns every policy for the org.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Policy, error) {
	return s.repo.List(ctx, orgID)
}

// Get returns one policy for the org.
func (s *Service) Get(ctx context.Context, orgID, policyID uuid.UUID) (Policy, error) {
	return s.repo.Get(ctx, orgID, policyID)
}

// Create validates and persists a new policy.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, req CreatePolicyRequest) (Policy, error) {
	policy := Policy{
		Name:                 req.Name,
		Priority:             req.Priority,
		FirstResponseMinutes: req.FirstResponseMinutes,
		ResolutionMinutes:    req.ResolutionMinutes,
	}
	normalize(&policy)
	if err := validate(policy); err != nil {
		return Policy{}, err
	}
	return s.repo.Create(ctx, orgID, policy)
}

// Update applies a partial patch to an existing policy.
func (s *Service) Update(ctx context.Context, orgID, policyID uuid.UUID, req UpdatePolicyRequest) (Policy, error) {
	policy, err := s.repo.Get(ctx, orgID, policyID)
	if err != nil {
		return Policy{}, err
	}

	if req.Name != nil {
		policy.Name = *req.Name
	}
	if req.Priority != nil {
		policy.Priority = *req.Priority
	}
	if req.FirstResponseMinutes != nil {
		policy.FirstResponseMinutes = *req.FirstResponseMinutes
	}
	if req.ResolutionMinutes != nil {
		policy.ResolutionMinutes = *req.ResolutionMinutes
	}

	normalize(&policy)
	if err := validate(policy); err != nil {
		return Policy{}, err
	}
	return s.repo.Update(ctx, orgID, policy)
}

// Delete removes a policy from the org.
func (s *Service) Delete(ctx context.Context, orgID, policyID uuid.UUID) error {
	return s.repo.Delete(ctx, orgID, policyID)
}

func normalize(policy *Policy) {
	policy.Name = strings.TrimSpace(policy.Name)
	policy.Priority = strings.ToLower(strings.TrimSpace(policy.Priority))
}

func validate(policy Policy) error {
	if policy.Name == "" || len(policy.Name) > maxPolicyNameLength {
		return ErrInvalidInput
	}
	if !ValidPriority(policy.Priority) {
		return ErrInvalidInput
	}
	if policy.FirstResponseMinutes <= 0 || policy.FirstResponseMinutes > maxMinutes {
		return ErrInvalidInput
	}
	if policy.ResolutionMinutes <= 0 || policy.ResolutionMinutes > maxMinutes {
		return ErrInvalidInput
	}
	if policy.ResolutionMinutes < policy.FirstResponseMinutes {
		return ErrInvalidInput
	}
	return nil
}
