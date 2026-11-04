package organizations

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/cache"
)

// settingsCacheEntry is the serialized form stored in Redis. It keeps the
// repository contract intact: callers get both the document and its
// updated_at timestamp.
type settingsCacheEntry struct {
	Settings  Settings  `json:"settings"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CachedStore decorates a Store with read-through caching. Cache failures are
// never fatal: a get falls back to PostgreSQL and writes/deletes are
// best-effort. Only successful reads are cached, so a missing organization is
// never memoized.
type CachedStore struct {
	inner Store
	cache cache.Cache
	ttl   time.Duration
}

// NewCachedStore wraps inner. A non-positive ttl disables caching and turns
// the decorator into a pass-through.
func NewCachedStore(inner Store, c cache.Cache, ttl time.Duration) *CachedStore {
	return &CachedStore{inner: inner, cache: c, ttl: ttl}
}

// GetSettings returns cached settings when present, otherwise reads the
// database and populates the cache.
func (s *CachedStore) GetSettings(ctx context.Context, orgID uuid.UUID) (Settings, time.Time, error) {
	key, keyErr := cache.OrgSettingsKey(orgID)
	useCache := keyErr == nil && s.ttl > 0 && s.cache != nil

	if useCache {
		if raw, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			var entry settingsCacheEntry
			if err := json.Unmarshal(raw, &entry); err == nil {
				return entry.Settings, entry.UpdatedAt, nil
			}
			// Corrupt entries are treated as a miss and overwritten below.
		}
	}

	settings, updatedAt, err := s.inner.GetSettings(ctx, orgID)
	if err != nil {
		return Settings{}, time.Time{}, err
	}

	if useCache {
		payload, err := json.Marshal(settingsCacheEntry{Settings: settings, UpdatedAt: updatedAt})
		if err == nil {
			_ = s.cache.Set(ctx, key, payload, s.ttl)
		}
	}
	return settings, updatedAt, nil
}

// UpdateSettings writes through to the database and invalidates the cached
// document. Invalidation happens only after a successful write.
func (s *CachedStore) UpdateSettings(ctx context.Context, orgID uuid.UUID, settings Settings) (Settings, time.Time, error) {
	saved, updatedAt, err := s.inner.UpdateSettings(ctx, orgID, settings)
	if err != nil {
		return Settings{}, time.Time{}, err
	}

	if key, keyErr := cache.OrgSettingsKey(orgID); keyErr == nil && s.cache != nil {
		_ = s.cache.Delete(ctx, key)
	}
	return saved, updatedAt, nil
}
