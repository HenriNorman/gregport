package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	var orgID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('Idempotency Org', $1) RETURNING id`,
		"idem-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

func countKeys(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE org_id = $1`, orgID,
	).Scan(&count); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	return count
}

func waitExpired(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, key string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var expired bool
		if err := pool.QueryRow(context.Background(),
			`SELECT expires_at <= now() FROM idempotency_keys WHERE org_id = $1 AND key = $2`,
			orgID, key,
		).Scan(&expired); err != nil {
			t.Fatalf("check expiry: %v", err)
		}
		if expired {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("key %q did not expire", key)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode stored body %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected body %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("body: %s, want %s", got, want)
	}
}

func TestAcquireNewKeyCreatesPendingRecord(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()
	before := time.Now()

	result, err := repo.Acquire(ctx, orgID, "create-ticket-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if result.Outcome != OutcomeAcquired {
		t.Fatalf("outcome: %s", result.Outcome)
	}
	record := result.Record
	if record.RequestHash != "hash-a" {
		t.Fatalf("request hash: %q", record.RequestHash)
	}
	if record.Completed() {
		t.Fatalf("new record is already completed: %+v", record)
	}
	if record.ResponseBody != nil {
		t.Fatalf("pending body: %q", record.ResponseBody)
	}
	if record.CreatedAt.Before(before.Add(-2 * time.Second)) {
		t.Fatalf("created at: %s", record.CreatedAt)
	}
	if record.ExpiresAt.Before(before.Add(time.Minute - 2*time.Second)) {
		t.Fatalf("expires at: %s", record.ExpiresAt)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("stored keys: %d", count)
	}
}

func TestAcquireSameKeyWhilePendingReturnsPending(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "pending-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	result, err := repo.Acquire(ctx, orgID, "pending-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.Outcome != OutcomePending {
		t.Fatalf("outcome: %s", result.Outcome)
	}
	if result.Record.RequestHash != "hash-a" || result.Record.Completed() {
		t.Fatalf("record: %+v", result.Record)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("stored keys: %d", count)
	}
}

func TestCompleteThenAcquireReplaysResponse(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()
	body := []byte(`{"id":"ticket-1","number":42,"nested":{"ok":true}}`)

	if _, err := repo.Acquire(ctx, orgID, "replay-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "replay-1", "hash-a", 201, body); err != nil {
		t.Fatalf("complete: %v", err)
	}

	result, err := repo.Acquire(ctx, orgID, "replay-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("replay acquire: %v", err)
	}
	if result.Outcome != OutcomeReplay {
		t.Fatalf("outcome: %s", result.Outcome)
	}
	if result.Record.ResponseStatus != 201 {
		t.Fatalf("status: %d", result.Record.ResponseStatus)
	}
	assertJSONEqual(t, result.Record.ResponseBody, string(body))
	if !result.Record.Completed() {
		t.Fatal("replay record is not marked complete")
	}

	// The expiry of a completed record is unchanged by Complete.
	if result.Record.ExpiresAt.IsZero() {
		t.Fatal("replay has no expiry")
	}
}

