package tickets

import (
	"context"
	"crypto/subtle"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// AuthenticateAPIKey validates a raw gp_live_ key and returns the principal.
// Revoked and expired keys return ErrAPIKeyNotFound (401 at the handler).
func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string) (requestctx.Principal, error) {
	prefix, ok := parseAPIKeyPrefix(raw)
	if !ok {
		return requestctx.Principal{}, ErrAPIKeyNotFound
	}

	k, err := s.repo.FindAPIKeyByPrefix(ctx, prefix)
	if err != nil {
		return requestctx.Principal{}, err
	}
	if k.RevokedAt != nil {
		return requestctx.Principal{}, ErrAPIKeyNotFound
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return requestctx.Principal{}, ErrAPIKeyNotFound
	}

	presentedHash := HashAPIKey(raw)
	if subtle.ConstantTimeCompare([]byte(presentedHash), []byte(k.KeyHash)) != 1 {
		return requestctx.Principal{}, ErrAPIKeyNotFound
	}

	s.lastUsed.mark(ctx, k.OrgID, k.ID)

	return requestctx.Principal{
		Kind:    requestctx.KindAPIKey,
		ActorID: k.ID,
		OrgID:   k.OrgID,
		Role:    "agent",
	}, nil
}

// parseAPIKeyPrefix splits gp_live_<id>_<secret> and returns gp_live_<id>.
func parseAPIKeyPrefix(raw string) (string, bool) {
	const p = "gp_live_"
	if len(raw) <= len(p) {
		return "", false
	}
	if raw[:len(p)] != p {
		return "", false
	}
	// find the underscore after the id
	rest := raw[len(p):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '_' {
			return p + rest[:i], true
		}
	}
	return "", false
}

// LastUsedThrottle updates api_keys.last_used_at at most once per window per
// key. When a Redis client is configured, the throttle is coordinated across
// processes with an atomic Redis SET NX so concurrent API instances do not
// hammer the database. Without Redis it falls back to a per-process in-memory
// throttle.
type LastUsedThrottle struct {
	mu       sync.Mutex
	lastSeen map[uuid.UUID]time.Time
	updateFn func(ctx context.Context, keyID uuid.UUID) error
	window   time.Duration
	// client is optional. When set, Redis gates the database update across
	// processes. When nil, only the in-memory fallback is used.
	client *redis.Client
}

// NewLastUsedThrottle creates a throttle. The update function writes
// last_used_at to the database and is invoked at most once per window per key.
func NewLastUsedThrottle(updateFn func(ctx context.Context, keyID uuid.UUID) error) *LastUsedThrottle {
	return &LastUsedThrottle{
		lastSeen: make(map[uuid.UUID]time.Time),
		updateFn: updateFn,
		window:   time.Minute,
	}
}

// SetRedisClient wires a Redis client for cross-process throttling. It is safe
// to call before any marks. A nil client disables Redis coordination.
func (t *LastUsedThrottle) SetRedisClient(client *redis.Client) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.client = client
}

// mark records that keyID was used. At most once per window per (org, key) the
// database update function is invoked, in the background so authentication is
// not blocked.
func (t *LastUsedThrottle) mark(ctx context.Context, orgID, keyID uuid.UUID) {
	key, err := cache.APIKeyLastUsedKey(orgID, keyID)
	if err != nil {
		return
	}

	t.mu.Lock()
	client := t.client
	t.mu.Unlock()

	if client != nil {
		t.markRedis(ctx, client, key, keyID)
		return
	}

	// In-memory fallback: per-process throttle.
	t.mu.Lock()
	last, ok := t.lastSeen[keyID]
	if ok && time.Since(last) < t.window {
		t.mu.Unlock()
		return
	}
	t.lastSeen[keyID] = time.Now()
	t.mu.Unlock()

	go t.update(keyID)
}

func (t *LastUsedThrottle) markRedis(ctx context.Context, client *redis.Client, key string, keyID uuid.UUID) {
	// SET NX EX window: only the first request in the window wins the gate and
	// performs the database update. The key expires at the window, allowing the
	// next window's first request to update again.
	created, err := client.SetNX(ctx, key, "1", t.window).Result()
	if err != nil {
		// Redis is down: fail open. Authentication continues, but last_used_at
		// is not refreshed so the database is not hammered during an outage.
		logging.FromContext(ctx).Warn("api key last-used throttle unavailable", "error", err)
		return
	}
	if !created {
		return
	}

	go t.update(keyID)
}

func (t *LastUsedThrottle) update(keyID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.updateFn(ctx, keyID)
}
