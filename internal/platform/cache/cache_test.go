package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestRedisRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewRedis(RedisOptions{Addr: mr.Addr(), Timeout: 50 * time.Millisecond})
	ctx := context.Background()

	value, ok, err := c.Get(ctx, "org:test:settings")
	if err != nil {
		t.Fatalf("get miss: %v", err)
	}
	if ok || value != nil {
		t.Fatalf("expected miss, got %q ok=%v", value, ok)
	}

	if err := c.Set(ctx, "org:test:settings", []byte(`{"timezone":"UTC"}`), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	value, ok, err = c.Get(ctx, "org:test:settings")
	if err != nil {
		t.Fatalf("get hit: %v", err)
	}
	if !ok || string(value) != `{"timezone":"UTC"}` {
		t.Fatalf("unexpected value: %q ok=%v", value, ok)
	}

	if err := c.Delete(ctx, "org:test:settings"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, err = c.Get(ctx, "org:test:settings")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestRedisTTLExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewRedis(RedisOptions{Addr: mr.Addr(), Timeout: 50 * time.Millisecond})
	ctx := context.Background()

	if err := c.Set(ctx, "key", []byte("value"), time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}
	mr.FastForward(time.Second)

	_, ok, err := c.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("expected key to expire")
	}
}

func TestRedisRejectsNonPositiveTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewRedis(RedisOptions{Addr: mr.Addr()})

	if err := c.Set(context.Background(), "key", []byte("value"), 0); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("expected ErrInvalidTTL, got %v", err)
	}
}

func TestRedisPingAndClose(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewRedis(RedisOptions{Addr: mr.Addr(), Timeout: 50 * time.Millisecond})

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestRedisUnavailableReturnsError(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	c := NewRedis(RedisOptions{
		Addr:         addr,
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		Timeout:      20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = c.Close() })

	if _, _, err := c.Get(context.Background(), "key"); err == nil {
		t.Fatal("expected redis error")
	}
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected ping error")
	}
}
