package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/idempotency"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/metrics"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
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
		`INSERT INTO organizations (name, slug) VALUES ('Idempotency Middleware Org', $1) RETURNING id`,
		"idem-mw-"+uuid.NewString()[:8],
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

// fakeStore mimics the PostgreSQL idempotency store for fast unit tests. It
// tracks pending and completed records per (org, key) and enforces the same
// state transitions as the real store.
type fakeStore struct {
	mu          sync.Mutex
	records     map[string]idempotency.Record // key: orgID:string + ":" + key
	acquireErr  error
	completeErr error
	releaseErr  error

	acquires  int
	completes int
	releases  int
}

func (f *fakeStore) key(orgID uuid.UUID, key string) string { return orgID.String() + ":" + key }

func (f *fakeStore) Acquire(ctx context.Context, orgID uuid.UUID, key, requestHash string, ttl time.Duration) (idempotency.AcquireResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++

	if f.acquireErr != nil {
		return idempotency.AcquireResult{}, f.acquireErr
	}

	k := f.key(orgID, key)
	now := time.Now()
	rec, exists := f.records[k]
	if !exists {
		f.records[k] = idempotency.Record{
			RequestHash: requestHash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(ttl),
		}
		return idempotency.AcquireResult{Outcome: idempotency.OutcomeAcquired, Record: f.records[k]}, nil
	}

	if rec.ExpiresAt.Before(now) {
		f.records[k] = idempotency.Record{
			RequestHash: requestHash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(ttl),
		}
		return idempotency.AcquireResult{Outcome: idempotency.OutcomeAcquired, Record: f.records[k]}, nil
	}

	switch {
	case rec.RequestHash != requestHash:
		return idempotency.AcquireResult{Outcome: idempotency.OutcomeConflict, Record: rec}, nil
	case !rec.Completed():
		return idempotency.AcquireResult{Outcome: idempotency.OutcomePending, Record: rec}, nil
	default:
		return idempotency.AcquireResult{Outcome: idempotency.OutcomeReplay, Record: rec}, nil
	}
}

func (f *fakeStore) Complete(ctx context.Context, orgID uuid.UUID, key, requestHash string, status int, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completes++

	if f.completeErr != nil {
		return f.completeErr
	}

	rec, ok := f.records[f.key(orgID, key)]
	if !ok || rec.ExpiresAt.Before(time.Now()) || rec.RequestHash != requestHash || rec.Completed() {
		return idempotency.ErrNotPending
	}
	rec.ResponseStatus = status
	rec.ResponseBody = body
	f.records[f.key(orgID, key)] = rec
	return nil
}

func (f *fakeStore) Release(ctx context.Context, orgID uuid.UUID, key, requestHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++

	if f.releaseErr != nil {
		return f.releaseErr
	}

	rec, ok := f.records[f.key(orgID, key)]
	if !ok || rec.ExpiresAt.Before(time.Now()) || rec.RequestHash != requestHash || rec.Completed() {
		return nil
	}
	delete(f.records, f.key(orgID, key))
	return nil
}

func (f *fakeStore) PurgeExpired(ctx context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted int64
	for k, rec := range f.records {
		if !rec.ExpiresAt.Before(before) {
			continue
		}
		delete(f.records, k)
		deleted++
	}
	return deleted, nil
}

type captureHandler struct {
	mu       sync.Mutex
	calls    int
	status   int
	body     string
	err      error
	lastPath string
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.calls++
	h.lastPath = r.URL.Path
	h.mu.Unlock()

	if h.err != nil {
		httpx.WriteError(w, r, h.err)
		return
	}
	w.WriteHeader(h.status)
	if h.body != "" {
		_, _ = w.Write([]byte(h.body))
	}
}

func principalCtx(orgID uuid.UUID) context.Context {
	return requestctx.WithPrincipal(context.Background(), requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    "owner",
	})
}

func TestIdempotencyPassesThroughWithoutHeader(t *testing.T) {
	handler := &captureHandler{status: http.StatusCreated, body: `{"ok":true}`}
	mw := Idempotency(IdempotencyConfig{Store: &fakeStore{records: map[string]idempotency.Record{}}, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{"subject":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(uuid.New()))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d", rec.Code)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls: %d, want 1", handler.calls)
	}
}