func TestAcquireDifferentHashConflicts(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "conflict-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	pending, err := repo.Acquire(ctx, orgID, "conflict-1", "hash-b", time.Minute)
	if err != nil {
		t.Fatalf("pending conflict: %v", err)
	}
	if pending.Outcome != OutcomeConflict {
		t.Fatalf("pending outcome: %s", pending.Outcome)
	}

	if err := repo.Complete(ctx, orgID, "conflict-1", "hash-a", 200, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	completed, err := repo.Acquire(ctx, orgID, "conflict-1", "hash-b", time.Minute)
	if err != nil {
		t.Fatalf("completed conflict: %v", err)
	}
	if completed.Outcome != OutcomeConflict {
		t.Fatalf("completed outcome: %s", completed.Outcome)
	}
	if completed.Record.ResponseStatus != 200 {
		t.Fatalf("conflict response leaked a status: %+v", completed.Record)
	}
}

func TestAcquireExpiredKeyIsReplaced(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	first, err := repo.Acquire(ctx, orgID, "expired-1", "hash-old", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "expired-1", "hash-old", 201, []byte(`{"old":true}`)); err != nil {
		t.Fatalf("complete old: %v", err)
	}
	waitExpired(t, pool, orgID, "expired-1")

	second, err := repo.Acquire(ctx, orgID, "expired-1", "hash-new", time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second.Outcome != OutcomeAcquired {
		t.Fatalf("outcome: %s", second.Outcome)
	}
	if second.Record.RequestHash != "hash-new" || second.Record.Completed() {
		t.Fatalf("replaced record: %+v", second.Record)
	}
	if second.Record.CreatedAt.Before(first.Record.CreatedAt) {
		t.Fatalf("created_at went backwards: %s -> %s", first.Record.CreatedAt, second.Record.CreatedAt)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("expired replacement left %d rows", count)
	}

	// The old hash is gone, so using it now is a fresh conflict against the
	// new owner.
	conflict, err := repo.Acquire(ctx, orgID, "expired-1", "hash-old", time.Minute)
	if err != nil {
		t.Fatalf("old hash acquire: %v", err)
	}
	if conflict.Outcome != OutcomeConflict {
		t.Fatalf("old hash outcome: %s", conflict.Outcome)
	}
}

func TestAcquireScopesKeysPerTenant(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	ctx := context.Background()

	a, err := repo.Acquire(ctx, orgA, "same-key", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	b, err := repo.Acquire(ctx, orgB, "same-key", "hash-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	if a.Outcome != OutcomeAcquired || b.Outcome != OutcomeAcquired {
		t.Fatalf("outcomes: A=%s B=%s", a.Outcome, b.Outcome)
	}

	if err := repo.Complete(ctx, orgA, "same-key", "hash-a", 201, []byte(`{"org":"a"}`)); err != nil {
		t.Fatalf("complete A: %v", err)
	}
	replayA, err := repo.Acquire(ctx, orgA, "same-key", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("replay A: %v", err)
	}
	if replayA.Outcome != OutcomeReplay {
		t.Fatalf("A outcome: %s", replayA.Outcome)
	}
	pendingB, err := repo.Acquire(ctx, orgB, "same-key", "hash-b", time.Minute)
	if err != nil {
		t.Fatalf("pending B: %v", err)
	}
	if pendingB.Outcome != OutcomePending {
		t.Fatalf("B outcome: %s", pendingB.Outcome)
	}

	if countA := countKeys(t, pool, orgA); countA != 1 {
		t.Fatalf("org A keys: %d", countA)
	}
	if countB := countKeys(t, pool, orgB); countB != 1 {
		t.Fatalf("org B keys: %d", countB)
	}
}

func TestCompleteRequiresTheOwningHash(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "owner-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "owner-1", "hash-b", 200, []byte(`{"ok":true}`)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("wrong hash: expected ErrNotPending, got %v", err)
	}

	result, err := repo.Acquire(ctx, orgID, "owner-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire after failed complete: %v", err)
	}
	if result.Outcome != OutcomePending {
		t.Fatalf("record changed after failed complete: %s", result.Outcome)
	}
}

func TestCompleteOnlyOnce(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "once-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "once-1", "hash-a", 201, []byte(`{"first":true}`)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "once-1", "hash-a", 500, []byte(`{"second":true}`)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second complete: expected ErrNotPending, got %v", err)
	}

	result, err := repo.Acquire(ctx, orgID, "once-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Record.ResponseStatus != 201 {
		t.Fatalf("status: %d", result.Record.ResponseStatus)
	}
	assertJSONEqual(t, result.Record.ResponseBody, `{"first":true}`)
}

func TestCompleteAfterExpiryFails(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "expired-complete", "hash-a", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	waitExpired(t, pool, orgID, "expired-complete")

	if err := repo.Complete(ctx, orgID, "expired-complete", "hash-a", 200, []byte(`{"ok":true}`)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ErrNotPending, got %v", err)
	}
}

