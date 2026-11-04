package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Memory is a fixed-window limiter used only as the login fallback when Redis
// is unavailable. It is intentionally stricter than the shared limiter.
type Memory struct {
	mu      sync.Mutex
	buckets map[string]memoryBucket
	now     func() time.Time
}

type memoryBucket struct {
	count int
	reset time.Time
}

// NewMemory creates an empty fallback limiter.
func NewMemory() *Memory {
	return &Memory{
		buckets: make(map[string]memoryBucket),
		now:     time.Now,
	}
}

// Allow counts one request against key in the current fixed window.
func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 || window <= 0 {
		return Result{}, ErrInvalidLimit
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	bucket, ok := m.buckets[key]
	if !ok || !now.Before(bucket.reset) {
		bucket = memoryBucket{reset: now.Add(window)}
	}

	if bucket.count >= limit {
		m.buckets[key] = bucket
		retry := bucket.reset.Sub(now)
		if retry < 0 {
			retry = 0
		}
		return Result{
			Allowed:    false,
			Limit:      limit,
			Remaining:  0,
			Reset:      bucket.reset,
			RetryAfter: retry,
		}, nil
	}

	bucket.count++
	m.buckets[key] = bucket
	m.sweep(now)

	return Result{
		Allowed:   true,
		Limit:     limit,
		Remaining: limit - bucket.count,
		Reset:     bucket.reset,
	}, nil
}

// sweep drops expired buckets once the map grows beyond a small threshold.
func (m *Memory) sweep(now time.Time) {
	if len(m.buckets) < 1024 {
		return
	}
	for key, bucket := range m.buckets {
		if !now.Before(bucket.reset) {
			delete(m.buckets, key)
		}
	}
}
