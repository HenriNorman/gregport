package sla

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	platformcache "github.com/HenriNorman/gregport/internal/platform/cache"
)

type cacheTestStore struct {
	policies []Policy

	listCalls   int
	getCalls    int
	createCalls int
	updateCalls int
	deleteCalls int

	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
}

func (s *cacheTestStore) List(_ context.Context, orgID uuid.UUID) ([]Policy, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]Policy, 0)
	for _, policy := range s.policies {
		if policy.OrgID == orgID {
			out = append(out, policy)
		}
	}
	return out, nil
}

func (s *cacheTestStore) Get(_ context.Context, orgID, policyID uuid.UUID) (Policy, error) {
	s.getCalls++
	if s.getErr != nil {
		return Policy{}, s.getErr
	}
	for _, policy := range s.policies {
		if policy.OrgID == orgID && policy.ID == policyID {
			return policy, nil
		}
	}
	return Policy{}, ErrNotFound
}

func (s *cacheTestStore) Create(_ context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	s.createCalls++
	if s.createErr != nil {
		return Policy{}, s.createErr
	}
	policy.ID = uuid.New()
	policy.OrgID = orgID
	policy.CreatedAt = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s.policies = append(s.policies, policy)
	return policy, nil
}

func (s *cacheTestStore) Update(_ context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	s.updateCalls++
	if s.updateErr != nil {
		return Policy{}, s.updateErr
	}
	policy.OrgID = orgID
	for i := range s.policies {
		if s.policies[i].OrgID == orgID && s.policies[i].ID == policy.ID {
			s.policies[i] = policy
			return policy, nil
		}
	}
	return Policy{}, ErrNotFound
}

func (s *cacheTestStore) Delete(_ context.Context, orgID, policyID uuid.UUID) error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	for i := range s.policies {
		if s.policies[i].OrgID == orgID && s.policies[i].ID == policyID {
			s.policies = append(s.policies[:i], s.policies[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func seedPolicy(store *cacheTestStore, orgID uuid.UUID, name string) Policy {
	policy := Policy{
		ID:                   uuid.New(),
		OrgID:                orgID,
		Name:                 name,
		Priority:             "normal",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    120,
		CreatedAt:            time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}
	store.policies = append(store.policies, policy)
	return policy
}

type slaFailingCache struct {
	getErr error
	setErr error
	delErr error
}

func (f slaFailingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, f.getErr
}
func (f slaFailingCache) Set(context.Context, string, []byte, time.Duration) error {
	return f.setErr
}
func (f slaFailingCache) Delete(context.Context, string) error { return f.delErr }

func TestCachedStoreListHitsCache(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	first, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	second, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("second list: %v", err)
	}

	if inner.listCalls != 1 {
		t.Fatalf("database list calls: %d, want 1", inner.listCalls)
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("cached list differs: %+v / %+v", first, second)
	}
}

func TestCachedStoreGetHitsCache(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	first, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	second, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}

	if inner.getCalls != 1 {
		t.Fatalf("database get calls: %d, want 1", inner.getCalls)
	}
	if first != second {
		t.Fatalf("cached policy differs: %+v / %+v", first, second)
	}
}

func TestCachedStoreListAndItemCachesAreIndependent(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if inner.listCalls != 1 || inner.getCalls != 1 {
		t.Fatalf("calls: list=%d get=%d", inner.listCalls, inner.getCalls)
	}
}

func TestCachedStoreCreateInvalidatesList(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("warm list: %v", err)
	}
	if _, err := cached.Create(ctx, orgID, Policy{
		Name: "High", Priority: "high", FirstResponseMinutes: 15, ResolutionMinutes: 60,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	policies, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if inner.listCalls != 2 {
		t.Fatalf("list cache was not invalidated: %d calls", inner.listCalls)
	}
	if len(policies) != 2 {
		t.Fatalf("policies after create: %+v", policies)
	}
}

func TestCachedStoreUpdateInvalidatesItemAndList(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("warm item: %v", err)
	}
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("warm list: %v", err)
	}

	policy.Name = "Renamed"
	if _, err := cached.Update(ctx, orgID, policy); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if inner.getCalls != 2 || got.Name != "Renamed" {
		t.Fatalf("item cache stale: calls=%d policy=%+v", inner.getCalls, got)
	}

	policies, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if inner.listCalls != 2 || policies[0].Name != "Renamed" {
		t.Fatalf("list cache stale: calls=%d policies=%+v", inner.listCalls, policies)
	}
}

func TestCachedStoreDeleteInvalidatesItemAndList(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("warm item: %v", err)
	}
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("warm list: %v", err)
	}

	if err := cached.Delete(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := cached.Get(ctx, orgID, policy.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: expected ErrNotFound, got %v", err)
	}
	if inner.getCalls != 2 {
		t.Fatalf("item cache was not invalidated: %d calls", inner.getCalls)
	}

	policies, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if inner.listCalls != 2 || len(policies) != 0 {
		t.Fatalf("list cache was not invalidated: calls=%d policies=%+v", inner.listCalls, policies)
	}
}

