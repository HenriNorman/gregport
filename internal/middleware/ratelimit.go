package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/clientip"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/metrics"
	"github.com/HenriNorman/gregport/internal/platform/ratelimit"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// RateLimitConfig configures the authenticated limiter.
type RateLimitConfig struct {
	Limiter  ratelimit.Limiter
	Limit    int
	Window   time.Duration
	Counters *metrics.Counters
	Proxy    clientip.Config
}

// RateLimit limits authenticated requests per API key or per user.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	return limitRequests(cfg, func(r *http.Request) (string, error) {
		p, ok := requestctx.FromContext(r.Context())
		if !ok {
			return "", errNoPrincipal
		}
		if p.Kind == requestctx.KindAPIKey {
			return cache.APIKeyRateLimitKey(p.OrgID, p.ActorID)
		}
		return cache.UserRateLimitKey(p.OrgID, p.ActorID)
	})
}

// PublicRateLimit limits unauthenticated requests by client IP.
func PublicRateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	return limitRequests(cfg, func(r *http.Request) (string, error) {
		return cache.PublicIPRateLimitKey(clientip.FromRequest(r, cfg.Proxy)), nil
	})
}

// ScopedPublicRateLimit is PublicRateLimit with a dedicated bucket. Use it when
// one public endpoint needs a much tighter budget than the rest, so its window
// does not share the generic per-IP log.
func ScopedPublicRateLimit(cfg RateLimitConfig, scope string) func(http.Handler) http.Handler {
	return limitRequests(cfg, func(r *http.Request) (string, error) {
		ip := clientip.FromRequest(r, cfg.Proxy)
		return cache.ScopedPublicIPRateLimitKey(scope, ip), nil
	})
}

type keyFunc func(*http.Request) (string, error)

type noPrincipalError struct{}

func (noPrincipalError) Error() string { return "ratelimit: no authenticated principal" }

var errNoPrincipal = noPrincipalError{}

func limitRequests(cfg RateLimitConfig, keyFn keyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Limiter == nil || cfg.Limit <= 0 || cfg.Window <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, err := keyFn(r)
			if err != nil {
				logging.FromContext(r.Context()).Error("rate limit identity failed", "error", err)
				next.ServeHTTP(w, r)
				return
			}

			result, err := cfg.Limiter.Allow(r.Context(), key, cfg.Limit, cfg.Window)
			if err != nil {
				recordRateLimitError(cfg.Counters, r, err)
				next.ServeHTTP(w, r)
				return
			}

			setRateLimitHeaders(w, result)
			if !result.Allowed {
				recordRejection(cfg.Counters)
				writeRateLimited(w, r, result)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LoginRateLimitConfig configures the login-specific limiter. Fallback is
// used when Redis is unavailable and applies a stricter limit.
type LoginRateLimitConfig struct {
	Limiter  ratelimit.Limiter
	Fallback ratelimit.Limiter
	Limit    int
	Window   time.Duration
	Counters *metrics.Counters
	Proxy    clientip.Config
}

// LoginRateLimit keys login attempts by client IP plus a hash of the submitted
// email. The request body is buffered and restored so the handler still sees it.
func LoginRateLimit(cfg LoginRateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Limiter == nil || cfg.Limit <= 0 || cfg.Window <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))

			ip := clientip.FromRequest(r, cfg.Proxy)
			key := loginKey(ip, body, readErr)

			result, err := cfg.Limiter.Allow(r.Context(), key, cfg.Limit, cfg.Window)
			degraded := false
			if err != nil {
				recordRateLimitError(cfg.Counters, r, err)
				if cfg.Fallback == nil {
					httpx.WriteError(w, r, &httpx.APIError{
						Status:  http.StatusServiceUnavailable,
						Code:    "rate_limit_unavailable",
						Message: "login is temporarily unavailable",
					})
					return
				}

				fallbackLimit := cfg.Limit / 2
				if fallbackLimit < 1 {
					fallbackLimit = 1
				}
				result, err = cfg.Fallback.Allow(r.Context(), key, fallbackLimit, cfg.Window)
				if err != nil {
					httpx.WriteError(w, r, &httpx.APIError{
						Status:  http.StatusServiceUnavailable,
						Code:    "rate_limit_unavailable",
						Message: "login is temporarily unavailable",
					})
					return
				}
				degraded = true
			}

			setRateLimitHeaders(w, result)
			if degraded {
				w.Header().Set("X-RateLimit-Degraded", "1")
			}
			if !result.Allowed {
				recordRejection(cfg.Counters)
				writeRateLimited(w, r, result)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func loginKey(ip string, body []byte, readErr error) string {
	if readErr == nil {
		var payload struct {
			Email string `json:"email"`
		}
		if err := json.Unmarshal(body, &payload); err == nil {
			email := strings.ToLower(strings.TrimSpace(payload.Email))
			if email != "" {
				sum := sha256.Sum256([]byte(email))
				return cache.LoginRateLimitKey(ip, hex.EncodeToString(sum[:]))
			}
		}
	}
	return cache.LoginIPRateLimitKey(ip)
}

func setRateLimitHeaders(w http.ResponseWriter, result ratelimit.Result) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.Reset.Unix(), 10))
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, result ratelimit.Result) {
	seconds := int(result.RetryAfter.Seconds())
	if result.RetryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	httpx.WriteError(w, r, &httpx.APIError{
		Status:  http.StatusTooManyRequests,
		Code:    "rate_limited",
		Message: "too many requests",
	})
}

func recordRejection(counters *metrics.Counters) {
	if counters != nil {
		counters.RateLimitRejected()
	}
}

func recordRateLimitError(counters *metrics.Counters, r *http.Request, err error) {
	if counters != nil {
		counters.RateLimitError()
	}
	logging.FromContext(r.Context()).Warn("rate limiter unavailable", "error", err)
}
