package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// WithLogger derives a request-scoped logger carrying the request id, stores
// it in the context and installs the mutable RequestInfo holder used by the
// access logger.
func WithLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if base != nil {
				l := base.With("request_id", GetRequestID(r.Context()))
				r = r.WithContext(logging.WithLogger(r.Context(), l))
			}

			ctx, _ := requestctx.WithRequestInfo(r.Context())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestLogger writes one structured JSON line per request. It never logs
// bodies, query strings or raw paths (which contain ids); it logs the chi
// route pattern instead.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			attrs := []any{
				"method", r.Method,
				"route", routePattern(r.Context()),
				"status", rec.status,
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"bytes", rec.bytes,
				"request_id", GetRequestID(r.Context()),
			}

			if info, ok := requestctx.RequestInfoFromContext(r.Context()); ok {
				if p, ok := info.Principal(); ok {
					attrs = append(attrs, "org_id", p.OrgID.String())
					if p.Kind == requestctx.KindUser {
						attrs = append(attrs, "user_id", p.ActorID.String())
					} else {
						attrs = append(attrs, "api_key_id", p.ActorID.String())
					}
				}
			}

			logger := logging.FromContext(r.Context())
			if rec.status >= http.StatusInternalServerError {
				logger.Error("request", attrs...)
				return
			}
			logger.Info("request", attrs...)
		})
	}
}

// routePattern returns the matched chi pattern, or a stable placeholder for
// unmatched requests. The raw path is deliberately never returned.
func routePattern(ctx context.Context) string {
	if rctx := chi.RouteContext(ctx); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

// responseRecorder captures status and bytes while staying transparent to
// http.ResponseController via Unwrap.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (rr *responseRecorder) WriteHeader(code int) {
	if rr.wrote {
		// Mirror net/http: a superfluous WriteHeader is ignored.
		return
	}
	rr.wrote = true
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(p []byte) (int, error) {
	if !rr.wrote {
		rr.WriteHeader(http.StatusOK)
	}
	n, err := rr.ResponseWriter.Write(p)
	rr.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rr *responseRecorder) Unwrap() http.ResponseWriter {
	return rr.ResponseWriter
}
