package dashboard

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

type orgCountStore struct {
	counts map[uuid.UUID]Counts
	err    error
	calls  int
}

func newOrgCountStore() *orgCountStore {
	return &orgCountStore{counts: make(map[uuid.UUID]Counts)}
}

func (s *orgCountStore) Counts(_ context.Context, orgID uuid.UUID) (Counts, error) {
	s.calls++
	if s.err != nil {
		return Counts{}, s.err
	}
	return s.counts[orgID], nil
}

type countsFailingCache struct {
	getErr error
	setErr error
	delErr error
}

func (f countsFailingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, f.getErr
}
func (f countsFailingCache) Set(context.Context, string, []byte, time.Duration) error {
	return f.setErr
}
func (f countsFailingCache) Delete(context.Context, string) error { return f.delErr }

func TestCachedCountsSecondReadHitsCache(t *testing.T) {
	orgID := uuid.New()
	inner := newOrgCountStore()
	inner.counts[orgID] = Counts{Total: 4, Open: 2}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	first, err := cached.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("first counts: %v", err)
	}
	second, err := cached.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("second counts: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("database calls: %d, want 1", inner.calls)
	}
	if first != second {
		t.Fatalf("cached counts differ: %+v / %+v", first, second)
	}
}

func TestCachedCountsKeepTenantsSeparate(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	inner := newOrgCountStore()
	inner.counts[orgA] = Counts{Total: 2, Open: 1}
	inner.counts[orgB] = Counts{Total: 9, Open: 7}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if counts, _ := cached.Counts(ctx, orgA); counts.Total != 2 {
			t.Fatalf("org A: %+v", counts)
		}
		if counts, _ := cached.Counts(ctx, orgB); counts.Total != 9 {
			t.Fatalf("org B: %+v", counts)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("database calls: %d, want 2", inner.calls)
	}
}

