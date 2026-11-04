package organizations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	platformcache "github.com/HenriNorman/gregport/internal/platform/cache"
)

type mapStore struct {
	settings map[uuid.UUID]Settings
	times    map[uuid.UUID]time.Time
	getCalls int
}

func newMapStore() *mapStore {
	return &mapStore{settings: make(map[uuid.UUID]Settings), times: make(map[uuid.UUID]time.Time)}
}

func (m *mapStore) GetSettings(_ context.Context, orgID uuid.UUID) (Settings, time.Time, error) {
	m.getCalls++
	settings, ok := m.settings[orgID]
	if !ok {
		return Settings{}, time.Time{}, ErrOrgNotFound
	}
	return settings, m.times[orgID], nil
}

func (m *mapStore) UpdateSettings(_ context.Context, orgID uuid.UUID, settings Settings) (Settings, time.Time, error) {
	m.settings[orgID] = settings
	m.times[orgID] = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	return settings, m.times[orgID], nil
}

type failingCache struct {
	getErr error
	setErr error
	delErr error
}

func (f failingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, f.getErr
}
func (f failingCache) Set(context.Context, string, []byte, time.Duration) error {
	return f.setErr
}
func (f failingCache) Delete(context.Context, string) error { return f.delErr }

func TestCachedStoreSecondReadHitsCache(t *testing.T) {
	updatedAt := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	inner := &fakeStore{
		settings:  Settings{Timezone: "Africa/Nairobi", DefaultPriority: "high"},
		updatedAt: updatedAt,
	}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	first, firstAt, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	second, secondAt, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}

	if inner.getCalls != 1 {
		t.Fatalf("database reads: %d, want 1", inner.getCalls)
	}
	if first != second || !firstAt.Equal(secondAt) || !firstAt.Equal(updatedAt) {
		t.Fatalf("cached values differ: %+v %s / %+v %s", first, firstAt, second, secondAt)
	}
}

func TestCachedStoreKeepsTenantsSeparate(t *testing.T) {
	inner := newMapStore()
	orgA := uuid.New()
	orgB := uuid.New()
	inner.settings[orgA] = Settings{Timezone: "Europe/London"}
	inner.settings[orgB] = Settings{Timezone: "Asia/Tokyo"}
	inner.times[orgA] = time.Now().UTC()
	inner.times[orgB] = time.Now().UTC()

	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if settings, _, _ := cached.GetSettings(ctx, orgA); settings.Timezone != "Europe/London" {
		t.Fatalf("org A: %+v", settings)
	}
	if settings, _, _ := cached.GetSettings(ctx, orgB); settings.Timezone != "Asia/Tokyo" {
		t.Fatalf("org B: %+v", settings)
	}
	if settings, _, _ := cached.GetSettings(ctx, orgA); settings.Timezone != "Europe/London" {
		t.Fatalf("org A after org B: %+v", settings)
	}
	if inner.getCalls != 2 {
		t.Fatalf("database reads: %d, want 2", inner.getCalls)
	}
}

func TestCachedStoreUpdateInvalidates(t *testing.T) {
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if inner.getCalls != 1 {
		t.Fatalf("database reads after warm: %d", inner.getCalls)
	}

	saved, _, err := cached.UpdateSettings(ctx, orgID, Settings{
		Timezone:        "Europe/London",
		DefaultPriority: "urgent",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if saved.Timezone != "Europe/London" {
		t.Fatalf("saved: %+v", saved)
	}

	reloaded, _, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if inner.getCalls != 2 {
		t.Fatalf("database reads after update: %d, want 2", inner.getCalls)
	}
	if reloaded.Timezone != "Europe/London" || reloaded.DefaultPriority != "urgent" {
		t.Fatalf("reloaded: %+v", reloaded)
	}
}

func TestCachedStoreFailedUpdateKeepsCache(t *testing.T) {
	wantErr := errors.New("write failed")
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		updateErr: wantErr,
	}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if _, _, err := cached.UpdateSettings(ctx, orgID, Settings{Timezone: "Europe/London"}); !errors.Is(err, wantErr) {
		t.Fatalf("expected update error, got %v", err)
	}

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if inner.getCalls != 1 {
		t.Fatalf("failed update evicted the cache: %d reads", inner.getCalls)
	}
}

func TestCachedStoreCorruptEntryFallsBackAndRefreshes(t *testing.T) {
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	key, err := platformcache.OrgSettingsKey(orgID)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := memory.Set(ctx, key, []byte("not-json"), time.Minute); err != nil {
		t.Fatalf("seed corrupt entry: %v", err)
	}

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("get with corrupt cache: %v", err)
	}
	if inner.getCalls != 1 {
		t.Fatalf("database reads: %d, want 1", inner.getCalls)
	}

	// The miss must overwrite the corrupt entry.
	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("get after refresh: %v", err)
	}
	if inner.getCalls != 1 {
		t.Fatalf("corrupt entry was not refreshed: %d reads", inner.getCalls)
	}
}

