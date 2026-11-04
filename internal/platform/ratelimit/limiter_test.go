package ratelimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedisLimiter(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedis(client), mr
}

func TestRedisAllowBoundary(t *testing.T) {
	limiter, _ := newRedisLimiter(t)
	ctx := context.Background()
	window := time.Minute

	for i := 1; i <= 3; i++ {
		result, err := limiter.Allow(ctx, "org:test:user:1", 3, window)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !result.Allowed {
			t.Fatalf("request %d denied", i)
		}
		if result.Limit != 3 || result.Remaining != 3-i {
			t.Fatalf("request %d: %+v", i, result)
		}
		if result.RetryAfter != 0 {
			t.Fatalf("request %d retry after: %s", i, result.RetryAfter)
		}
		if !result.Reset.After(time.Now()) {
			t.Fatalf("request %d reset is not in the future: %s", i, result.Reset)
		}
	}

	result, err := limiter.Allow(ctx, "org:test:user:1", 3, window)
	if err != nil {
		t.Fatalf("denied request: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected the request after the limit to be denied")
	}
	if result.Remaining != 0 {
		t.Fatalf("remaining: %d", result.Remaining)
	}
	if result.RetryAfter <= 0 || result.RetryAfter > window {
		t.Fatalf("retry after: %s", result.RetryAfter)
	}
}

func TestRedisKeysAreIsolated(t *testing.T) {
	limiter, _ := newRedisLimiter(t)
	ctx := context.Background()

	if r, _ := limiter.Allow(ctx, "org:a:user:1", 1, time.Minute); !r.Allowed {
		t.Fatal("first key was denied")
	}
	if r, _ := limiter.Allow(ctx, "org:a:user:1", 1, time.Minute); r.Allowed {
		t.Fatal("second request on the same key was allowed")
	}
	if r, _ := limiter.Allow(ctx, "org:b:user:1", 1, time.Minute); !r.Allowed {
		t.Fatal("a different org shared the bucket")
	}
}

func TestRedisWindowResets(t *testing.T) {
	limiter, mr := newRedisLimiter(t)
	ctx := context.Background()

	if r, _ := limiter.Allow(ctx, "key", 1, time.Minute); !r.Allowed {
		t.Fatal("first request was denied")
	}
	if r, _ := limiter.Allow(ctx, "key", 1, time.Minute); r.Allowed {
		t.Fatal("second request was allowed")
	}

	mr.FastForward(time.Minute + time.Millisecond)

	if r, err := limiter.Allow(ctx, "key", 1, time.Minute); err != nil || !r.Allowed {
		t.Fatalf("request after the window: %+v err=%v", r, err)
	}
}

func TestRedisConcurrentBoundary(t *testing.T) {
	limiter, _ := newRedisLimiter(t)
	ctx := context.Background()

	const limit = 10
	const requests = 40
	var allowed atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := limiter.Allow(ctx, "concurrent", limit, time.Minute)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			if result.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != limit {
		t.Fatalf("allowed %d requests, want %d", got, limit)
	}
}

func TestRedisRejectsInvalidArguments(t *testing.T) {
	limiter, _ := newRedisLimiter(t)

	for _, tc := range []struct {
		limit  int
		window time.Duration
	}{
		{0, time.Minute},
		{-1, time.Minute},
		{1, 0},
		{1, -time.Second},
	} {
		if _, err := limiter.Allow(context.Background(), "key", tc.limit, tc.window); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit=%d window=%s: %v", tc.limit, tc.window, err)
		}
	}
}

func TestRedisUnavailableReturnsError(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	limiter := NewRedis(client)
	if _, err := limiter.Allow(context.Background(), "key", 1, time.Minute); err == nil {
		t.Fatal("expected redis error")
	}
}
