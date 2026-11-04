package dashboard

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/cache"
)

// CachedStore decorates a Store with a short-lived read-through cache. Counts
// are cheap to recompute but read far more often than tickets change, so a
// miss simply falls back to PostgreSQL.
type CachedStore struct {
	inner Store
	cache cache.Cache
	ttl   time.Duration
}

// NewCachedStore wraps inner. A non-positive ttl disables caching.
func NewCachedStore(inner Store, c cache.Cache, ttl time.Duration) *CachedStore {
	return &CachedStore{inner: inner, cache: c, ttl: ttl}
}

// Counts returns cached counts when present, otherwise reads PostgreSQL and
// populates the cache.
func (s *CachedStore) Counts(ctx context.Context, orgID uuid.UUID) (Counts, error) {
	key, keyErr := cache.DashboardCountsKey(orgID)
	useCache := keyErr == nil && s.ttl > 0 && s.cache != nil

	if useCache {
		if raw, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			var counts Counts
			if err := json.Unmarshal(raw, &counts); err == nil {
				return counts, nil
			}
		}
	}

	counts, err := s.inner.Counts(ctx, orgID)
	if err != nil {
		return Counts{}, err
	}
	if useCache {
		if payload, err := json.Marshal(counts); err == nil {
			_ = s.cache.Set(ctx, key, payload, s.ttl)
		}
	}
	return counts, nil
}

// Invalidator drops the cached counts for one organization. Ticket mutations
// call it after the database commit so the next dashboard read recomputes.
type Invalidator struct {
	cache cache.Cache
}

// NewInvalidator creates a counts invalidator.
func NewInvalidator(c cache.Cache) *Invalidator {
	return &Invalidator{cache: c}
}

// InvalidateCounts removes the org's cached dashboard counts.
func (i *Invalidator) InvalidateCounts(ctx context.Context, orgID uuid.UUID) error {
	if i == nil || i.cache == nil {
		return nil
	}
	key, err := cache.DashboardCountsKey(orgID)
	if err != nil {
		return err
	}
	return i.cache.Delete(ctx, key)
}