func TestCachedStoreFailedWritesKeepCache(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("warm item: %v", err)
	}
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("warm list: %v", err)
	}

	wantErr := errors.New("write failed")
	inner.createErr = wantErr
	inner.updateErr = wantErr
	inner.deleteErr = wantErr

	if _, err := cached.Create(ctx, orgID, Policy{Name: "High", Priority: "high", FirstResponseMinutes: 1, ResolutionMinutes: 2}); !errors.Is(err, wantErr) {
		t.Fatalf("create error: %v", err)
	}
	if _, err := cached.Update(ctx, orgID, policy); !errors.Is(err, wantErr) {
		t.Fatalf("update error: %v", err)
	}
	if err := cached.Delete(ctx, orgID, policy.ID); !errors.Is(err, wantErr) {
		t.Fatalf("delete error: %v", err)
	}

	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("cached item was evicted by a failed write: %v", err)
	}
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("cached list was evicted by a failed write: %v", err)
	}
	if inner.getCalls != 1 || inner.listCalls != 1 {
		t.Fatalf("failed writes hit the database: get=%d list=%d", inner.getCalls, inner.listCalls)
	}
}

func TestCachedStoreDoesNotCacheNotFound(t *testing.T) {
	orgID := uuid.New()
	policyID := uuid.New()
	inner := &cacheTestStore{}
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := cached.Get(ctx, orgID, policyID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get %d: expected ErrNotFound, got %v", i, err)
		}
	}
	if inner.getCalls != 2 {
		t.Fatalf("not-found policy was cached: %d calls", inner.getCalls)
	}
}

func TestCachedStoreKeepsTenantsAndItemsSeparate(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	inner := &cacheTestStore{}
	policyA := seedPolicy(inner, orgA, "A")
	policyB := seedPolicy(inner, orgB, "B")
	cached := NewCachedStore(inner, platformcache.NewMemory(), time.Minute)
	ctx := context.Background()

	listA, _ := cached.List(ctx, orgA)
	listB, _ := cached.List(ctx, orgB)
	if len(listA) != 1 || listA[0].ID != policyA.ID {
		t.Fatalf("org A list: %+v", listA)
	}
	if len(listB) != 1 || listB[0].ID != policyB.ID {
		t.Fatalf("org B list: %+v", listB)
	}
	if _, err := cached.Get(ctx, orgA, policyB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get: expected ErrNotFound, got %v", err)
	}
	if inner.listCalls != 2 || inner.getCalls != 1 {
		t.Fatalf("calls: list=%d get=%d", inner.listCalls, inner.getCalls)
	}
}

func TestCachedStoreCorruptEntriesFallBack(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, time.Minute)
	ctx := context.Background()

	listKey, _ := platformcache.SLAPoliciesKey(orgID)
	itemKey, _ := platformcache.SLAPolicyKey(orgID, policy.ID)
	if err := memory.Set(ctx, listKey, []byte("not-json"), time.Minute); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	if err := memory.Set(ctx, itemKey, []byte("not-json"), time.Minute); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if inner.listCalls != 1 || inner.getCalls != 1 {
		t.Fatalf("corrupt entries did not fall back")
	}

	// The second read must come from the refreshed entries.
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("second list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if inner.listCalls != 1 || inner.getCalls != 1 {
		t.Fatalf("corrupt entries were not refreshed")
	}
}

func TestCachedStoreTTLExpiry(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 30*time.Second)
	ctx := context.Background()

	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	memory.Advance(31 * time.Second)
	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("second list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if inner.listCalls != 2 || inner.getCalls != 2 {
		t.Fatalf("TTL did not expire: list=%d get=%d", inner.listCalls, inner.getCalls)
	}
}

