package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/HenriNorman/gregport/internal/platform/clientip"
	"github.com/HenriNorman/gregport/internal/platform/metrics"
	"github.com/HenriNorman/gregport/internal/platform/ratelimit"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func TestRateLimitAllowedAndDeniedHeaders(t *testing.T) {
	limiter := testRedisLimiter(t)
	cfg := RateLimitConfig{Limiter: limiter, Limit: 2, Window: time.Minute, Counters: metrics.New()}
	handler := RateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	p := requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   uuid.New(),
		Role:    "agent",
	}

	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		req := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), p)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status: %d", i, rec.Code)
		}
		if got := rec.Header().Get("X-RateLimit-Limit"); got != "2" {
			t.Fatalf("request %d limit header: %q", i, got)
		}
		wantRemaining := strconv.Itoa(2 - i)
		if got := rec.Header().Get("X-RateLimit-Remaining"); got != wantRemaining {
			t.Fatalf("request %d remaining header: %q, want %q", i, got, wantRemaining)
		}
		reset, err := strconv.ParseInt(rec.Header().Get("X-RateLimit-Reset"), 10, 64)
		if err != nil || reset < time.Now().Unix() {
			t.Fatalf("request %d reset header: %q err=%v", i, rec.Header().Get("X-RateLimit-Reset"), err)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), p))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: %d", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("remaining header: %q", got)
	}
	if got := rec.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Fatalf("retry-after header: %q", got)
	}
	assertRateLimitedEnvelope(t, rec)
}

func TestRateLimitSeparatesUsersAndAPIKeys(t *testing.T) {
	limiter := testRedisLimiter(t)
	cfg := RateLimitConfig{Limiter: limiter, Limit: 1, Window: time.Minute}
	handler := RateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	orgID := uuid.New()
	user := requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: orgID}
	apiKey := requestctx.Principal{Kind: requestctx.KindAPIKey, ActorID: uuid.New(), OrgID: orgID}

	for _, p := range []requestctx.Principal{user, apiKey} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), p))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s first request: %d", p.Kind, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), user))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("user second request: %d", rec.Code)
	}

	otherOrg := requestctx.Principal{Kind: requestctx.KindUser, ActorID: user.ActorID, OrgID: uuid.New()}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), otherOrg))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same user in another org shared the bucket: %d", rec.Code)
	}
}

func TestRateLimitFailsOpenWhenRedisIsDown(t *testing.T) {
	counters := metrics.New()
	cfg := RateLimitConfig{Limiter: errorLimiter{}, Limit: 1, Window: time.Minute, Counters: counters}
	called := false
	handler := RateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	p := requestctx.Principal{Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: uuid.New()}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), p))

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("request was not allowed: called=%v status=%d", called, rec.Code)
	}
	if got := counters.Snapshot().RateLimitErrors; got != 1 {
		t.Fatalf("rate limit errors: %d", got)
	}
}

func TestPublicRateLimitUsesForwardedAddressWhenTrusted(t *testing.T) {
	limiter := testRedisLimiter(t)
	cfg := RateLimitConfig{
		Limiter: limiter,
		Limit:   1,
		Window:  time.Minute,
		Proxy: clientip.Config{
			TrustProxyHeaders: true,
			TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		},
	}
	handler := PublicRateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func(forwarded string) int {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.5:1234"
		req.Header.Set("X-Forwarded-For", forwarded)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := request("203.0.113.9"); code != http.StatusNoContent {
		t.Fatalf("first request: %d", code)
	}
	if code := request("203.0.113.9"); code != http.StatusTooManyRequests {
		t.Fatalf("same client: %d", code)
	}
	if code := request("203.0.113.10"); code != http.StatusNoContent {
		t.Fatalf("different client: %d", code)
	}
}

func TestLoginRateLimitByIPAndEmail(t *testing.T) {
	limiter := testRedisLimiter(t)
	cfg := LoginRateLimitConfig{Limiter: limiter, Limit: 2, Window: time.Minute}
	handler := LoginRateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("handler could not read the restored body: %v", err)
		}
		if payload.Email == "" {
			t.Error("handler received an empty email")
		}
		w.WriteHeader(http.StatusOK)
	}))

	login := func(email, ip string) (int, http.Header) {
		body := `{"email":"` + email + `","password":"secret"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(body))
		req.RemoteAddr = ip + ":4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Header()
	}

	for i := 1; i <= 2; i++ {
		code, header := login("user@example.com", "203.0.113.9")
		if code != http.StatusOK {
			t.Fatalf("attempt %d: %d", i, code)
		}
		if header.Get("X-RateLimit-Limit") != "2" {
			t.Fatalf("attempt %d limit header: %q", i, header.Get("X-RateLimit-Limit"))
		}
	}

	code, header := login("user@example.com", "203.0.113.9")
	if code != http.StatusTooManyRequests {
		t.Fatalf("third attempt: %d", code)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}

	if code, _ := login("other@example.com", "203.0.113.9"); code != http.StatusOK {
		t.Fatalf("different email: %d", code)
	}
	if code, _ := login("user@example.com", "203.0.113.10"); code != http.StatusOK {
		t.Fatalf("different IP: %d", code)
	}
}

func TestLoginRateLimitFallsBackAndIsStricter(t *testing.T) {
	counters := metrics.New()
	cfg := LoginRateLimitConfig{
		Limiter:  errorLimiter{},
		Fallback: ratelimit.NewMemory(),
		Limit:    4,
		Window:   time.Minute,
		Counters: counters,
	}
	called := 0
	handler := LoginRateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))

	login := func() (int, http.Header) {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login",
			strings.NewReader(`{"email":"user@example.com"}`))
		req.RemoteAddr = "203.0.113.9:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Header()
	}

	// Redis is down, so the fallback limit is 2 (half of 4).
	for i := 1; i <= 2; i++ {
		code, header := login()
		if code != http.StatusOK {
			t.Fatalf("fallback attempt %d: %d", i, code)
		}
		if header.Get("X-RateLimit-Degraded") != "1" {
			t.Fatalf("fallback attempt %d did not report degradation", i)
		}
	}
	if code, _ := login(); code != http.StatusTooManyRequests {
		t.Fatalf("fallback third attempt: %d", code)
	}
	if called != 2 {
		t.Fatalf("handler called %d times, want 2", called)
	}
	if got := counters.Snapshot().RateLimitErrors; got != 3 {
		t.Fatalf("rate limit errors: %d", got)
	}
}

func TestLoginRateLimitUsesIPForMalformedBody(t *testing.T) {
	limiter := testRedisLimiter(t)
	cfg := LoginRateLimitConfig{Limiter: limiter, Limit: 1, Window: time.Minute}
	handler := LoginRateLimit(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))

	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader("not-json"))
		req.RemoteAddr = "203.0.113.9:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call(); code != http.StatusBadRequest {
		t.Fatalf("first malformed request: %d", code)
	}
	if code := call(); code != http.StatusTooManyRequests {
		t.Fatalf("second malformed request: %d", code)
	}
}

type errorLimiter struct{}

func (errorLimiter) Allow(context.Context, string, int, time.Duration) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("redis unavailable")
}

func testRedisLimiter(t *testing.T) *ratelimit.Redis {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return ratelimit.NewRedis(client)
}

func withPrincipal(r *http.Request, p requestctx.Principal) *http.Request {
	return r.WithContext(requestctx.WithPrincipal(r.Context(), p))
}

func assertRateLimitedEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error.Code != "rate_limited" {
		t.Fatalf("error code: %q", envelope.Error.Code)
	}
}
