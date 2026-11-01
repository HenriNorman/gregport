package tickets

import (
	"context"
	"crypto/subtle"
	"sync"
	"time"

	"github.com/google/uuid"

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

	s.lastUsed.mark(k.ID)

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

// LastUsedThrottle updates api_keys.last_used_at at most once per minute per key.
type LastUsedThrottle struct {
	mu       sync.Mutex
	lastSeen map[uuid.UUID]time.Time
	updateFn func(ctx context.Context, keyID uuid.UUID) error
	window   time.Duration
}

func NewLastUsedThrottle(updateFn func(ctx context.Context, keyID uuid.UUID) error) *LastUsedThrottle {
	return &LastUsedThrottle{
		lastSeen: make(map[uuid.UUID]time.Time),
		updateFn: updateFn,
		window:   time.Minute,
	}
}

func (t *LastUsedThrottle) mark(keyID uuid.UUID) {
	t.mu.Lock()
	last, ok := t.lastSeen[keyID]
	if ok && time.Since(last) < t.window {
		t.mu.Unlock()
		return
	}
	t.lastSeen[keyID] = time.Now()
	t.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = t.updateFn(ctx, keyID)
	}()
}