func TestCachedStoreTTLExpiry(t *testing.T) {
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 30*time.Second)
	ctx := context.Background()
	orgID := uuid.New()

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("first get: %v", err)
	}
	memory.Advance(31 * time.Second)
	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if inner.getCalls != 2 {
		t.Fatalf("database reads after TTL: %d, want 2", inner.getCalls)
	}
}

func TestCachedStoreCacheFailureFallsBack(t *testing.T) {
	wantErr := errors.New("redis down")
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
	cached := NewCachedStore(inner, failingCache{getErr: wantErr, setErr: wantErr, delErr: wantErr}, time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if inner.getCalls != 2 {
		t.Fatalf("database reads: %d, want 2", inner.getCalls)
	}

	if _, _, err := cached.UpdateSettings(ctx, orgID, Settings{Timezone: "Europe/London"}); err != nil {
		t.Fatalf("update must ignore cache delete failure: %v", err)
	}
}

func TestCachedStoreDoesNotCacheNotFound(t *testing.T) {
	inner := &fakeStore{getErr: ErrOrgNotFound}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	for i := 0; i < 2; i++ {
		if _, _, err := cached.GetSettings(ctx, orgID); !errors.Is(err, ErrOrgNotFound) {
			t.Fatalf("get %d: expected ErrOrgNotFound, got %v", i, err)
		}
	}
	if inner.getCalls != 2 {
		t.Fatalf("not-found result was cached: %d reads", inner.getCalls)
	}

	inner.getErr = nil
	inner.settings = Settings{Timezone: "UTC", DefaultPriority: "normal"}
	inner.updatedAt = time.Now().UTC()
	if _, _, err := cached.GetSettings(ctx, orgID); err != nil {
		t.Fatalf("get after create: %v", err)
	}
}

func TestCachedStoreZeroTTLIsPassThrough(t *testing.T) {
	inner := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updatedAt: time.Now().UTC(),
	}
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 0)
	ctx := context.Background()
	orgID := uuid.New()

	_, _, _ = cached.GetSettings(ctx, orgID)
	_, _, _ = cached.GetSettings(ctx, orgID)

	if inner.getCalls != 2 {
		t.Fatalf("database reads: %d, want 2", inner.getCalls)
	}
	if memory.Len() != 0 {
		t.Fatalf("disabled cache stored %d keys", memory.Len())
	}
}

func TestCachedStoreWithPostgresAndRedis(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	redisCache := platformcache.NewRedis(platformcache.RedisOptions{
		Addr:    mr.Addr(),
		Timeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = redisCache.Close() })

	cached := NewCachedStore(repo, redisCache, time.Minute)
	key, err := platformcache.OrgSettingsKey(orgID)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	first, firstAt, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if !mr.Exists(key) {
		t.Fatal("settings were not cached")
	}

	// Change the row behind the cache; a cached read must not see it.
	if _, err := pool.Exec(ctx,
		`UPDATE organizations SET settings = '{"timezone":"Europe/Paris"}' WHERE id = $1`, orgID,
	); err != nil {
		t.Fatalf("direct update: %v", err)
	}
	second, secondAt, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if second != first || !secondAt.Equal(firstAt) {
		t.Fatalf("read did not come from cache: %+v vs %+v", second, first)
	}

	if _, _, err := cached.UpdateSettings(ctx, orgID, Settings{
		Timezone:        "Africa/Nairobi",
		DefaultPriority: "high",
		SupportEmail:    "support@acme.com",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if mr.Exists(key) {
		t.Fatal("update did not invalidate the cache")
	}

	third, _, err := cached.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if third.Timezone != "Africa/Nairobi" || third.DefaultPriority != "high" || third.SupportEmail != "support@acme.com" {
		t.Fatalf("stale settings after update: %+v", third)
	}
	if !mr.Exists(key) {
		t.Fatal("settings were not re-cached after update")
	}

	// The database itself must hold the new document.
	stored, _, err := repo.GetSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("database read: %v", err)
	}
	if stored != third {
		t.Fatalf("database %+v, cache %+v", stored, third)
	}
}
