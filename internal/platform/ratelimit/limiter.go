// Package ratelimit implements the atomic sliding-window limiter used by the
// API. Redis is the shared implementation; Memory is the login fallback when
// Redis is unavailable.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ErrInvalidLimit is returned for a non-positive request limit or window.
var ErrInvalidLimit = errors.New("ratelimit: limit and window must be positive")

// Result describes one limiter decision.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	Reset      time.Time
	RetryAfter time.Duration
}

// Limiter is the shared contract for Redis and the login fallback.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// Redis implements Limiter with an atomic Lua sliding-window log.
type Redis struct {
	client redis.Scripter
	script *redis.Script
}

// NewRedis creates a Redis limiter. The scripter is usually a *redis.Client.
func NewRedis(client redis.Scripter) *Redis {
	return &Redis{
		client: client,
		script: redis.NewScript(slidingWindowScript),
	}
}

// slidingWindowScript keeps a sorted set of request timestamps. Expired
// entries are removed before the current request is counted, so the decision
// and the state update happen in one atomic server-side step.
const slidingWindowScript = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local member = ARGV[3]

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local window_start = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)
local count = redis.call('ZCARD', key)

if count >= limit then
  local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
  local reset = now + window
  if oldest[2] ~= nil then
    reset = tonumber(oldest[2]) + window
  end
  redis.call('PEXPIRE', key, window)
  return {0, count, reset, now}
end

redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, window)
count = count + 1

local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local reset = now + window
if oldest[2] ~= nil then
  reset = tonumber(oldest[2]) + window
end
return {1, count, reset, now}
`

// Allow counts one request against key.
func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 || window <= 0 {
		return Result{}, ErrInvalidLimit
	}

	raw, err := r.script.Run(ctx, r.client, []string{key},
		limit, window.Milliseconds(), uuid.NewString(),
	).Result()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: %w", err)
	}

	values, ok := raw.([]interface{})
	if !ok || len(values) != 4 {
		return Result{}, fmt.Errorf("ratelimit: unexpected script result %T", raw)
	}

	allowed, err := toInt64(values[0])
	if err != nil {
		return Result{}, err
	}
	count, err := toInt64(values[1])
	if err != nil {
		return Result{}, err
	}
	resetMS, err := toInt64(values[2])
	if err != nil {
		return Result{}, err
	}
	nowMS, err := toInt64(values[3])
	if err != nil {
		return Result{}, err
	}

	remaining := limit - int(count)
	if remaining < 0 {
		remaining = 0
	}
	retryAfter := time.Duration(resetMS-nowMS) * time.Millisecond
	if allowed == 1 {
		retryAfter = 0
	}
	if retryAfter < 0 {
		retryAfter = 0
	}

	return Result{
		Allowed:    allowed == 1,
		Limit:      limit,
		Remaining:  remaining,
		Reset:      time.UnixMilli(resetMS),
		RetryAfter: retryAfter,
	}, nil
}

func toInt64(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		var n int64
		if _, err := fmt.Sscan(v, &n); err != nil {
			return 0, fmt.Errorf("ratelimit: invalid script number %q", v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("ratelimit: unexpected script value %T", value)
	}
}
