package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryRoundTripAndExpiry(t *testing.T) {
	c := NewMemory()
	ctx := context.Background()

	value, ok, err := c.Get(ctx, "key")
	if err != nil || ok || value != nil {
		t.Fatalf("expected miss: %q %v %v", value, ok, err)
	}

	input := []byte("value")
	if err := c.Set(ctx, "key", input, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	input[0] = 'X' // the cache must keep its own copy

	value, ok, err = c.Get(ctx, "key")
	if err != nil || !ok || string(value) != "value" {
		t.Fatalf("get: %q %v %v", value, ok, err)
	}

	c.Advance(time.Minute)
	_, ok, err = c.Get(ctx, "key")
	if err != nil || ok {
		t.Fatalf("expected expiry: ok=%v err=%v", ok, err)
	}
	if c.Len() != 0 {
		t.Fatalf("expired key was not swept: len=%d", c.Len())
	}

	if err := c.Set(ctx, "key", []byte("value"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := c.Delete(ctx, "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, err = c.Get(ctx, "key")
	if err != nil || ok {
		t.Fatalf("expected miss after delete: ok=%v err=%v", ok, err)
	}
}

func TestMemoryRejectsNonPositiveTTL(t *testing.T) {
	c := NewMemory()

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := c.Set(context.Background(), "key", []byte("value"), ttl); !errors.Is(err, ErrInvalidTTL) {
			t.Fatalf("ttl %s: expected ErrInvalidTTL, got %v", ttl, err)
		}
	}
}

func TestMemoryPing(t *testing.T) {
	if err := NewMemory().Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
