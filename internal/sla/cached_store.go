package sla

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/cache"
)

// CachedStore decorates a Store with read-through caching for the policy list
// and individual policies. Cache failures fall back to PostgreSQL, and writes
// invalidate the affected keys only after the database write succeeds.
type CachedStore struct {
	inner Store
	cache cache.Cache
	ttl   time.Duration
}

// NewCachedStore wraps inner. A non-positive ttl disables caching.
func NewCachedStore(inner Store, c cache.Cache, ttl time.Duration) *CachedStore {
	return &CachedStore{inner: inner, cache: c, ttl: ttl}
}

// List returns the org's policies, using the list cache when warm.
func (s *CachedStore) List(ctx context.Context, orgID uuid.UUID) ([]Policy, error) {
	key, keyErr := cache.SLAPoliciesKey(orgID)
	useCache := keyErr == nil && s.ttl > 0 && s.cache != nil

	if useCache {
		if raw, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			var policies []Policy
			if err := json.Unmarshal(raw, &policies); err == nil {
				return policies, nil
			}
		}
	}

	policies, err := s.inner.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if useCache {
		if payload, err := json.Marshal(policies); err == nil {
			_ = s.cache.Set(ctx, key, payload, s.ttl)
		}
	}
	return policies, nil
}

// Get returns one policy, using the item cache when warm.
func (s *CachedStore) Get(ctx context.Context, orgID, policyID uuid.UUID) (Policy, error) {
	key, keyErr := cache.SLAPolicyKey(orgID, policyID)
	useCache := keyErr == nil && s.ttl > 0 && s.cache != nil

	if useCache {
		if raw, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			var policy Policy
			if err := json.Unmarshal(raw, &policy); err == nil {
				return policy, nil
			}
		}
	}

	policy, err := s.inner.Get(ctx, orgID, policyID)
	if err != nil {
		return Policy{}, err
	}
	if useCache {
		if payload, err := json.Marshal(policy); err == nil {
			_ = s.cache.Set(ctx, key, payload, s.ttl)
		}
	}
	return policy, nil
}

// Create writes a new policy and invalidates the list cache.
func (s *CachedStore) Create(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	created, err := s.inner.Create(ctx, orgID, policy)
	if err != nil {
		return Policy{}, err
	}
	if key, keyErr := cache.SLAPoliciesKey(orgID); keyErr == nil && s.cache != nil {
		_ = s.cache.Delete(ctx, key)
	}
	return created, nil
}

// Update writes the policy and invalidates both the item and list caches.
func (s *CachedStore) Update(ctx context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	updated, err := s.inner.Update(ctx, orgID, policy)
	if err != nil {
		return Policy{}, err
	}
	s.invalidate(ctx, orgID, policy.ID)
	return updated, nil
}

// Delete removes the policy and invalidates both caches.
func (s *CachedStore) Delete(ctx context.Context, orgID, policyID uuid.UUID) error {
	if err := s.inner.Delete(ctx, orgID, policyID); err != nil {
		return err
	}
	s.invalidate(ctx, orgID, policyID)
	return nil
}

func (s *CachedStore) invalidate(ctx context.Context, orgID, policyID uuid.UUID) {
	if s.cache == nil {
		return
	}
	if key, err := cache.SLAPoliciesKey(orgID); err == nil {
		_ = s.cache.Delete(ctx, key)
	}
	if key, err := cache.SLAPolicyKey(orgID, policyID); err == nil {
		_ = s.cache.Delete(ctx, key)
	}
}