func TestIdempotencyAcquiresAndPassesResponse(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	key := "idem-key-1"
	body := []byte(`{"subject":"x"}`)

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"number":42}` {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls: %d, want 1", handler.calls)
	}
	if store.completes != 1 {
		t.Fatalf("completes: %d, want 1", store.completes)
	}

	// Replay: same key and body returns the stored response without calling the handler again.
	req2 := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", key)
	req2 = req2.WithContext(principalCtx(orgID))
	rec2 := httptest.NewRecorder()
	next.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay status: %d", rec2.Code)
	}
	if rec2.Body.String() != `{"number":42}` {
		t.Fatalf("replay body: %s", rec2.Body.String())
	}
	if handler.calls != 1 {
		t.Fatalf("handler called again on replay: %d", handler.calls)
	}
	if store.acquires != 2 {
		t.Fatalf("acquires: %d, want 2", store.acquires)
	}
}

func TestIdempotencyRejectsDifferentBody(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	key := "idem-key-2"
	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{"subject":"a"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first status: %d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{"subject":"b"}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", key)
	req2 = req2.WithContext(principalCtx(orgID))
	rec2 := httptest.NewRecorder()
	next.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Fatalf("conflict status: %d body=%s", rec2.Code, rec2.Body.String())
	}
	if got := decodeErrorCode(t, rec2); got != "idempotency_conflict" {
		t.Fatalf("conflict code: %q", got)
	}
	if handler.calls != 1 {
		t.Fatalf("handler called on conflicting body: %d", handler.calls)
	}
}

func TestIdempotencyPendingWhileInFlight(t *testing.T) {
	orgID := uuid.New()
	// First request acquires and holds the key pending (handler never completes).
	store := &fakeStore{records: map[string]idempotency.Record{}}
	body := []byte(`{"subject":"x"}`)
	pendingHash := hashRequest(http.MethodPost, "/tickets", body, nil)
	if _, err := store.Acquire(context.Background(), orgID, "idem-key-3", pendingHash, time.Hour); err != nil {
		t.Fatalf("pre-acquire: %v", err)
	}

	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-key-3")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("pending status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeErrorCode(t, rec); got != "idempotency_pending" {
		t.Fatalf("pending code: %q", got)
	}
	if handler.calls != 0 {
		t.Fatalf("handler called while pending: %d", handler.calls)
	}
}

func TestIdempotencyKeysAreTenantScoped(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	key := "idem-key-shared"
	body := []byte(`{"subject":"x"}`)

	makeReq := func(org uuid.UUID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req = req.WithContext(principalCtx(org))
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, req)
		return rec
	}

	if recA := makeReq(orgA); recA.Code != http.StatusCreated {
		t.Fatalf("org A status: %d", recA.Code)
	}
	// Same key in org B is independent.
	if recB := makeReq(orgB); recB.Code != http.StatusCreated {
		t.Fatalf("org B status: %d", recB.Code)
	}
	if handler.calls != 2 {
		t.Fatalf("handler calls: %d, want 2", handler.calls)
	}
}

func TestIdempotencyExpiredKeyCanBeReused(t *testing.T) {
	orgID := uuid.New()
	store := &fakeStore{records: map[string]idempotency.Record{}}
	// Seed an expired record manually.
	store.records[orgID.String()+":idem-key-exp"] = idempotency.Record{
		RequestHash:    "hash-old",
		CreatedAt:      time.Now().Add(-time.Hour),
		ExpiresAt:      time.Now().Add(-time.Second),
		ResponseStatus: 200,
		ResponseBody:   []byte(`{"old":true}`),
	}

	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{"subject":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-key-exp")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d", rec.Code)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls: %d, want 1 (expired key should be reused)", handler.calls)
	}
}

func TestIdempotencyRequiresPrincipal(t *testing.T) {
	handler := &captureHandler{status: http.StatusCreated}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Idempotency-Key", "idem-key-no-principal")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeErrorCode(t, rec); got != "unauthorized" {
		t.Fatalf("code: %q", got)
	}
	if handler.calls != 0 {
		t.Fatalf("handler called without principal: %d", handler.calls)
	}
}

func TestIdempotencyRequiresTTL(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{status: http.StatusCreated}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: 0})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Idempotency-Key", "idem-key-no-ttl")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}
	if handler.calls != 0 {
		t.Fatalf("handler called without ttl: %d", handler.calls)
	}
}

func TestIdempotencyCapturesHandlerErrorResponse(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{err: httpx.NotFound("ticket_not_found", "ticket not found")}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	key := "idem-key-err"
	body := []byte(`{"subject":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if store.completes != 1 {
		t.Fatalf("completes: %d, want 1", store.completes)
	}

	// Replay returns the stored 404.
	req2 := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", key)
	req2 = req2.WithContext(principalCtx(orgID))
	rec2 := httptest.NewRecorder()
	next.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotFound {
		t.Fatalf("replay status: %d", rec2.Code)
	}
	if handler.calls != 1 {
		t.Fatalf("handler called again on replay of error: %d", handler.calls)
	}
}

