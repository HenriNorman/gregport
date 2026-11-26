package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LoginGuard is the per-account backoff used in addition to the per-IP rate
// limiter. An attacker with many IPs still cannot try unlimited passwords
// against one account.
type LoginGuard interface {
	Allowed(ctx context.Context, account string) (bool, error)
	Failure(ctx context.Context, account string) error
	Reset(ctx context.Context, account string) error
}

type RedisLoginGuard struct {
	client    *redis.Client
	threshold int
	window    time.Duration
}

func NewRedisLoginGuard(client *redis.Client, threshold int, window time.Duration) *RedisLoginGuard {
	if threshold < 1 {
		threshold = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &RedisLoginGuard{client: client, threshold: threshold, window: window}
}

func loginGuardKey(account string) string {
	sum := sha256.Sum256([]byte(account))
	return "auth:login-fail:" + hex.EncodeToString(sum[:])
}

func (g *RedisLoginGuard) Allowed(ctx context.Context, account string) (bool, error) {
	n, err := g.client.Get(ctx, loginGuardKey(account)).Int()
	if err == redis.Nil {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("login guard: %w", err)
	}
	return n < g.threshold, nil
}

func (g *RedisLoginGuard) Failure(ctx context.Context, account string) error {
	key := loginGuardKey(account)
	pipe := g.client.TxPipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, g.window)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("login guard failure: %w", err)
	}
	return nil
}

func (g *RedisLoginGuard) Reset(ctx context.Context, account string) error {
	if err := g.client.Del(ctx, loginGuardKey(account)).Err(); err != nil {
		return fmt.Errorf("login guard reset: %w", err)
	}
	return nil
}