func TestReleaseAllowsRetry(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "retry-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Release(ctx, orgID, "retry-1", "hash-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if count := countKeys(t, pool, orgID); count != 0 {
		t.Fatalf("released key still stored: %d", count)
	}

	result, err := repo.Acquire(ctx, orgID, "retry-1", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.Outcome != OutcomeAcquired {
		t.Fatalf("outcome: %s", result.Outcome)
	}
}

func TestReleaseIgnoresWrongHashAndCompletedRecords(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "release-1", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Release(ctx, orgID, "release-1", "hash-b"); err != nil {
		t.Fatalf("wrong-hash release: %v", err)
	}
	pending, err := repo.Acquire(ctx, orgID, "release-1", "hash-a", time.Minute)
	if err != nil || pending.Outcome != OutcomePending {
		t.Fatalf("pending after wrong-hash release: %s err=%v", pending.Outcome, err)
	}

	if err := repo.Complete(ctx, orgID, "release-1", "hash-a", 201, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := repo.Release(ctx, orgID, "release-1", "hash-a"); err != nil {
		t.Fatalf("completed release: %v", err)
	}
	replay, err := repo.Acquire(ctx, orgID, "release-1", "hash-a", time.Minute)
	if err != nil || replay.Outcome != OutcomeReplay {
		t.Fatalf("completed record was released: %s err=%v", replay.Outcome, err)
	}
}

func TestPurgeExpiredRemovesOnlyExpiredRecords(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "purge-expired", "hash-a", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire expired: %v", err)
	}
	if _, err := repo.Acquire(ctx, orgID, "purge-live", "hash-b", time.Minute); err != nil {
		t.Fatalf("acquire live: %v", err)
	}
	waitExpired(t, pool, orgID, "purge-expired")

	deleted, err := repo.PurgeExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted: %d, want 1", deleted)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("remaining keys: %d", count)
	}

	live, err := repo.Acquire(ctx, orgID, "purge-live", "hash-b", time.Minute)
	if err != nil || live.Outcome != OutcomePending {
		t.Fatalf("live record was purged: %s err=%v", live.Outcome, err)
	}
}

