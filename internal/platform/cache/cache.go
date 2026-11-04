// Package cache provides the shared Redis-backed cache used by read-heavy
// services. Consumers depend on small interfaces so tests can substitute an
// in-memory implementation.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidTTL is returned when a cache write does not have a positive TTL.
var ErrInvalidTTL = errors.New("cache: ttl must be positive")

// Cache is the minimal contract consumers need for read-through caching.
// A miss is reported as ok=false with a nil error.
type Cache interface {
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Pinger is implemented by caches that expose a health check.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Redis is the production Cache implementation.
type Redis struct {
	client  *redis.Client
	timeout time.Duration
}

// RedisOptions configures the Redis client. Zero timeouts fall back to the
// defaults below so a missing environment value cannot disable deadlines.
type RedisOptions struct {
	Addr         string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	Timeout      time.Duration
}

const (
	defaultDialTimeout  = time.Second
	defaultReadTimeout  = 500 * time.Millisecond
	defaultWriteTimeout = 500 * time.Millisecond
	defaultTimeout      = 200 * time.Millisecond
)

// NewRedis creates a Redis-backed cache. It does not connect until the first
// command, so startup is not blocked by an unavailable cache.
func NewRedis(opts RedisOptions) *Redis {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}

	return &Redis{
		client: redis.NewClient(&redis.Options{
			Addr:         opts.Addr,
			Password:     opts.Password,
			DB:           opts.DB,
			DialTimeout:  opts.DialTimeout,
			ReadTimeout:  opts.ReadTimeout,
			WriteTimeout: opts.WriteTimeout,
		}),
		timeout: opts.Timeout,
	}
}

// Get returns the cached value. A missing key is (nil, false, nil).
func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	value, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get: %w", err)
	}
	return value, true, nil
}

// Set stores a value with a mandatory positive TTL.
func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return ErrInvalidTTL
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

// Delete removes a key. Deleting a missing key is not an error.
func (r *Redis) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// Ping checks Redis reachability.
func (r *Redis) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache ping: %w", err)
	}
	return nil
}

// Close releases the Redis client.
func (r *Redis) Close() error {
	return r.client.Close()
}
