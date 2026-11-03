package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
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
				l := base.With("request_id", requestctx.RequestID(r.Context()))
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
				"route", httpx.RoutePattern(r.Context()),
				"status", rec.status,
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"bytes", rec.bytes,
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

// routePattern helper removed in favour of httpx.RoutePattern.

// responseRecorder captures status and bytes while staying transparent to
// http.ResponseController via Unwrap.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

// WroteHeader reports whether the handler has already committed a status.
func (rr *responseRecorder) WroteHeader() bool {
	return rr.wrote
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