func TestIdempotencyStoreErrorReturns500(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{status: http.StatusCreated}
	store := &fakeStore{records: map[string]idempotency.Record{}, acquireErr: errors.New("db down")}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour, Counters: metrics.New()})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Idempotency-Key", "idem-key-store-err")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if handler.calls != 0 {
		t.Fatalf("handler called when store failed: %d", handler.calls)
	}
}

func TestIdempotencyPanicReleasesKey(t *testing.T) {
	orgID := uuid.New()
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Idempotency-Key", "idem-key-panic")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()

	func() {
		defer func() { _ = recover() }()
		next.ServeHTTP(rec, req)
	}()

	if store.releases != 1 {
		t.Fatalf("releases: %d, want 1 (key must be released after panic)", store.releases)
	}
}

func TestIdempotencyBodyReadErrorStillWorks(t *testing.T) {
	orgID := uuid.New()
	handler := &captureHandler{status: http.StatusCreated}
	store := &fakeStore{records: map[string]idempotency.Record{}}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Hour})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	req := httptest.NewRequest(http.MethodPost, "/tickets", nil)
	req.Header.Set("Idempotency-Key", "idem-key-read-err")
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Error.Code
}

// Integration test against the real PostgreSQL-backed store.
func TestIdempotencyMiddlewareWithPostgres(t *testing.T) {
	pool := testPool(t)
	orgID := seedOrg(t, pool)
	store := idempotency.NewRepository(pool)

	handler := &captureHandler{status: http.StatusCreated, body: `{"number":42}`}
	mw := Idempotency(IdempotencyConfig{Store: store, TTL: time.Minute})
	next := mw(http.HandlerFunc(handler.ServeHTTP))

	key := "idem-key-pg"
	body := []byte(`{"subject":"x"}`)

	req := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req = req.WithContext(principalCtx(orgID))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls: %d, want 1", handler.calls)
	}

	// Replay against PostgreSQL: same key+body returns stored response.
	req2 := httptest.NewRequest(http.MethodPost, "/tickets", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", key)
	req2 = req2.WithContext(principalCtx(orgID))
	rec2 := httptest.NewRecorder()
	next.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay status: %d body=%s", rec2.Code, rec2.Body.String())
	}
	var replayBody any
	if err := json.Unmarshal(rec2.Body.Bytes(), &replayBody); err != nil {
		t.Fatalf("decode replay body: %v (body=%s)", err, rec2.Body.String())
	}
	var wantBody any
	if err := json.Unmarshal([]byte(`{"number":42}`), &wantBody); err != nil {
		t.Fatalf("decode want body: %v", err)
	}
	if !jsonEqual(replayBody, wantBody) {
		t.Fatalf("replay body: %s, want %s", rec2.Body.String(), `{"number":42}`)
	}
	if handler.calls != 1 {
		t.Fatalf("handler called again on postgres replay: %d", handler.calls)
	}
}

func jsonEqual(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}
