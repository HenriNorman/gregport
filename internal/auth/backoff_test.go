package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisLoginGuard_BacksOffThenResets(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	guard := NewRedisLoginGuard(client, 3, time.Minute)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, err := guard.Allowed(ctx, "user@example.com")
		if err != nil {
			t.Fatalf("allowed: %v", err)
		}
		if !allowed {
			t.Fatalf("blocked before threshold at attempt %d", i+1)
		}
		if err := guard.Failure(ctx, "user@example.com"); err != nil {
			t.Fatalf("failure: %v", err)
		}
	}

	allowed, err := guard.Allowed(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("allowed after threshold: %v", err)
	}
	if allowed {
		t.Fatal("expected the account to be blocked after the threshold")
	}

	if err := guard.Reset(ctx, "user@example.com"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	allowed, err = guard.Allowed(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("allowed after reset: %v", err)
	}
	if !allowed {
		t.Fatal("expected a successful login to clear the backoff")
	}
}

func TestRedisLoginGuard_KeysDoNotContainEmail(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	guard := NewRedisLoginGuard(client, 3, time.Minute)
	if err := guard.Failure(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("failure: %v", err)
	}

	keys := mr.Keys()
	if len(keys) != 1 {
		t.Fatalf("expected one key, got %v", keys)
	}
	for _, key := range keys {
		if strings.Contains(key, "user@example.com") {
			t.Fatalf("login guard key leaks the email address: %s", key)
		}
	}
}
