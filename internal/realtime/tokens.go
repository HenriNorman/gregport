package realtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// ErrInvalidToken is returned for a missing, expired, or already-used stream
// token. The handler maps it to 401.
var ErrInvalidToken = errors.New("realtime: invalid stream token")

const streamTokenPrefix = "realtime:stream-token:"

// TokenStore issues short-lived single-use credentials for EventSource, which
// cannot send an Authorization header.
type TokenStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewTokenStore(client *redis.Client, ttl time.Duration) *TokenStore {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &TokenStore{client: client, ttl: ttl}
}

// Issue stores the principal under a random token. The token is bound to the
// principal's org and cannot be reused after Consume.
func (s *TokenStore) Issue(ctx context.Context, p requestctx.Principal) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal stream principal: %w", err)
	}

	expiresAt := time.Now().Add(s.ttl).UTC()
	if err := s.client.Set(ctx, streamTokenPrefix+token, payload, s.ttl).Err(); err != nil {
		return "", time.Time{}, fmt.Errorf("store stream token: %w", err)
	}
	return token, expiresAt, nil
}

// Consume atomically reads and deletes the token, so a leaked URL cannot be
// replayed even inside the TTL window.
func (s *TokenStore) Consume(ctx context.Context, token string) (requestctx.Principal, error) {
	if token == "" {
		return requestctx.Principal{}, ErrInvalidToken
	}

	raw, err := s.client.GetDel(ctx, streamTokenPrefix+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return requestctx.Principal{}, ErrInvalidToken
	}
	if err != nil {
		return requestctx.Principal{}, fmt.Errorf("consume stream token: %w", err)
	}

	var p requestctx.Principal
	if err := json.Unmarshal(raw, &p); err != nil {
		return requestctx.Principal{}, fmt.Errorf("decode stream principal: %w", err)
	}
	if p.OrgID == uuid.Nil {
		return requestctx.Principal{}, ErrInvalidToken
	}
	return p, nil
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate stream token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
