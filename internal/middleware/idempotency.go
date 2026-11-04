// Package middleware provides the idempotency middleware. It works with the
// PostgreSQL idempotency store (not Redis) so a cache outage cannot replay or
// duplicate a write.
package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	idemstore "github.com/HenriNorman/gregport/internal/idempotency"
	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/metrics"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

const (
	idempotencyHeader = "Idempotency-Key"
	maxRequestBody    = 1 << 20 // 1 MiB, matches the JSON decoder limit
)

// IdempotencyConfig configures the idempotency middleware.
type IdempotencyConfig struct {
	// Store persists idempotency keys and captured responses.
	Store idemstore.Store
	// TTL is how long a key is retained. Must be positive.
	TTL time.Duration
	// Counters records idempotency outcomes.
	Counters *metrics.Counters
}

// Idempotency rejects duplicate or conflicting requests carrying an
// Idempotency-Key header, replays completed responses, and records pending
// requests so the caller owns the key until it completes or releases it.
//
// The middleware is method-agnostic; callers wire it only onto the unsafe
// (state-changing) routes that need it, typically POST endpoints.
func Idempotency(cfg IdempotencyConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			p, ok := requestctx.FromContext(r.Context())
			if !ok {
				httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
				return
			}

			if cfg.TTL <= 0 {
				recordIdempotencyError(cfg.Counters)
				httpx.WriteError(w, r, httpx.InternalError(errors.New("idempotency: ttl not configured")))
				return
			}

			body, readErr := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))

			requestHash := hashRequest(r.Method, r.URL.Path, body, readErr)

			result, err := cfg.Store.Acquire(r.Context(), p.OrgID, key, requestHash, cfg.TTL)
			if err != nil {
				recordIdempotencyError(cfg.Counters)
				logging.FromContext(r.Context()).Error("idempotency acquire failed", "error", err)
				httpx.WriteError(w, r, httpx.InternalError(err))
				return
			}

			switch result.Outcome {
			case idemstore.OutcomeReplay:
				metricsIdempotencyReplay(cfg.Counters)
				writeReplay(w, r, result.Record)
				return
			case idemstore.OutcomePending:
				metricsIdempotencyPending(cfg.Counters)
				httpx.WriteError(w, r, &httpx.APIError{
					Status:  http.StatusConflict,
					Code:    "idempotency_pending",
					Message: "request with this idempotency key is in progress",
				})
				return
			case idemstore.OutcomeConflict:
				metricsIdempotencyConflict(cfg.Counters)
				httpx.WriteError(w, r, &httpx.APIError{
					Status:  http.StatusConflict,
					Code:    "idempotency_conflict",
					Message: "idempotency key was already used with a different request body",
				})
				return
			}

			// OutcomeAcquired: run the handler and capture its response so it
			// can be stored for future replays.
			capture := &responseCapture{ResponseWriter: w}
			var panicValue any
			func() {
				defer func() {
					if pv := recover(); pv != nil {
						panicValue = pv
					}
				}()
				next.ServeHTTP(capture, r)
			}()

			status := capture.statusCode()
			bodyOut := capture.body.Bytes()
			if len(bodyOut) == 0 {
				bodyOut = nil
			}

			if panicValue != nil {
				// Do not store a response for a panicked request; release the
				// key so the client may retry, then re-panic for the recovery
				// middleware above to handle.
				_ = cfg.Store.Release(r.Context(), p.OrgID, key, requestHash)
				panic(panicValue)
			}

			if err := cfg.Store.Complete(r.Context(), p.OrgID, key, requestHash, status, bodyOut); err != nil && !errors.Is(err, idemstore.ErrNotPending) {
				recordIdempotencyError(cfg.Counters)
				logging.FromContext(r.Context()).Error("idempotency complete failed", "error", err)
			}
		})
	}
}

// responseCapture wraps a ResponseWriter so the middleware can store the
// status and body the handler produced for future replays.
type responseCapture struct {
	http.ResponseWriter
	status        int
	body          bytes.Buffer
	headerWritten bool
}

func (rc *responseCapture) WriteHeader(code int) {
	if rc.headerWritten {
		return
	}
	rc.status = code
	rc.headerWritten = true
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	if !rc.headerWritten {
		rc.WriteHeader(http.StatusOK)
	}
	rc.body.Write(b)
	return rc.ResponseWriter.Write(b)
}

func (rc *responseCapture) statusCode() int {
	if rc.status == 0 {
		return http.StatusOK
	}
	return rc.status
}

// writeReplay returns a previously captured response unchanged.
func writeReplay(w http.ResponseWriter, _ *http.Request, record idemstore.Record) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(record.ResponseStatus)
	if len(record.ResponseBody) > 0 {
		_, _ = w.Write(record.ResponseBody)
	}
}

func hashRequest(method, path string, body []byte, readErr error) string {
	if readErr != nil {
		body = nil
	}
	sum := sha256.Sum256(append(append([]byte(method+"\n"+path+"\n"), body...), '\n'))
	return hex.EncodeToString(sum[:])
}

func metricsIdempotencyReplay(c *metrics.Counters) {
	if c != nil {
		c.IdempotencyReplay()
	}
}

func metricsIdempotencyPending(c *metrics.Counters) {
	if c != nil {
		c.IdempotencyPending()
	}
}

func metricsIdempotencyConflict(c *metrics.Counters) {
	if c != nil {
		c.IdempotencyConflict()
	}
}

func recordIdempotencyError(c *metrics.Counters) {
	if c != nil {
		c.IdempotencyError()
	}
}