func TestCompleteEmptyBody(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "empty-body", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Complete(ctx, orgID, "empty-body", "hash-a", 204, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	result, err := repo.Acquire(ctx, orgID, "empty-body", "hash-a", time.Minute)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Outcome != OutcomeReplay {
		t.Fatalf("outcome: %s", result.Outcome)
	}
	if result.Record.ResponseStatus != 204 || len(result.Record.ResponseBody) != 0 {
		t.Fatalf("record: %+v", result.Record)
	}
}

func TestValidationRejectsBadInput(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	longKey := strings.Repeat("k", MaxKeyLength+1)
	longHash := strings.Repeat("h", MaxRequestHashLength+1)

	acquireCases := []struct {
		name string
		org  uuid.UUID
		key  string
		hash string
		ttl  time.Duration
	}{
		{"missing org", uuid.Nil, "key", "hash", time.Minute},
		{"empty key", orgID, "", "hash", time.Minute},
		{"whitespace key", orgID, "   ", "hash", time.Minute},
		{"long key", orgID, longKey, "hash", time.Minute},
		{"empty hash", orgID, "key", "", time.Minute},
		{"whitespace hash", orgID, "key", "  ", time.Minute},
		{"long hash", orgID, "key", longHash, time.Minute},
		{"zero ttl", orgID, "key", "hash", 0},
		{"negative ttl", orgID, "key", "hash", -time.Second},
	}
	for _, tt := range acquireCases {
		t.Run("acquire/"+tt.name, func(t *testing.T) {
			if _, err := repo.Acquire(ctx, tt.org, tt.key, tt.hash, tt.ttl); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}

	completeCases := []struct {
		name   string
		org    uuid.UUID
		key    string
		hash   string
		status int
		body   []byte
	}{
		{"missing org", uuid.Nil, "key", "hash", 200, nil},
		{"empty key", orgID, "", "hash", 200, nil},
		{"empty hash", orgID, "key", "", 200, nil},
		{"status too low", orgID, "key", "hash", 99, nil},
		{"status too high", orgID, "key", "hash", 600, nil},
		{"invalid body", orgID, "key", "hash", 200, []byte(`not-json`)},
	}
	for _, tt := range completeCases {
		t.Run("complete/"+tt.name, func(t *testing.T) {
			if err := repo.Complete(ctx, tt.org, tt.key, tt.hash, tt.status, tt.body); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}

	releaseCases := []struct {
		name string
		org  uuid.UUID
		key  string
		hash string
	}{
		{"missing org", uuid.Nil, "key", "hash"},
		{"empty key", orgID, "", "hash"},
		{"empty hash", orgID, "key", ""},
		{"long key", orgID, longKey, "hash"},
	}
	for _, tt := range releaseCases {
		t.Run("release/"+tt.name, func(t *testing.T) {
			if err := repo.Release(ctx, tt.org, tt.key, tt.hash); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}

	if _, err := repo.PurgeExpired(ctx, time.Time{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero purge time: expected ErrInvalidInput, got %v", err)
	}
	if count := countKeys(t, pool, orgID); count != 0 {
		t.Fatalf("invalid input wrote %d rows", count)
	}
}

func TestConcurrentAcquireSameHashOnlyOneOwner(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	const workers = 16
	results := make(chan AcquireResult, workers)
	errs := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.Acquire(ctx, orgID, "race-1", "hash-a", time.Minute)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent acquire: %v", err)
	}

	acquired, pending := 0, 0
	for result := range results {
		switch result.Outcome {
		case OutcomeAcquired:
			acquired++
		case OutcomePending:
			pending++
		default:
			t.Fatalf("unexpected outcome: %s", result.Outcome)
		}
	}
	if acquired != 1 || pending != workers-1 {
		t.Fatalf("acquired=%d pending=%d", acquired, pending)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("stored keys: %d", count)
	}
}

func TestConcurrentAcquireDifferentHashesOnlyOneWins(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	const workers = 12
	results := make(chan AcquireResult, workers)
	errs := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		hash := fmt.Sprintf("hash-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.Acquire(ctx, orgID, "race-2", hash, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent acquire: %v", err)
	}

	acquired, conflicts := 0, 0
	for result := range results {
		switch result.Outcome {
		case OutcomeAcquired:
			acquired++
		case OutcomeConflict:
			conflicts++
		default:
			t.Fatalf("unexpected outcome: %s", result.Outcome)
		}
	}
	if acquired != 1 || conflicts != workers-1 {
		t.Fatalf("acquired=%d conflicts=%d", acquired, conflicts)
	}
}

func TestConcurrentCompleteOnlyOneWriter(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "race-complete", "hash-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	const workers = 12
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		body := []byte(fmt.Sprintf(`{"writer":%d}`, i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.Complete(ctx, orgID, "race-complete", "hash-a", 201, body)
		}()
	}
	wg.Wait()
	close(errs)

	completed, rejected := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			completed++
		case errors.Is(err, ErrNotPending):
			rejected++
		default:
			t.Fatalf("unexpected complete error: %v", err)
		}
	}
	if completed != 1 || rejected != workers-1 {
		t.Fatalf("completed=%d rejected=%d", completed, rejected)
	}

	result, err := repo.Acquire(ctx, orgID, "race-complete", "hash-a", time.Minute)
	if err != nil || result.Outcome != OutcomeReplay {
		t.Fatalf("replay: %s err=%v", result.Outcome, err)
	}
	if result.Record.ResponseStatus != 201 || !json.Valid(result.Record.ResponseBody) {
		t.Fatalf("stored response: %+v", result.Record)
	}
}

func TestConcurrentAcquireAfterExpiryOnlyOneOwner(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := seedOrg(t, pool)
	ctx := context.Background()

	if _, err := repo.Acquire(ctx, orgID, "race-expired", "hash-old", 20*time.Millisecond); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	waitExpired(t, pool, orgID, "race-expired")

	const workers = 8
	results := make(chan AcquireResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		hash := fmt.Sprintf("new-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.Acquire(ctx, orgID, "race-expired", hash, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent acquire: %v", err)
	}

	acquired := 0
	for result := range results {
		if result.Outcome == OutcomeAcquired {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired: %d, want 1", acquired)
	}
	if count := countKeys(t, pool, orgID); count != 1 {
		t.Fatalf("expired race left %d rows", count)
	}
}
