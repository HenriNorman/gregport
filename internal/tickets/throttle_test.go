package tickets

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	platformcache "github.com/HenriNorman/gregport/internal/platform/cache"
)

type countingUpdate struct {
	mu    sync.Mutex
	calls int
	ids   []uuid.UUID
	err   error
}

func (c *countingUpdate) Update(ctx context.Context, keyID uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.ids = append(c.ids, keyID)
	if c.err != nil {
		return c.err
	}
	return nil
}

func (c *countingUpdate) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func waitForCalls(t *testing.T, update *countingUpdate, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if update.count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("update calls: %d, want at least %d", update.count(), want)
}

func throttleWithRedis(t *testing.T, updateFn func(context.Context, uuid.UUID) error) (*LastUsedThrottle, *redis.Client, func()) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	throttle := NewLastUsedThrottle(updateFn)
	throttle.SetRedisClient(client)
	return throttle, client, func() { mr.Close() }
}

func TestRedisThrottleUpdatesOncePerWindow(t *testing.T) {
	orgID := uuid.New()
	keyID := uuid.New()
	update := &countingUpdate{}
	throttle, _, cleanup := throttleWithRedis(t, update.Update)
	defer cleanup()

	ctx := context.Background()

	throttle.mark(ctx, orgID, keyID)
	throttle.mark(ctx, orgID, keyID)
	throttle.mark(ctx, orgID, keyID)

	waitForCalls(t, update, 1)
	if got := update.count(); got != 1 {
		t.Fatalf("update calls: %d, want 1 (throttled within window)", got)
	}
}

func TestRedisThrottleUpdatesAgainAfterWindow(t *testing.T) {
	orgID := uuid.New()
	keyID := uuid.New()
	update := &countingUpdate{}
	throttle, client, cleanup := throttleWithRedis(t, update.Update)
	defer cleanup()

	ctx := context.Background()

	throttle.mark(ctx, orgID, keyID)
	waitForCalls(t, update, 1)

	// Within the window: the gate is held, no more updates.
	throttle.mark(ctx, orgID, keyID)
	time.Sleep(60 * time.Millisecond)
	if got := update.count(); got != 1 {
		t.Fatalf("update calls: %d, want 1 (still throttled)", got)
	}

	// Simulate the Redis TTL expiring: the gate is released, so the next mark
	// updates again. (Production relies on Redis TTL for window expiry.)
	key, _ := platformcache.APIKeyLastUsedKey(orgID, keyID)
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatalf("del: %v", err)
	}

	throttle.mark(ctx, orgID, keyID)
	waitForCalls(t, update, 2)
	if got := update.count(); got != 2 {
		t.Fatalf("post-window update calls: %d, want 2", got)
	}
}

func TestRedisThrottleKeysAreIndependent(t *testing.T) {
	orgID := uuid.New()
	keyA := uuid.New()
	keyB := uuid.New()
	update := &countingUpdate{}
	throttle, _, cleanup := throttleWithRedis(t, update.Update)
	defer cleanup()

	ctx := context.Background()
	throttle.mark(ctx, orgID, keyA)
	throttle.mark(ctx, orgID, keyB)

	waitForCalls(t, update, 2)
	if got := update.count(); got != 2 {
		t.Fatalf("update calls: %d, want 2 (different keys are independent)", got)
	}
}

func TestRedisThrottleKeysAreTenantScoped(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	keyID := uuid.New()
	update := &countingUpdate{}
	throttle, _, cleanup := throttleWithRedis(t, update.Update)
	defer cleanup()

	ctx := context.Background()
	throttle.mark(ctx, orgA, keyID)
	throttle.mark(ctx, orgB, keyID)

	waitForCalls(t, update, 2)
	if got := update.count(); got != 2 {
		t.Fatalf("update calls: %d, want 2 (same key in different orgs is independent)", got)
	}
}

func TestRedisThrottleRedisDownFailOpen(t *testing.T) {
	orgID := uuid.New()
	keyID := uuid.New()
	update := &countingUpdate{}
	throttle := NewLastUsedThrottle(update.Update)
	// Point at a dead address so every Redis call errors.
	throttle.SetRedisClient(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))

	ctx := context.Background()

	throttle.mark(ctx, orgID, keyID)
	time.Sleep(300 * time.Millisecond)

	// Fail open: do not hammer the database when Redis is unavailable.
	if got := update.count(); got != 0 {
		t.Fatalf("update calls: %d, want 0 (Redis down, DB protected)", got)
	}
}

func TestInMemoryFallbackThrottles(t *testing.T) {
	keyID := uuid.New()
	update := &countingUpdate{}
	throttle := NewLastUsedThrottle(update.Update)
	// No Redis client: in-memory fallback.

	ctx := context.Background()
	throttle.mark(ctx, uuid.New(), keyID)
	throttle.mark(ctx, uuid.New(), keyID)

	waitForCalls(t, update, 1)
	if got := update.count(); got != 1 {
		t.Fatalf("update calls: %d, want 1 (in-memory throttle)", got)
	}
}

func TestRedisThrottleUpdateErrorDoesNotBlockAuth(t *testing.T) {
	orgID := uuid.New()
	keyID := uuid.New()
	update := &countingUpdate{err: errors.New("db down")}
	throttle, _, cleanup := throttleWithRedis(t, update.Update)
	defer cleanup()

	ctx := context.Background()
	throttle.mark(ctx, orgID, keyID)

	waitForCalls(t, update, 1)
	if got := update.count(); got != 1 {
		t.Fatalf("update calls: %d, want 1", got)
	}

	// The key is already marked in Redis, so a second mark within the window
	// does not retry the failing update.
	throttle.mark(ctx, orgID, keyID)
	time.Sleep(60 * time.Millisecond)
	if got := update.count(); got != 1 {
		t.Fatalf("update calls after failed update: %d, want 1", got)
	}
}