func TestCachedStoreFailuresFallBack(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	cached := NewCachedStore(inner, slaFailingCache{
		getErr: errors.New("redis down"),
		setErr: errors.New("redis down"),
		delErr: errors.New("redis down"),
	}, time.Minute)
	ctx := context.Background()

	if _, err := cached.List(ctx, orgID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if inner.listCalls != 1 || inner.getCalls != 1 {
		t.Fatalf("database calls: list=%d get=%d", inner.listCalls, inner.getCalls)
	}

	if _, err := cached.Create(ctx, orgID, Policy{Name: "High", Priority: "high", FirstResponseMinutes: 1, ResolutionMinutes: 2}); err != nil {
		t.Fatalf("create must ignore cache delete failure: %v", err)
	}
	if _, err := cached.Update(ctx, orgID, policy); err != nil {
		t.Fatalf("update must ignore cache delete failure: %v", err)
	}
	if err := cached.Delete(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("delete must ignore cache delete failure: %v", err)
	}
}

func TestCachedStoreZeroTTLIsPassThrough(t *testing.T) {
	orgID := uuid.New()
	inner := &cacheTestStore{}
	policy := seedPolicy(inner, orgID, "Normal")
	memory := platformcache.NewMemory()
	cached := NewCachedStore(inner, memory, 0)
	ctx := context.Background()

	_, _ = cached.List(ctx, orgID)
	_, _ = cached.List(ctx, orgID)
	_, _ = cached.Get(ctx, orgID, policy.ID)
	_, _ = cached.Get(ctx, orgID, policy.ID)

	if inner.listCalls != 2 || inner.getCalls != 2 {
		t.Fatalf("database calls: list=%d get=%d", inner.listCalls, inner.getCalls)
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

	policy, err := cached.Create(ctx, orgID, Policy{
		Name:                 "Normal",
		Priority:             "normal",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    120,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	listKey, _ := platformcache.SLAPoliciesKey(orgID)
	itemKey, _ := platformcache.SLAPolicyKey(orgID, policy.ID)

	firstList, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !mr.Exists(listKey) {
		t.Fatal("list was not cached")
	}
	firstItem, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !mr.Exists(itemKey) {
		t.Fatal("item was not cached")
	}

	// Change the row behind the cache; cached reads must not see it.
	if _, err := pool.Exec(ctx,
		`UPDATE sla_policies SET name = 'Direct update' WHERE org_id = $1 AND id = $2`,
		orgID, policy.ID,
	); err != nil {
		t.Fatalf("direct update: %v", err)
	}
	secondList, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("cached list: %v", err)
	}
	secondItem, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if secondList[0].Name != firstList[0].Name || secondItem.Name != firstItem.Name {
		t.Fatalf("read bypassed the cache: %+v / %+v", secondList, secondItem)
	}

	policy.Name = "Updated"
	if _, err := cached.Update(ctx, orgID, policy); err != nil {
		t.Fatalf("update: %v", err)
	}
	if mr.Exists(listKey) || mr.Exists(itemKey) {
		t.Fatal("update did not invalidate both caches")
	}

	reloaded, err := cached.Get(ctx, orgID, policy.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if reloaded.Name != "Updated" {
		t.Fatalf("stale item after update: %+v", reloaded)
	}

	if err := cached.Delete(ctx, orgID, policy.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if mr.Exists(listKey) || mr.Exists(itemKey) {
		t.Fatal("delete did not invalidate both caches")
	}
	if _, err := cached.Get(ctx, orgID, policy.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	empty, err := cached.List(ctx, orgID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("policies after delete: %+v", empty)
	}

	// Sanity check that the cache payloads round-trip the same values the
	// database returns.
	rawList, ok, err := redisCache.Get(ctx, listKey)
	if err != nil || !ok {
		t.Fatalf("read list cache: ok=%v err=%v", ok, err)
	}
	var cachedList []Policy
	if err := json.Unmarshal(rawList, &cachedList); err != nil {
		t.Fatalf("decode list cache: %v", err)
	}
	dbList, err := repo.List(ctx, orgID)
	if err != nil {
		t.Fatalf("database list: %v", err)
	}
	if len(cachedList) != len(dbList) {
		t.Fatalf("cached list %+v, database %+v", cachedList, dbList)
	}
}