func TestCachedCountsDoNotCacheErrors(t *testing.T) {
	wantErr := errors.New("database down")
	inner := &orgCountStore{err: wantErr}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()
	orgID := uuid.New()

	for i := 0; i < 2; i++ {
		if _, err := cached.Counts(ctx, orgID); !errors.Is(err, wantErr) {
			t.Fatalf("get %d: expected store error, got %v", i, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("errors were cached: %d calls", inner.calls)
	}
}

func TestCachedCountsTTLExpiry(t *testing.T) {
	orgID := uuid.New()
	inner := newOrgCountStore()
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 45*time.Second)
	ctx := context.Background()

	if _, err := cached.Counts(ctx, orgID); err != nil {
		t.Fatalf("first counts: %v", err)
	}
	memory.Advance(46 * time.Second)
	if _, err := cached.Counts(ctx, orgID); err != nil {
		t.Fatalf("second counts: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("TTL did not expire: %d calls", inner.calls)
	}
}

func TestCachedCountsCorruptEntryFallsBackAndRefreshes(t *testing.T) {
	orgID := uuid.New()
	inner := newOrgCountStore()
	inner.counts[orgID] = Counts{Total: 3}
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, time.Minute)
	ctx := context.Background()

	key, _ := platformcache.DashboardCountsKey(orgID)
	if err := memory.Set(ctx, key, []byte("not-json"), time.Minute); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	if counts, err := cached.Counts(ctx, orgID); err != nil || counts.Total != 3 {
		t.Fatalf("counts: %+v err=%v", counts, err)
	}
	if inner.calls != 1 {
		t.Fatalf("corrupt entry did not fall back: %d calls", inner.calls)
	}

	if _, err := cached.Counts(ctx, orgID); err != nil {
		t.Fatalf("second counts: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("corrupt entry was not refreshed: %d calls", inner.calls)
	}
}

func TestCachedCountsFailureFallsBack(t *testing.T) {
	orgID := uuid.New()
	inner := newOrgCountStore()
	inner.counts[orgID] = Counts{Total: 1}
	cached := NewCachedStore(inner, countsFailingCache{
		getErr: errors.New("redis down"),
		setErr: errors.New("redis down"),
		delErr: errors.New("redis down"),
	}, time.Minute)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if counts, err := cached.Counts(ctx, orgID); err != nil || counts.Total != 1 {
			t.Fatalf("get %d: %+v err=%v", i, counts, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("database calls: %d, want 2", inner.calls)
	}
}

func TestCachedCountsZeroTTLIsPassThrough(t *testing.T) {
	orgID := uuid.New()
	inner := newOrgCountStore()
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 0)
	ctx := context.Background()

	_, _ = cached.Counts(ctx, orgID)
	_, _ = cached.Counts(ctx, orgID)

	if inner.calls != 2 {
		t.Fatalf("database calls: %d, want 2", inner.calls)
	}
	if memory.Len() != 0 {
		t.Fatalf("disabled cache stored %d keys", memory.Len())
	}
}

func TestInvalidatorDropsOnlyTheOrgKey(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	memory := platformcache.NewMemory()
	ctx := context.Background()

	keyA, _ := platformcache.DashboardCountsKey(orgA)
	keyB, _ := platformcache.DashboardCountsKey(orgB)
	if err := memory.Set(ctx, keyA, []byte(`{"total":1}`), time.Minute); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := memory.Set(ctx, keyB, []byte(`{"total":2}`), time.Minute); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	invalidator := NewInvalidator(memory)
	if err := invalidator.InvalidateCounts(ctx, orgA); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	if _, ok, _ := memory.Get(ctx, keyA); ok {
		t.Fatal("org A counts were not invalidated")
	}
	if _, ok, _ := memory.Get(ctx, keyB); !ok {
		t.Fatal("org B counts were invalidated")
	}
}

func TestInvalidatorRejectsMissingOrg(t *testing.T) {
	invalidator := NewInvalidator(platformcache.NewMemory())
	err := invalidator.InvalidateCounts(context.Background(), uuid.Nil)
	if !errors.Is(err, platformcache.ErrMissingOrgID) {
		t.Fatalf("expected ErrMissingOrgID, got %v", err)
	}
}

func TestInvalidatorWithNilCacheIsNoop(t *testing.T) {
	var invalidator *Invalidator
	if err := invalidator.InvalidateCounts(context.Background(), uuid.New()); err != nil {
		t.Fatalf("nil invalidator: %v", err)
	}
	if err := NewInvalidator(nil).InvalidateCounts(context.Background(), uuid.New()); err != nil {
		t.Fatalf("nil cache: %v", err)
	}
}

func TestCachedCountsWithPostgresAndRedis(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name) VALUES ($1, $2, 'Customer') RETURNING id`,
		orgID, "dash-cache-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	redisCache := platformcache.NewRedis(platformcache.RedisOptions{
		Addr:    mr.Addr(),
		Timeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = redisCache.Close() })

	cached := NewCachedStore(repo, redisCache, 45*time.Second)
	key, _ := platformcache.DashboardCountsKey(orgID)

	first, err := cached.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("first counts: %v", err)
	}
	if first.Total != 0 {
		t.Fatalf("first counts: %+v", first)
	}
	if !mr.Exists(key) {
		t.Fatal("counts were not cached")
	}

	insertTicket(t, pool, orgID, customerID, ticketSeed{
		number: 1, status: "new", priority: "urgent",
	})

	stale, err := cached.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("cached counts: %v", err)
	}
	if stale.Total != 0 {
		t.Fatalf("cached read bypassed Redis: %+v", stale)
	}

	invalidator := NewInvalidator(redisCache)
	if err := invalidator.InvalidateCounts(ctx, orgID); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if mr.Exists(key) {
		t.Fatal("invalidator left the key behind")
	}

	fresh, err := cached.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("fresh counts: %v", err)
	}
	if fresh.Total != 1 || fresh.Open != 1 || fresh.ByPriority.Urgent != 1 {
		t.Fatalf("fresh counts: %+v", fresh)
	}
	if !mr.Exists(key) {
		t.Fatal("counts were not re-cached after invalidation")
	}

	dbCounts, err := repo.Counts(ctx, orgID)
	if err != nil {
		t.Fatalf("database counts: %v", err)
	}
	if dbCounts != fresh {
		t.Fatalf("database %+v, cache %+v", dbCounts, fresh)
	}
}
