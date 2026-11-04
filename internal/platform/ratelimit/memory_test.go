package ratelimit

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestMemoryAllowBoundaryAndReset(t *testing.T) {
	limiter := NewMemory()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	ctx := context.Background()
	window := time.Minute

	for i := 1; i <= 2; i++ {
		result, err := limiter.Allow(ctx, "key", 2, window)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !result.Allowed || result.Remaining != 2-i {
			t.Fatalf("request %d: %+v", i, result)
		}
	}

	result, err := limiter.Allow(ctx, "key", 2, window)
	if err != nil {
		t.Fatalf("denied request: %v", err)
	}
	if result.Allowed || result.Remaining != 0 || result.RetryAfter <= 0 {
		t.Fatalf("denied result: %+v", result)
	}

	now = now.Add(window)
	if result, err := limiter.Allow(ctx, "key", 2, window); err != nil || !result.Allowed {
		t.Fatalf("request after reset: %+v err=%v", result, err)
	}
}

func TestMemoryKeysAreIsolated(t *testing.T) {
	limiter := NewMemory()
	ctx := context.Background()

	if r, _ := limiter.Allow(ctx, "a", 1, time.Minute); !r.Allowed {
		t.Fatal("first key denied")
	}
	if r, _ := limiter.Allow(ctx, "a", 1, time.Minute); r.Allowed {
		t.Fatal("second request on the same key allowed")
	}
	if r, _ := limiter.Allow(ctx, "b", 1, time.Minute); !r.Allowed {
		t.Fatal("different key shared the bucket")
	}
}

func TestMemoryRejectsInvalidArguments(t *testing.T) {
	limiter := NewMemory()

	for _, tc := range []struct {
		limit  int
		window time.Duration
	}{
		{0, time.Minute},
		{1, 0},
		{1, -time.Second},
	} {
		if _, err := limiter.Allow(context.Background(), "key", tc.limit, tc.window); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit=%d window=%s: %v", tc.limit, tc.window, err)
		}
	}
}

func TestMemorySweepsExpiredBuckets(t *testing.T) {
	limiter := NewMemory()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 1100; i++ {
		key := "key-" + strconv.Itoa(i)
		if _, err := limiter.Allow(ctx, key, 1, time.Minute); err != nil {
			t.Fatalf("allow: %v", err)
		}
	}

	now = now.Add(2 * time.Minute)
	if _, err := limiter.Allow(ctx, "fresh", 1, time.Minute); err != nil {
		t.Fatalf("allow fresh: %v", err)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	for key, bucket := range limiter.buckets {
		if key != "fresh" && !now.Before(bucket.reset) {
			t.Fatalf("expired bucket %q was not swept", key)
		}
	}
}
